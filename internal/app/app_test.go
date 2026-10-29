package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
)

func TestStartupCancellationReapsStartedChild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	t.Setenv("TEST_CORE_PID", pidFile)
	binary := filepath.Join(dir, "core")
	script := "#!/bin/sh\nif [ \"$1\" = -t ]; then exit 0; fi\necho $$ > \"$TEST_CORE_PID\"\nexec sleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := Run(ctx, config.Config{Listen: "127.0.0.1:0", Token: strings.Repeat("x", 64), Binary: binary, RuntimeDir: filepath.Join(dir, "runtime")})
	if err == nil {
		t.Fatal("reported ready without a controller")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal("child never started", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatal("orphan after startup cancellation")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "runtime"))
	if err != nil || len(entries) != 0 {
		t.Fatal("runtime artifacts remain", err)
	}
}

func TestOccupiedAPIAddressFailsBeforeStartingCore(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dir := filepath.Join(t.TempDir(), "runtime")
	err = Run(context.Background(), config.Config{Listen: listener.Addr().String(), Binary: "/missing", RuntimeDir: dir})
	if err == nil || err.Error() != "cannot bind management listener" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("core startup attempted despite occupied API port")
	}
}
