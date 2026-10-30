package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type trafficUser struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Upload   string `json:"uploadBytes"`
	Download string `json:"downloadBytes"`
}
type trafficSnapshot struct {
	Approximate         bool          `json:"approximate"`
	PollIntervalSeconds int           `json:"pollIntervalSeconds"`
	State               string        `json:"state"`
	Stale               bool          `json:"stale"`
	Error               string        `json:"error"`
	ObservedAt          *time.Time    `json:"observedAt"`
	Users               []trafficUser `json:"users"`
	ResetCount          string        `json:"resetCount"`
	SkippedConnections  int           `json:"skippedConnections"`
}
type trafficBytes struct{ Upload, Download uint64 }

func trafficRead() (trafficSnapshot, error) {
	var s trafficSnapshot
	if err := userAPI("GET", "traffic", nil, 200, &s); err != nil {
		return s, err
	}
	if !s.Approximate || s.PollIntervalSeconds != 2 || s.SkippedConnections < 0 {
		return s, errors.New("traffic API metadata mismatch")
	}
	if s.State != "starting" && s.State != "collecting" && s.State != "degraded" {
		return s, errors.New("traffic API state invalid")
	}
	if _, err := decimalCounter(s.ResetCount); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, u := range s.Users {
		if seen[u.Name] || u.Name == "" {
			return s, errors.New("duplicate or empty traffic user")
		}
		seen[u.Name] = true
		if _, err := decimalCounter(u.Upload); err != nil {
			return s, err
		}
		if _, err := decimalCounter(u.Download); err != nil {
			return s, err
		}
	}
	return s, nil
}
func decimalCounter(value string) (uint64, error) {
	if value == "" {
		return 0, errors.New("traffic counter is not a decimal string")
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, errors.New("traffic counter is not a decimal string")
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("traffic counter out of range")
	}
	return n, nil
}
func trafficTotals(s trafficSnapshot) map[string]trafficBytes {
	totals := make(map[string]trafficBytes, len(s.Users))
	for _, u := range s.Users {
		upload, _ := decimalCounter(u.Upload)
		download, _ := decimalCounter(u.Download)
		totals[u.Name] = trafficBytes{upload, download}
	}
	return totals
}
func sameTraffic(a, b trafficSnapshot) bool {
	x, y := trafficTotals(a), trafficTotals(b)
	if len(x) != len(y) {
		return false
	}
	for name, value := range x {
		if other, ok := y[name]; !ok || value != other {
			return false
		}
	}
	return true
}

