package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func TestLiveCoreHelper(t *testing.T) {
	if os.Getenv("LIVE_CORE_HELPER") != "1" {
		return
	}
	for _, arg := range os.Args {
		if arg == "-t" {
			if _, err := os.Stat(os.Getenv("LIVE_REJECT_FILE")); err == nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	l, err := net.Listen("tcp", os.Getenv("LIVE_LISTEN"))
	if err != nil {
		os.Exit(2)
	}
	defer l.Close()
	for {
		c, err := l.Accept()
		if err != nil {
			os.Exit(3)
		}
		c.Close()
	}
}

func liveProcess(t *testing.T, handler http.HandlerFunc) (*Process, *node.VLESS, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"version":"v1.19.33"}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	address := l.Addr().String()
	l.Close()
	vpn := &node.VLESS{Listen: "127.0.0.1", Port: port, Users: []node.User{{Name: "alice", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}}, Reality: node.Reality{Destination: "target:8443", PrivateKey: "a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s", ShortIDs: []string{"abcd"}, ServerNames: []string{"target.test"}}}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reject := filepath.Join(t.TempDir(), "reject")
	t.Setenv("LIVE_CORE_HELPER", "1")
	t.Setenv("LIVE_TEST_BINARY", exe)
	t.Setenv("LIVE_LISTEN", address)
	t.Setenv("LIVE_REJECT_FILE", reject)
	binary := fakeCore(t, "exec \"$LIVE_TEST_BINARY\" -test.run='^TestLiveCoreHelper$' -- \"$@\"\n")
	p, err := Start(context.Background(), Options{Binary: binary, Directory: t.TempDir(), ControllerAddress: strings.TrimPrefix(srv.URL, "http://"), VLESS: vpn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		p.Stop(ctx)
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := p.Ready(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper listener not ready")
		}
		time.Sleep(time.Millisecond * 5)
	}
	return p, vpn, reject
}

func TestApplyUsersReloadsAndRetainsVerifiedArtifact(t *testing.T) {
	var payload []byte
	p, vpn, _ := liveProcess(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.RequestURI() != "/configs?force=true" {
			t.Error("unexpected reload request")
		}
		var body struct {
			Payload string `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		payload = []byte(body.Payload)
		w.WriteHeader(http.StatusNoContent)
	})
	pid := p.cmd.Process.Pid
	vpn.Reality.ShortIDs[0] = "invalid"
	vpn.Reality.ServerNames[0] = "invalid name"
	vpn.Port = 1
	users := []node.User{{Name: "bob", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d480"}}
	if err := p.ApplyUsers(context.Background(), users); err != nil {
		t.Fatal(err)
	}
	users[0].Name = "changed"
	disk, err := os.ReadFile(p.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != string(payload) || !strings.Contains(string(disk), `"bob"`) || strings.Contains(string(disk), `"alice"`) {
		t.Fatal("verified artifact does not match desired snapshot")
	}
	if p.cmd.Process.Pid != pid {
		t.Fatal("core restarted")
	}
	if err := p.ApplyUsers(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"users":[]`) {
		t.Fatal("last user was not removed")
	}
}

func TestApplyUsersValidationFailureDoesNotMutateRuntime(t *testing.T) {
	var calls atomic.Int32
	p, _, reject := liveProcess(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) })
	before, _ := os.ReadFile(p.configPath)
	if err := os.WriteFile(reject, []byte("reject"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.ApplyUsers(context.Background(), nil); err == nil {
		t.Fatal("accepted rejected candidate")
	}
	after, _ := os.ReadFile(p.configPath)
	if calls.Load() != 0 || string(before) != string(after) {
		t.Fatal("validation failure changed runtime or verified artifact")
	}
}

func TestApplyUsersAmbiguousFailureDoesNotRollback(t *testing.T) {
	var calls atomic.Int32
	p, _, _ := liveProcess(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		w.Write([]byte("secret-config-diagnostic"))
	})
	before, _ := os.ReadFile(p.configPath)
	err := p.ApplyUsers(context.Background(), nil)
	if err == nil || strings.Contains(err.Error(), "secret-config-diagnostic") {
		t.Fatal("missing or unsanitized failure")
	}
	after, _ := os.ReadFile(p.configPath)
	if calls.Load() != 1 || string(before) != string(after) {
		t.Fatal("ambiguous apply rolled back or replaced verified artifact")
	}
}

func TestApplyUsersRequiresOwnedListenerAfterReload(t *testing.T) {
	var p *Process
	p, _, _ = liveProcess(t, func(w http.ResponseWriter, r *http.Request) {
		if err := p.cmd.Process.Kill(); err != nil {
			t.Error("cannot stop helper", err)
			w.WriteHeader(500)
			return
		}
		// Kill only sends a signal. Wait for cmd.Wait to observe exit before
		// replying; Done also waits for this in-flight operation and would deadlock.
		select {
		case <-p.lifetime.Done():
		case <-time.After(5 * time.Second):
			t.Error("helper did not exit after kill")
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	if err := p.ApplyUsers(context.Background(), nil); err == nil {
		t.Fatal("204 without listener counted as applied")
	}
}

func TestStopCancelsLiveApplicationAndRejectsLaterApply(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	p, _, _ := liveProcess(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(204) })
	result := make(chan error, 1)
	go func() { result <- p.ApplyUsers(context.Background(), nil) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := p.Stop(ctx)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("apply succeeded during shutdown")
	}
	if err := p.ApplyUsers(context.Background(), nil); err == nil {
		t.Fatal("stopped process accepted apply")
	}
}

func TestSweepRevokedSelectsExactIdentities(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		if r.Method == "GET" {
			w.Write([]byte(`{"connections":[{"id":"one","metadata":{"inboundUser":"alice"}},{"id":"two","metadata":{"inboundUser":"alice2"}},{"id":"three","metadata":{"inboundUser":""}}]}`))
			return
		}
		deleted = append(deleted, r.URL.Path)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	n, err := NewController(srv.URL, "token").SweepRevoked(context.Background(), []string{"alice"})
	if err != nil || n != 1 || len(deleted) != 1 || deleted[0] != "/connections/one" {
		t.Fatalf("wrong selective cleanup: %d, %v, %v", n, err, deleted)
	}
}

func TestSweepRevokedRejectsIncompleteSnapshotsBeforeDeleting(t *testing.T) {
	for _, body := range []string{`{}`, `{"connections":[`, `{"connections":[{"id":"one"}]}`, `{"connections":[{"id":"","metadata":{"inboundUser":"alice"}}]}`, `{"connections":[{"id":"../","metadata":{"inboundUser":"alice"}}]}`, `{"connections":[]} {}`, `{"connections":[]}` + strings.Repeat(" ", 8<<20)} {
		t.Run("invalid snapshot", func(t *testing.T) {
			var deletes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					deletes.Add(1)
					w.WriteHeader(204)
					return
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			_, err := NewController(srv.URL, "").SweepRevoked(context.Background(), []string{"alice"})
			if err == nil || deletes.Load() != 0 {
				t.Fatal("accepted invalid snapshot or performed partial deletion")
			}
		})
	}
}

func TestLiveOperationsSerializeAndWaitingCallsCanCancel(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	p, _, _ := liveProcess(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.WriteHeader(204)
	})
	result := make(chan error, 1)
	go func() { result <- p.ApplyUsers(context.Background(), nil) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.SweepRevoked(ctx, []string{"alice"}); err == nil {
		t.Fatal("waiting sweep ignored cancellation")
	}
	if calls.Load() != 1 {
		t.Fatal("cleanup overlapped credential application")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := p.ApplyUsers(context.Background(), nil); err != nil {
		t.Fatal("operation gate did not recover", err)
	}
}

func TestSweepRevokedRepeatsForLateSessions(t *testing.T) {
	var samples, deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes.Add(1)
			w.WriteHeader(204)
			return
		}
		if samples.Add(1) == 1 {
			w.Write([]byte(`{"connections":[]}`))
			return
		}
		w.Write([]byte(`{"connections":[{"id":"late","metadata":{"inboundUser":"alice"}}]}`))
	}))
	defer srv.Close()
	c := NewController(srv.URL, "")
	if n, err := c.SweepRevoked(context.Background(), []string{"alice"}); err != nil || n != 0 {
		t.Fatal("first empty observation failed")
	}
	if n, err := c.SweepRevoked(context.Background(), []string{"alice"}); err != nil || n != 1 || deletes.Load() != 1 {
		t.Fatal("late session was not closed")
	}
}

func TestSweepRevokedBoundsDeletesAndReportsPartialFailure(t *testing.T) {
	for _, failedDelete := range []int32{0, 3} {
		t.Run("bounded sweep", func(t *testing.T) {
			connections := make([]map[string]any, 129)
			for i := range connections {
				connections[i] = map[string]any{"id": fmt.Sprintf("session-%d", i), "metadata": map[string]string{"inboundUser": "alice"}}
			}
			body, err := json.Marshal(map[string]any{"connections": connections})
			if err != nil {
				t.Fatal(err)
			}
			var deletes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.Write(body)
					return
				}
				if deletes.Add(1) == failedDelete {
					w.WriteHeader(500)
					w.Write([]byte("private-error"))
					return
				}
				w.WriteHeader(204)
			}))
			defer srv.Close()
			n, err := NewController(srv.URL, "").SweepRevoked(context.Background(), []string{"alice"})
			expected := 128
			expectedCalls := int32(128)
			if failedDelete != 0 {
				expected = int(failedDelete) - 1
				expectedCalls = failedDelete
			}
			if err == nil || n != expected || deletes.Load() != expectedCalls || strings.Contains(err.Error(), "private-error") {
				t.Fatal("cleanup did not report bounded partial progress")
			}
		})
	}
}

func TestApplyUsersRejectsInvalidUsersBeforeReload(t *testing.T) {
	var calls atomic.Int32
	p, _, _ := liveProcess(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) })
	if err := p.ApplyUsers(context.Background(), []node.User{{Name: "alice", UUID: "secret-invalid"}}); err == nil || strings.Contains(err.Error(), "secret-invalid") {
		t.Fatal("invalid credential accepted or exposed")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid users sent to core")
	}
}
