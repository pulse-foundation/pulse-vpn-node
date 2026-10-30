//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const nodeImage = "pulse-vpn-node:integration-test"

var prebuilt = flag.Bool("images-prebuilt", false, "require existing node, helper and check images instead of building")
var cases = []struct{ name, project string }{
	{"Postgres", "pulse-vpn-node-storage-test"},
	{"Lifecycle", "pulse-vpn-node-smoke"},
	{"Reality", "pulse-vpn-node-reality-test"},
	{"Users", "pulse-vpn-node-users-test"},
	{"Traffic", "pulse-vpn-node-traffic-test"},
	{"Telemetry", "pulse-vpn-node-telemetry-test"},
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (8 << 20) - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

type result struct {
	stdout, stderr string
	err            error
}

// expectedFailure requires a completed nonzero exit, not a deadline, signal,
// launch error or truncated output that happens to contain the expected text.
func (r result) expectedFailure(message string) bool {
	var exit *exec.ExitError
	return errors.As(r.err, &exit) && exit.ExitCode() > 0 && strings.Contains(r.stderr, message)
}

type suite struct {
	root      string
	env       []string
	prepared  bool
	validated bool
	imageID   string
}
type lab struct {
	*suite
	project string
}

func need(t *testing.T, ok bool, message string) {
	t.Helper()
	if !ok {
		t.Fatal(message)
	}
}
func decode[T any](t *testing.T, raw string) T {
	t.Helper()
	var value T
	if json.Unmarshal([]byte(raw), &value) != nil {
		t.Fatal("invalid Docker/API JSON (content withheld)")
	}
	return value
}
func newSuite(t *testing.T) *suite {
	t.Helper()
	root, err := os.Getwd()
	need(t, err == nil, "cannot locate repository")
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		need(t, parent != root, "run tests from within the repository")
		root = parent
	}
	var env []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "PULSE_NODE_") && !strings.HasPrefix(item, "COMPOSE_") {
			env = append(env, item)
		}
	}
	return &suite{root: root, env: env}
}
func (s *suite) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = s.root, s.env, 5*time.Second
	return cmd
}
func (s *suite) run(timeout time.Duration, args ...string) result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := s.command(ctx, args...)
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if stdout.overflow || stderr.overflow {
		err = fmt.Errorf("command output limit exceeded")
	}
	return result{stdout.String(), stderr.String(), err}
}
func (s *suite) docker(t *testing.T, args ...string) string {
	t.Helper()
	r := s.run(3*time.Minute, args...)
	need(t, r.err == nil, "Docker command failed or timed out (output withheld)")
	return strings.TrimSpace(r.stdout)
}
func (l *lab) args(args ...string) []string {
	base := []string{"compose", "--env-file", "/dev/null", "-p", l.project, "-f", "compose.yaml", "-f", "tests/integration/compose.yaml"}
	return append(base, args...)
}
func (l *lab) compose(t *testing.T, args ...string) string {
	t.Helper()
	return l.docker(t, l.args(args...)...)
}
func (s *suite) clean(t *testing.T, project string) {
	t.Helper()
	for _, args := range [][]string{{"ps", "-aq"}, {"network", "ls", "-q"}, {"volume", "ls", "-q"}} {
		output := s.docker(t, append(args, "--filter", "label=com.docker.compose.project="+project)...)
		need(t, output == "", "test project has remaining resources: "+project)
	}
}
func (s *suite) image(t *testing.T) string {
	t.Helper()
	return s.docker(t, "image", "inspect", nodeImage, "--format", "{{.Id}}")
}
func (s *suite) prepare(t *testing.T) {
	t.Helper()
	if s.prepared {
		return
	}
	if !s.validated {
		s.checkCompose(t)
	}
	// Refuse stale projects before building or mutating any test resources.
	for _, c := range cases {
		s.clean(t, c.project)
	}
	if *prebuilt {
		s.docker(t, "image", "inspect", nodeImage, "pulse-vpn-node-reality-lab:local", "pulse-vpn-node:checks")
	} else {
		l := &lab{s, "pulse-vpn-node-storage-test"}
		r := s.run(20*time.Minute, l.args("--profile", "test", "--progress", "plain", "build", "node", "generate", "postgres-check")...)
		if r.err != nil {
			// Build contexts exclude operator secrets; runtime fixtures do not exist yet.
			// Keep runtime command output private, but retain bounded build diagnostics.
			t.Logf("image build stdout:\n%s\nimage build stderr:\n%s", r.stdout, r.stderr)
			t.Fatalf("image build/Go verification failed: %v", r.err)
		}
	}
	s.imageID, s.prepared = s.image(t), true
}
func (s *suite) start(t *testing.T, project string) *lab {
	t.Helper()
	s.prepare(t)
	s.clean(t, project)
	l := &lab{s, project}
	t.Cleanup(func() {
		// Cleanup has its own deadline: the test context may already be cancelled.
		r := s.run(3*time.Minute, l.args("--profile", "test", "down", "--volumes", "--remove-orphans")...)
		if r.err != nil {
			t.Error("test cleanup failed (output withheld)")
		}
		s.clean(t, project)
		need(t, s.image(t) == s.imageID, "runtime image changed during acceptance tests")
	})
	return l
}
func (l *lab) up(t *testing.T, services ...string) {
	t.Helper()
	l.compose(t, append([]string{"up", "-d", "--no-build", "--pull", "missing", "--wait", "--wait-timeout", "120"}, services...)...)
}
func (l *lab) restart(t *testing.T) {
	t.Helper()
	l.compose(t, "restart", "node")
	l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "node")
}
func (l *lab) coreIdentity(t *testing.T) string {
	t.Helper()
	value := l.compose(t, "exec", "-T", "node", "/bin/sh", "-c", `for p in /proc/[0-9]*; do if [ "$(cat "$p/comm" 2>/dev/null)" = mihomo ]; then awk '{print $1, $22}' "$p/stat"; fi; done`)
	need(t, len(strings.Split(value, "\n")) == 1 && len(strings.Fields(value)) == 2, "expected exactly one Mihomo PID/start time")
	return value
}

type container struct {
	State struct {
		Status    string
		ExitCode  int
		OOMKilled bool
		Health    struct{ Status string }
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

func (l *lab) inspect(t *testing.T, id string) container {
	t.Helper()
	values := decode[[]container](t, l.docker(t, "inspect", id))
	need(t, len(values) == 1, "expected one container")
	return values[0]
}
func (l *lab) targetIP(t *testing.T) string {
	t.Helper()
	id := l.compose(t, "ps", "-q", "target")
	ip := l.inspect(t, id).NetworkSettings.Networks[l.project+"_back"].IPAddress
	need(t, ip != "", "missing backend target address")
	return ip
}
func (l *lab) probe(t *testing.T, name string) {
	t.Helper()
	output := l.compose(t, "run", "--rm", "--no-deps", "-T", "-e", "REALITY_TEST_TARGET_IP="+l.targetIP(t), "probe", name)
	logProbe(t, output)
}
func logProbe(t *testing.T, output string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "PASS: ") {
			t.Log(line)
		}
	}
}