// Wait for three distinct successful collector observations with equal totals.
// Numerous API reads between polls must not change the source's observation time.
func stableTraffic() (trafficSnapshot, error) {
	deadline := time.Now().Add(30 * time.Second)
	var last trafficSnapshot
	stable, cached := 0, false
	for time.Now().Before(deadline) {
		s, err := trafficRead()
		if err == nil && s.State == "collecting" && !s.Stale && s.Error == "" && s.ObservedAt != nil {
			if last.ObservedAt == nil || !sameTraffic(last, s) {
				stable = 0
			} else if !last.ObservedAt.Equal(*s.ObservedAt) {
				stable++
			} else {
				cached = true
			}
			last = s
			if stable >= 2 && cached {
				return s, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last, errors.New("traffic failed to settle across collector polls")
}
func trafficDelta(before, after trafficSnapshot, name string, upload, download uint64) error {
	old, current := trafficTotals(before), trafficTotals(after)
	if len(old) != 2 || len(current) != 2 {
		return errors.New("expected two traffic users")
	}
	for key, value := range old {
		next, ok := current[key]
		if !ok || next.Upload < value.Upload || next.Download < value.Download {
			return errors.New("traffic totals regressed")
		}
		wantUp, wantDown := uint64(0), uint64(0)
		if key == name {
			wantUp, wantDown = upload, download
		}
		if next.Upload-value.Upload != wantUp || next.Download-value.Download != wantDown {
			return fmt.Errorf("%s traffic delta: got %d/%d, want %d/%d", key, next.Upload-value.Upload, next.Download-value.Download, wantUp, wantDown)
		}
	}
	return nil
}
func openTrafficTCP(proxy string) (*tcpSession, error) {
	conn, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		return nil, errors.New("traffic TCP proxy unavailable")
	}
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := io.WriteString(conn, "CONNECT target:9002 HTTP/1.1\r\nHost: target:9002\r\n\r\n"); err != nil {
		conn.Close()
		return nil, errors.New("traffic tunnel request failed")
	}
	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || res.StatusCode != 200 {
		conn.Close()
		return nil, errors.New("traffic tunnel rejected")
	}
	return &tcpSession{conn: conn, reader: reader}, nil
}
func trafficTCP(s *tcpSession, upload, download int) error {
	_ = s.conn.SetDeadline(time.Now().Add(5 * time.Second))
	packet := make([]byte, 8+upload)
	binary.BigEndian.PutUint32(packet[:4], uint32(upload))
	binary.BigEndian.PutUint32(packet[4:8], uint32(download))
	if _, err := io.Copy(s.conn, bytes.NewReader(packet)); err != nil {
		return errors.New("traffic TCP upload failed")
	}
	got := make([]byte, download)
	if _, err := io.ReadFull(s.reader, got); err != nil || !bytes.Equal(got, bytes.Repeat([]byte{0x5a}, download)) {
		return errors.New("traffic TCP download failed")
	}
	return nil
}
func trafficUDP(s *udpSession, upload, download int) error {
	target := net.ParseIP(os.Getenv("REALITY_TEST_TARGET_IP")).To4()
	if target == nil {
		return errors.New("test target IPv4 is required")
	}
	packet := append([]byte{0, 0, 0, 1}, target...)
	packet = append(packet, byte(9003>>8), byte(9003&255))
	payload := make([]byte, upload)
	binary.BigEndian.PutUint16(payload, uint16(download))
	packet = append(packet, payload...)
	_ = s.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := s.conn.Write(packet); err != nil {
		return errors.New("traffic UDP upload failed")
	}
	response := make([]byte, 2048)
	n, err := s.conn.Read(response)
	if err != nil {
		return errors.New("traffic UDP response timed out")
	}
	reader := bytes.NewReader(response[:n])
	var header [4]byte
	var port [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil || header[0] != 0 || header[1] != 0 || header[2] != 0 {
		return errors.New("traffic UDP frame invalid")
	}
	if _, err := readAddress(reader, header[3]); err != nil {
		return errors.New("traffic UDP address invalid")
	}
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return errors.New("traffic UDP port invalid")
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{0x5a}, download)) {
		return errors.New("traffic UDP download mismatch")
	}
	return nil
}
func trafficAuth() error {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := client.Get("http://node:8080/api/v1/traffic")
	if err != nil {
		return errors.New("traffic authentication probe unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		return errors.New("traffic API accepts unauthenticated requests")
	}
	return nil
}
func liveTraffic() error {
	if err := trafficAuth(); err != nil {
		return err
	}
	uuid, err := os.ReadFile(filepath.Join(fixtureDir, "second-uuid"))
	if err != nil {
		return errors.New("second user fixture unavailable")
	}
	if err := mutateUser("second-test", true, string(uuid)); err != nil {
		return err
	}
	firstTCP, err := openTrafficTCP("client:7890")
	if err != nil {
		return err
	}
	defer firstTCP.conn.Close()
	secondTCP, err := openTrafficTCP("second:7890")
	if err != nil {
		return err
	}
	defer secondTCP.conn.Close()
	firstUDP, err := openUDP("client:7890")
	if err != nil {
		return err
	}
	defer firstUDP.close()
	secondUDP, err := openUDP("second:7890")
	if err != nil {
		return err
	}
	defer secondUDP.close()
	before, err := stableTraffic()
	if err != nil {
		return err
	}
	checks := []struct {
		name, transport  string
		upload, download uint64
		send             func() error
	}{
		{"local-test", "TCP", 4104, 16384, func() error { return trafficTCP(firstTCP, 4096, 16384) }},
		{"second-test", "TCP", 16392, 4096, func() error { return trafficTCP(secondTCP, 16384, 4096) }},
		{"local-test", "UDP", 1024, 256, func() error { return trafficUDP(firstUDP, 1024, 256) }},
		{"second-test", "UDP", 256, 1024, func() error { return trafficUDP(secondUDP, 256, 1024) }},
	}
	for _, check := range checks {
		if err := check.send(); err != nil {
			return err
		}
		after, err := stableTraffic()
		if err != nil {
			return err
		}
		if err := trafficDelta(before, after, check.name, check.upload, check.download); err != nil {
			return fmt.Errorf("%s: %w", check.transport, err)
		}
		before = after
		fmt.Printf("PASS: %s asymmetric %s bytes have correct direction and user isolation\n", check.name, check.transport)
	}
	fmt.Println("PASS: authenticated traffic API uses decimal strings; repeated reads/idle polls do not increment counters")
	if err := coordinate("database outage"); err != nil {
		return err
	}
	if err := userAPI("GET", "traffic", nil, 503, nil); err != nil {
		return err
	}
	if err := trafficTCP(firstTCP, 8192, 32768); err != nil {
		return err
	}
	if err := trafficUDP(firstUDP, 512, 128); err != nil {
		return err
	}
	// Keep the database unavailable across at least two scheduled collector attempts.
	<-time.After(5 * time.Second)
	if err := coordinate("database recovery"); err != nil {
		return err
	}
	after, err := stableTraffic()
	if err != nil {
		return err
	}
	if err := trafficDelta(before, after, "local-test", 8200+512, 32768+128); err != nil {
		return fmt.Errorf("database recovery: %w", err)
	}
	fmt.Println("PASS: existing TCP/UDP survive database outage; recovery catches up once without double counting")
	before = after
	if err := mutateUser("second-test", false, ""); err != nil {
		return err
	}
	after, err = stableTraffic()
	if err != nil {
		return err
	}
	if !sameTraffic(before, after) {
		return errors.New("disabling user changed retained traffic totals")
	}
	disabled := false
	for _, user := range after.Users {
		if user.Name == "second-test" {
			disabled = !user.Enabled
		}
	}
	if !disabled {
		return errors.New("disabled user missing from traffic API")
	}
	fmt.Println("PASS: disabling a user retains collected traffic totals")
	firstTCP.conn.Close()
	secondTCP.conn.Close()
	firstUDP.close()
	secondUDP.close()
	baseline, err := stableTraffic()
	if err != nil {
		return err
	}
	if !sameTraffic(after, baseline) {
		return errors.New("closing idle sessions changed traffic totals")
	}
	data, err := json.Marshal(baseline)
	if err != nil {
		return errors.New("traffic baseline encoding failed")
	}
	if err := os.WriteFile("/traffic-baseline/baseline.json", data, 0600); err != nil {
		return errors.New("traffic baseline could not be persisted")
	}
	return nil
}
func persistedTraffic() error {
	data, err := os.ReadFile("/traffic-baseline/baseline.json")
	if err != nil {
		return errors.New("traffic baseline unavailable")
	}
	var before trafficSnapshot
	if err := json.Unmarshal(data, &before); err != nil {
		return errors.New("invalid traffic baseline")
	}
	after, err := stableTraffic()
	if err != nil {
		return err
	}
	if !sameTraffic(before, after) {
		return errors.New("traffic totals changed after node/core restart")
	}
	if err := requireUsers(true, false); err != nil {
		return err
	}
	for _, user := range after.Users {
		if user.Name == "second-test" && user.Enabled {
			return errors.New("traffic API lost disabled state after restart")
		}
	}
	fmt.Println("PASS: per-user totals and disabled user survive node/core restart without recounting")
	return nil
}
