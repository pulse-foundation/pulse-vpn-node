package config

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
)

type Config struct {
	Listen     string
	Token      string
	Binary     string
	RuntimeDir string
}

func Load() (Config, error) {
	file := os.Getenv("PULSE_NODE_TOKEN_FILE")
	if file == "" {
		return Config{}, errors.New("PULSE_NODE_TOKEN_FILE is required")
	}
	f, err := os.Open(file)
	if err != nil {
		return Config{}, errors.New("cannot open API token file")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return Config{}, errors.New("cannot read API token file")
	}
	token := strings.TrimRight(string(raw), "\r\n")
	if len(raw) > 4096 || len(token) < 32 || len(token) > 256 {
		return Config{}, errors.New("API token must contain 32 to 256 printable non-space ASCII characters")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return Config{}, errors.New("API token contains invalid characters")
		}
	}
	cfg := Config{Listen: envDefault("PULSE_NODE_LISTEN", "127.0.0.1:8080"), Token: token, Binary: envDefault("PULSE_NODE_MIHOMO_BINARY", "/usr/local/bin/mihomo"), RuntimeDir: envDefault("PULSE_NODE_RUNTIME_DIR", "/run/pulse-node")}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return Config{}, errors.New("invalid PULSE_NODE_LISTEN address")
	}
	return cfg, nil
}

func envDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
