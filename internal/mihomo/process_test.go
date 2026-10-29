package mihomo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeCore(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "core")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartRejectsInvalidCandidateAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	binary := fakeCore(t, "echo private-config-value >&2\nexit 1\n")
	_, err := Start(context.Background(), Options{Binary: binary, Directory: dir, ControllerAddress: "127.0.0.1:19090"})
	if err == nil {
		t.Fatal("accepted rejected config")
	}
	if strings.Contains(err.Error(), "private-config-value") {
		t.Fatal("leaked core diagnostics")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("left secret artifacts after validation failed")
	}
}

func TestStopReapsChildAndRemovesRuntimeArtifact(t *testing.T) {
	binary := fakeCore(t, "if [ \"$1\" = -t ]; then exit 0; fi\nexec sleep 300\n")
	dir := t.TempDir()
	p, err := Start(context.Background(), Options{Binary: binary, Directory: dir, ControllerAddress: "127.0.0.1:19090"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	info, err := os.Stat(p.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("config permissions are not private")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("child not reaped")
	}
	if err := syscall.Kill(p.cmd.Process.Pid, 0); err == nil {
		t.Fatal("child remains after stop")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("runtime artifacts remain")
	}
}

func TestStopKillsChildWhenGracePeriodExpires(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("TEST_READY_FILE", ready)
	binary := fakeCore(t, "if [ \"$1\" = -t ]; then exit 0; fi\ntrap '' TERM\ntouch \"$TEST_READY_FILE\"\nexec sleep 300\n")
	p, err := Start(context.Background(), Options{Binary: binary, Directory: t.TempDir(), ControllerAddress: "127.0.0.1:19090"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper not ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("child not reaped after forced termination")
	}
}
