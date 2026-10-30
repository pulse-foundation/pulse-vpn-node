package mihomo

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func TestListenerOwnershipRejectsReachableForeignSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reference runtime is Linux Docker")
	}
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if err := ownsListener(context.Background(), os.Getpid(), port); err != nil {
		t.Fatal("did not recognize our own listener:", err)
	}
	if err := ownsListener(context.Background(), child.Process.Pid, port); err == nil {
		t.Fatal("accepted reachable foreign listener")
	}
	_ = l.Close()
	if err := ownsListener(context.Background(), os.Getpid(), port); err == nil {
		t.Fatal("accepted closed listener")
	}
}

func TestReadyRejectsPortClaimedDuringCoreValidation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reference runtime is Linux Docker")
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	dir := t.TempDir()
	marker, release := filepath.Join(dir, "validating"), filepath.Join(dir, "continue")
	t.Setenv("TEST_VALIDATING", marker)
	t.Setenv("TEST_CONTINUE", release)
	binary := fakeCore(t, "if [ \"$1\" = -t ]; then\ntouch \"$TEST_VALIDATING\"\nwhile [ ! -f \"$TEST_CONTINUE\" ]; do sleep 0.01; done\nexit 0\nfi\nexec sleep 300\n")
	vpn := &node.VLESS{Listen: "127.0.0.1", Port: port, Users: []node.User{{Name: "local", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}}, Reality: node.Reality{Destination: "target:8443", PrivateKey: "a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s", ShortIDs: []string{"abcd"}, ServerNames: []string{"target.test"}}}
	type result struct {
		p   *Process
		err error
	}
	results := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		p, err := Start(ctx, Options{Binary: binary, Directory: dir, ControllerAddress: "127.0.0.1:19090", VLESS: vpn})
		results <- result{p, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("core validation never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	foreign, err := net.Listen("tcp", vpn.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	r := <-results
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = r.p.Stop(stop)
	}()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"v1.19.33"}`))
	}))
	defer controller.Close()
	r.p.controller = NewController(controller.URL, "synthetic-token")
	if _, err := r.p.Ready(context.Background()); err == nil {
		t.Fatal("reported ready for a foreign port acquired after preflight")
	}
}
