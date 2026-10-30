package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresTokenFile(t *testing.T) {
	t.Setenv("PULSE_NODE_TOKEN_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("accepted missing token file")
	}
}

func TestLoadValidatesTokenWithoutLeakingIt(t *testing.T) {
	for _, token := range []string{"", "short-secret", strings.Repeat("x", 4097), strings.Repeat("x", 32) + " embedded space"} {
		t.Run("invalid", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PULSE_NODE_TOKEN_FILE", path)
			_, err := Load()
			if err == nil {
				t.Fatal("accepted invalid token")
			}
			if token != "" && strings.Contains(err.Error(), token) {
				t.Fatal("error exposed token")
			}
		})
	}
}

func TestLoadDefaultsAndMountedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PULSE_NODE_TOKEN_FILE", path)
	t.Setenv("PULSE_NODE_LISTEN", "")
	t.Setenv("PULSE_NODE_RUNTIME_DIR", "")
	t.Setenv("PULSE_NODE_MIHOMO_BINARY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != token || cfg.Listen != "127.0.0.1:8080" || cfg.RuntimeDir != "/run/pulse-node" || cfg.Binary != "/usr/local/bin/mihomo" {
		t.Fatal("unexpected defaults or token loading")
	}
}
