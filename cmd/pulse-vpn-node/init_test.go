package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCommand(t *testing.T) {
	t.Setenv("PULSE_NODE_TOKEN_FILE", "")
	dir := filepath.Join(t.TempDir(), "config")
	var out bytes.Buffer
	args := []string{"--config-dir", dir, "--reality-destination", "target.test:443", "--reality-server-names", "target.test"}
	if err := runInit(args, &out); err != nil {
		t.Fatal(err)
	}
	var result struct{ Created, Preserved []string }
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Created) != 4 {
		t.Fatal("invalid summary")
	}
	for _, name := range []string{"api-token", "database-password"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), strings.TrimSpace(string(raw))) {
			t.Fatal("secret leaked")
		}
	}
	out.Reset()
	if err := runInit([]string{"--config-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Preserved) != 4 {
		t.Fatal("repeat did not preserve settings")
	}
}
func TestInitFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown=DO_NOT_ECHO_SECRET"}, {"--port=DO_NOT_ECHO_SECRET"},
		{"--port=0"}, {"--port=-1"}, {"--port=65536"}, {"positional"},
		{"--listen="}, {"--reality-destination="}, {"--reality-server-names="},
	} {
		var out bytes.Buffer
		err := runInit(args, &out)
		if err == nil {
			t.Fatalf("invalid arguments accepted: %q", args)
		}
		if strings.Contains(err.Error(), "DO_NOT_ECHO_SECRET") || out.Len() != 0 {
			t.Fatal("unsafe diagnostic output")
		}
	}
	var out bytes.Buffer
	if err := runInit([]string{"--help"}, &out); err != nil || !strings.Contains(out.String(), "--config-dir") {
		t.Fatal("help unavailable")
	}
}
func TestInitDispatchBeforeRuntimeConfig(t *testing.T) {
	t.Setenv("PULSE_NODE_TOKEN_FILE", "/nonexistent")
	previous := os.Args
	os.Args = []string{"pulse-vpn-node", "init", "--unknown"}
	t.Cleanup(func() { os.Args = previous })
	if err := run(); err == nil || !strings.Contains(err.Error(), "init arguments") {
		t.Fatal("init loaded runtime config")
	}
}
