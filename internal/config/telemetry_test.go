package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryDefaultsDoNotProbePublicTargets(t *testing.T) {
	c, err := loadTelemetry("")
	if err != nil || c.SampleIntervalSeconds != 5 || c.ProbeIntervalSeconds != 30 || len(c.Probes.Targets) != 0 || c.Resources.HostProc != "" {
		t.Fatal("unsafe telemetry defaults")
	}
}

func TestTelemetrySettingsValidation(t *testing.T) {
	for _, body := range []string{
		`null`,
		`{"probes":{"targets":[{"name":"dns","type":"dns","address":"192.0.2.1"}]}}`,
		`{"probes":{"targets":[{"name":"dns","type":"dns","address":"::1"}]}}`,
		`{"probes":{"targets":[{"name":"url","type":"https","address":"https://example.test/?"}]}}`,
		`{"sampleIntervalSeconds":0}`, `{"probeIntervalSeconds":1}`, `{"probeTimeoutMilliseconds":999999}`,
		`{"probes":{"concurrency":50}}`, `{"unknown":true}`, `{} {}`,
		`{"resources":{"hostProc":"relative/path"}}`,
		`{"resources":{"filesystems":[{"name":"root","path":"/","scope":"vps"}]}}`,
		`{"probes":{"targets":[{"name":"private","type":"https","address":"https://secret:password@example.test/"}]}}`,
		`{"probes":{"targets":[{"name":"private","type":"https","address":"https://example.test/?secret=token"}]}}`,
		`{"probes":{"targets":[{"name":"private","type":"tcp","address":"example.test:0"}]}}`,
		`{"probes":{"targets":[{"name":"private","type":"icmp","address":"example.test"}]}}`,
		`{"resources":{"containerInterfaces":["eth0","eth0"]}}`, strings.Repeat("x", 16385),
	} {
		t.Run("invalid", func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "settings")
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadTelemetry(p)
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), body) {
				t.Fatal("invalid input accepted or details leaked")
			}
		})
	}
	body := `{"sampleIntervalSeconds":1,"resources":{"hostProc":"/host/proc","hostInterfaces":["eth0"],"disks":["vda"],"filesystems":[{"name":"root","path":"/host/fs/root","scope":"host"}]},"probes":{"targets":[{"name":"local","type":"tcp","address":"127.0.0.1:443"}]}}`
	p := filepath.Join(t.TempDir(), "settings")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadTelemetry(p)
	if err != nil || c.Resources.HostProc != "/host/proc" || c.SampleIntervalSeconds != 1 || c.Probes.Timeout.Milliseconds() != 2000 {
		t.Fatal("valid explicit settings rejected")
	}
}
