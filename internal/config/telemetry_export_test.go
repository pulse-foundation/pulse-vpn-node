package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

func TestTelemetryEnrollmentPrivateBoundedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.json")
	cfg, err := LoadTelemetryExport(path)
	if err != nil || cfg != nil {
		t.Fatal("missing enrollment must leave exporter disabled")
	}
	value := telemetry.ExportConfig{SchemaVersion: 1, Endpoint: "https://receiver.example/api/v1/node-ingest", EnrollmentGeneration: "4c9bc671-b3e3-40f9-89ab-cf8682ce7f8e", Credential: "synthetic-private-credential"}
	data, _ := json.Marshal(value)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadTelemetryExport(path)
	if err != nil || cfg == nil || cfg.Credential != value.Credential {
		t.Fatal("valid private enrollment not loaded")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTelemetryExport(path); err == nil || strings.Contains(err.Error(), value.Credential) {
		t.Fatal("unsafe permissions not rejected safely")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 16385)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTelemetryExport(path); err == nil {
		t.Fatal("oversized enrollment accepted")
	}
}
