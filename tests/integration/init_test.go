//go:build integration

package integration_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initialize exercises the published image entrypoint and the unmodified deployment
// Compose, rather than the test overlay's pre-generated credentials.
func (s *suite) initialize(t *testing.T, project string, idle bool) {
	s.prepare(t)
	s.clean(t, project)
	dir := t.TempDir()
	need(t, os.Chmod(dir, 0700) == nil, "cannot protect init directory")
	local := *s
	local.env = append(append([]string{}, s.env...),
		"PULSE_NODE_CONFIG_DIR="+dir, "PULSE_NODE_IMAGE="+nodeImage,
		"PULSE_NODE_VPN_BIND=127.0.0.1", "PULSE_NODE_VPN_PORT=0", "PULSE_NODE_PORT=0")
	compose := func(args ...string) []string {
		return append([]string{"compose", "--env-file", "/dev/null", "-p", project, "-f", "compose.yaml"}, args...)
	}
	t.Cleanup(func() {
		r := local.run(3*time.Minute, compose("down", "--volumes", "--remove-orphans")...)
		need(t, r.err == nil, "init Compose cleanup failed (output withheld)")
		s.clean(t, project)
	})
	initArgs := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--mount", "type=bind,src=" + dir + ",dst=/config", nodeImage, "init", "--config-dir", "/config"}
	freshArgs := append([]string{}, initArgs...)
	if idle {
		freshArgs = append(freshArgs, "--idle")
	} else {
		freshArgs = append(freshArgs, "--reality-destination", "target.test:443", "--reality-server-names", "target.test")
	}
	summary := decode[struct{ Created, Preserved []string }](t, local.docker(t, freshArgs...))
	need(t, len(summary.Created) == 4 && len(summary.Preserved) == 0, "fresh init did not create four files")
	before := map[string][]byte{}
	for _, name := range []string{"api-token", "database-password", "database-url", "vless.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		need(t, err == nil, "cannot read init output")
		before[name] = raw
	}
	summary = decode[struct{ Created, Preserved []string }](t, local.docker(t, initArgs...))
	need(t, len(summary.Created) == 0 && len(summary.Preserved) == 4, "repeat init did not preserve files")
	for name, raw := range before {
		after, err := os.ReadFile(filepath.Join(dir, name))
		need(t, err == nil && bytes.Equal(after, raw), "repeat init changed "+name)
	}
	failed := local.run(time.Minute, append(append([]string{}, initArgs...), "--port", "443")...)
	message := "conflict"
	if idle {
		message = "idle settings"
	}
	need(t, failed.expectedFailure(message), "init conflict was not rejected")
	local.docker(t, compose("up", "-d", "--no-build", "--pull", "missing", "--wait", "--wait-timeout", "120")...)
	waitNodeConfigured(t, func() string {
		return local.docker(t, compose("exec", "-T", "node", "pulse-vpn-node", "status")...)
	}, !idle)
	logs := local.docker(t, compose("logs", "--no-color", "node", "migrate")...)
	for _, name := range []string{"api-token", "database-password"} {
		need(t, !strings.Contains(logs, strings.TrimSpace(string(before[name]))), "secret leaked into runtime logs")
	}
	t.Log("PASS: offline init, byte-preserving repeat, conflict rejection and standard Compose database/migration/Mihomo startup")
}
