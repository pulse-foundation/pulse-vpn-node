package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type usersSnapshot struct {
	Revision int64 `json:"revision"`
	Users    []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"users"`
	Runtime struct {
		AppliedRevision int64  `json:"appliedRevision"`
		CleanupState    string `json:"cleanupState"`
	} `json:"runtime"`
}
type userOperation struct {
	Revision int64  `json:"revision"`
	State    string `json:"state"`
}

// Failures deliberately omit response bodies, request credentials, and transport errors.
func userAPI(method, path string, body any, status int, out any) error {
	token, err := os.ReadFile(filepath.Join(fixtureDir, "api-token"))
	if err != nil {
		return errors.New("fixture token unavailable")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid test request")
		}
	}
	req, err := http.NewRequest(method, "http://node:8080/api/v1/"+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid test path")
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newUUID())
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("user API unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != status {
		return fmt.Errorf("user API status: got %d, want %d", res.StatusCode, status)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return errors.New("user API response unreadable")
	}
	if bytes.Contains(data, []byte(`"uuid"`)) {
		return errors.New("user API exposed credential field")
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return errors.New("invalid user API response")
	}
	return nil
}
func userSnapshot() (usersSnapshot, error) {
	var s usersSnapshot
	err := userAPI("GET", "users", nil, 200, &s)
	return s, err
}
func awaitRevision(revision int64) error {
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		var op userOperation
		if err := userAPI("GET", fmt.Sprintf("operations/%d", revision), nil, 200, &op); err != nil {
			return err
		}
		if op.Revision != revision {
			return errors.New("operation revision mismatch")
		}
		if op.State == "failed" {
			return errors.New("user operation failed")
		}
		s, err := userSnapshot()
		if err != nil {
			return err
		}
		if op.State == "applied" && s.Runtime.AppliedRevision == revision && s.Runtime.CleanupState == "observed_empty" {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("user operation did not apply before deadline")
}
func mutateUser(name string, enabled bool, uuid string) error {
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	body := map[string]any{"enabled": enabled, "expectedRevision": s.Revision}
	if uuid != "" {
		body["uuid"] = uuid
	}
	var op userOperation
	if err := userAPI("PUT", "users/"+name, body, 202, &op); err != nil {
		return err
	}
	if op.Revision <= s.Revision {
		return errors.New("mutation did not advance revision")
	}
	return awaitRevision(op.Revision)
}

type tcpSession struct {
	conn   net.Conn
	reader *bufio.Reader
}

func openTCP(proxy string) (*tcpSession, error) {
	conn, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		return nil, errors.New("TCP proxy unavailable")
	}
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err = io.WriteString(conn, "CONNECT target:9001 HTTP/1.1\r\nHost: target:9001\r\n\r\n"); err != nil {
		conn.Close()
		return nil, errors.New("TCP tunnel request failed")
	}
	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || res.StatusCode != 200 {
		conn.Close()
		return nil, errors.New("TCP tunnel rejected")
	}
	return &tcpSession{conn: conn, reader: reader}, nil
}
func (s *tcpSession) echo() error {
	_ = s.conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := make([]byte, 1024)
	_, _ = rand.Read(payload)
	if _, err := s.conn.Write(payload); err != nil {
		return errors.New("existing TCP session write failed")
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(s.reader, got); err != nil || !bytes.Equal(payload, got) {
		return errors.New("existing TCP session echo failed")
	}
	return nil
}

type liveSessions struct {
	tcp *tcpSession
	udp *udpSession
}

func openSessions(proxy string) (*liveSessions, error) {
	tcp, err := openTCP(proxy)
	if err != nil {
		return nil, err
	}
	udp, err := openUDP(proxy)
	if err != nil {
		tcp.conn.Close()
		return nil, err
	}
	s := &liveSessions{tcp: tcp, udp: udp}
	if err = s.echo(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *liveSessions) close() { s.tcp.conn.Close(); s.udp.close() }
func (s *liveSessions) echo() error {
	if err := s.tcp.echo(); err != nil {
		return err
	}
	return s.udp.echo()
}
func (s *liveSessions) revoked() error {
	// A successful read of zero bytes/EOF proves TCP closure, rather than relying on
	// a timeout which could merely be a temporarily stalled connection.
	_ = s.tcp.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var b [1]byte
	_, err := s.tcp.reader.Read(b[:])
	if err == nil {
		return errors.New("revoked TCP connection still readable")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		return errors.New("revoked TCP connection was not closed")
	}
	return s.udp.revoked()
}
func coordinate(stage string) error {
	fmt.Println("READY: " + stage)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || line != "continue\n" {
		return errors.New("test coordinator unavailable")
	}
	return nil
}
func requireUsers(original, second bool) error {
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	if len(s.Users) != 2 {
		return errors.New("unexpected persisted user count")
	}
	found := map[string]bool{}
	for _, u := range s.Users {
		found[u.Name] = u.Enabled
	}
	first, ok1 := found["local-test"]
	next, ok2 := found["second-test"]
	if !ok1 || !ok2 || first != original || next != second {
		return errors.New("persisted user state mismatch")
	}
	return nil
}
func liveUsers() error {
	if err := probe(); err != nil {
		return err
	}
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	if len(s.Users) != 1 || s.Users[0].Name != "local-test" || !s.Users[0].Enabled {
		return errors.New("seed user import mismatch")
	}
	if err = expectRejected("second:7890"); err != nil {
		return err
	}
	original, err := openSessions("client:7890")
	if err != nil {
		return err
	}
	defer original.close()
	uuid, err := os.ReadFile(filepath.Join(fixtureDir, "second-uuid"))
	if err != nil {
		return errors.New("second user fixture unavailable")
	}
	if err = mutateUser("second-test", true, string(uuid)); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	second, err := openSessions("second:7890")
	if err != nil {
		return err
	}
	defer second.close()
	if err = mutateUser("second-test", false, ""); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	if err = second.revoked(); err != nil {
		return err
	}
	if err = expectRejected("second:7890"); err != nil {
		return err
	}
	fmt.Println("PASS: add and revoke preserve original TCP/UDP; revoked sessions close and new access fails")
	s, err = userSnapshot()
	if err != nil {
		return err
	}
	if err = coordinate("database outage"); err != nil {
		return err
	}
	if err = userAPI("PUT", "users/local-test", map[string]any{"enabled": false, "expectedRevision": s.Revision}, 503, nil); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	if err = coordinate("database recovery"); err != nil {
		return err
	}
	if err = requireUsers(true, false); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	fmt.Println("PASS: database outage rejects mutations and preserves existing TCP/UDP service")
	if err = mutateUser("local-test", false, ""); err != nil {
		return err
	}
	if err = original.revoked(); err != nil {
		return err
	}
	if err = expectRejected("client:7890"); err != nil {
		return err
	}
	if err = expectRejected("second:7890"); err != nil {
		return err
	}
	if err = mutateUser("local-test", true, ""); err != nil {
		return err
	}
	if err = probe(); err != nil {
		return err
	}
	if err = requireUsers(true, false); err != nil {
		return err
	}
	fmt.Println("PASS: all-disabled denies access; re-enabling restores service")
	return nil
}
func persistedUsers() error {
	if err := requireUsers(true, false); err != nil {
		return err
	}
	if err := probe(); err != nil {
		return err
	}
	if err := expectRejected("second:7890"); err != nil {
		return err
	}
	fmt.Println("PASS: desired users persist through node restart; revoked user stays denied")
	return nil
}
