//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type composeModel struct {
	Services map[string]composeService
	Secrets  map[string]any
	Networks map[string]struct{ Internal bool }
}
type composeService struct {
	Image       string
	Build       any
	Restart     string
	Environment map[string]string
	ReadOnly    bool     `json:"read_only"`
	CapDrop     []string `json:"cap_drop"`
	CapAdd      []string `json:"cap_add"`
	SecurityOpt []string `json:"security_opt"`
	Privileged  bool
	NetworkMode string `json:"network_mode"`
	Networks    map[string]any
	Secrets     []any
	DependsOn   map[string]struct{ Condition string } `json:"depends_on"`
	Ports       []struct {
		HostIP    string `json:"host_ip"`
		Target    int
		Published string
	}
	Volumes []struct{ Type, Target string }
}

func has(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func (s *suite) config(t *testing.T, overlay string, env ...string) composeModel {
	t.Helper()
	args := []string{"compose", "--env-file", "/dev/null", "--profile", "test", "-f", "compose.yaml"}
	if overlay != "" {
		args = append(args, "-f", overlay)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := s.command(ctx, append(args, "config", "--format", "json")...)
	cmd.Env = append(append([]string{}, s.env...), env...)
	// Only Compose's config model is captured, not the contents of secret files.
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	need(t, cmd.Run() == nil && !stdout.overflow && !stderr.overflow, "Compose resolution failed (output withheld)")
	return decode[composeModel](t, stdout.String())
}
func checkNode(t *testing.T, m composeModel) {
	t.Helper()
	for _, name := range []string{"node", "migrate", "database"} {
		_, ok := m.Services[name]
		need(t, ok, "missing service: "+name)
	}
	node, migrate, db := m.Services["node"], m.Services["migrate"], m.Services["database"]
	need(t, node.Image != "" && node.Image == migrate.Image, "node/migration image mismatch")
	need(t, len(db.Ports) == 0, "database must stay private")
	need(t, node.DependsOn["migrate"].Condition == "service_completed_successfully", "node must wait for migration")
	need(t, migrate.DependsOn["database"].Condition == "service_healthy", "migration must wait for DB health")
	for _, svc := range []composeService{node, migrate} {
		need(t, svc.ReadOnly && has(svc.CapDrop, "ALL") && !svc.Privileged && len(svc.CapAdd) == 0, "runtime hardening lost")
		need(t, has(svc.SecurityOpt, "no-new-privileges:true"), "missing no-new-privileges")
	}
	for _, key := range []string{"TOKEN", "VLESS", "DATABASE_URL", "TELEMETRY"} {
		need(t, node.Environment["PULSE_NODE_"+key+"_FILE"] != "", "missing node config file setting")
	}
	persistent := false
	for _, v := range db.Volumes {
		if v.Type == "volume" && v.Target == "/var/lib/postgresql/data" {
			persistent = true
		}
	}
	need(t, persistent, "database volume missing")
	for _, p := range node.Ports {
		need(t, p.Target != 19090, "core controller must stay private")
	}
}
func (s *suite) checkCompose(t *testing.T) {
	base := s.config(t, "")
	checkNode(t, base)
	need(t, len(base.Services) == 3, "main Compose must contain only node, database and migration")
	need(t, base.Services["node"].Restart == "unless-stopped" && base.Services["migrate"].Restart == "no", "incorrect runtime/migration restart policy")
	need(t, base.Services["node"].Build == nil, "main Compose must use a prepared image")
	dev := s.config(t, "compose.dev.yaml")
	checkNode(t, dev)
	ports := dev.Services["node"].Ports
	need(t, len(ports) == 2, "dev ports must replace main bindings")
	published := map[string]bool{}
	for _, p := range ports {
		need(t, p.HostIP == "127.0.0.1", "dev must bind loopback")
		published[p.Published] = true
	}
	need(t, published["18080"] && published["18443"], "incorrect default dev ports")
	need(t, dev.Services["node"].Restart == "no", "dev must not restart failures")
	lab := s.config(t, "tests/integration/compose.yaml", "PULSE_NODE_VPN_BIND=0.0.0.0", "PULSE_NODE_VPN_PORT=443", "PULSE_NODE_IMAGE=unrelated:do-not-use", "PULSE_NODE_CONFIG_DIR=/does-not-exist/private")
	checkNode(t, lab)
	need(t, len(lab.Secrets) == 0, "lab must not use developer secrets")
	need(t, len(lab.Networks) == 2, "expected two isolated lab networks")
	for _, n := range lab.Networks {
		need(t, n.Internal, "lab must use internal networks")
	}
	for _, svc := range lab.Services {
		need(t, len(svc.Ports) == 0 && len(svc.Secrets) == 0, "lab exposes ports or developer secrets")
		need(t, svc.NetworkMode == "" || svc.NetworkMode == "none", "unexpected lab network mode")
	}
	for name, want := range map[string]string{"target": "back", "client": "default", "second": "default", "probe": "default", "bad-key": "default", "bad-uuid": "default", "replacement": "default", "configure": "default"} {
		svc, ok := lab.Services[name]
		need(t, ok, "missing lab service: "+name)
		_, ok = svc.Networks[want]
		need(t, ok && len(svc.Networks) == 1, "test peer isolation lost: "+name)
	}
	need(t, lab.Services["node"].Image == nodeImage, "operator image override reached the lab")
	// Ensure no project-wide environment variable was retained by the coordinator.
	for _, env := range s.env {
		need(t, !strings.HasPrefix(env, "PULSE_NODE_") && !strings.HasPrefix(env, "COMPOSE_"), "operator environment leaked")
	}
	s.validated = true
}

// Run the actual test binary against an isolated, deliberately unsafe Compose
// checkout. No Dockerfile or credentials are copied, so even a regressed guard
// cannot build/start a node from this directory.
func TestUnsafeComposeStopsRuntime(t *testing.T) {
	s := newSuite(t)
	root := t.TempDir()
	for _, name := range []string{"go.mod", "compose.yaml", "compose.dev.yaml", "tests/integration/compose.yaml"} {
		data, err := os.ReadFile(filepath.Join(s.root, name))
		need(t, err == nil, "cannot read Compose test source")
		if name == "tests/integration/compose.yaml" {
			data = bytes.Replace(data, []byte("ports: !reset []"), []byte(`ports: ["0.0.0.0:55555:8443"]`), 1)
		}
		path := filepath.Join(root, name)
		need(t, os.MkdirAll(filepath.Dir(path), 0700) == nil, "cannot create isolated checkout")
		need(t, os.WriteFile(path, data, 0600) == nil, "cannot write isolated Compose")
	}
	binary, err := os.Executable()
	need(t, err == nil, "cannot locate test binary")
	for _, filter := range []string{"^TestNode$", "^TestNode/Lifecycle$"} {
		t.Run(filter, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run", filter, "-test.v", "-test.timeout=25s")
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			need(t, err != nil && ctx.Err() == nil, "unsafe Compose must fail promptly")
			need(t, bytes.Contains(output, []byte("lab exposes ports or developer secrets")), "unsafe Compose was not rejected before preparation")
			need(t, !bytes.Contains(output, []byte("=== RUN   TestNode/Postgres")), "failed isolation check did not stop runtime scenarios")
		})
	}
}
