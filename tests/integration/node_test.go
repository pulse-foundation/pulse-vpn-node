//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"
)

func TestNode(t *testing.T) {
	s := newSuite(t)
	if !t.Run("Compose", s.checkCompose) {
		return
	}
	for _, c := range cases {
		ok := t.Run(c.name, func(t *testing.T) {
			if c.name == "Init" || c.name == "InitIdle" {
				s.initialize(t, c.project, c.name == "InitIdle")
				return
			}
			l := s.start(t, c.project)
			switch c.name {
			case "Postgres":
				l.up(t, "database")
				l.compose(t, "run", "--rm", "--no-deps", "postgres-check")
			case "Lifecycle":
				l.lifecycle(t)
			case "Expiry":
				l.expiry(t)
			case "ManagedRecovery":
				l.managedRecovery(t)
			case "ManagedLifecycle":
				l.managedLifecycle(t)
			case "Reality":
				l.reality(t)
			default:
				l.managed(t, c.name)
			}
		})
		if !ok && !s.prepared {
			return // Preparation is shared; retrying it in another scenario hides the first failure.
		}
	}
}
func (l *lab) exited(t *testing.T, id string) container {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		c := l.inspect(t, id)
		if c.State.Status == "exited" {
			return c
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("agent did not exit")
	return container{}
}
func (l *lab) lifecycle(t *testing.T) {
	l.up(t, "node")
	id := l.compose(t, "ps", "-q", "node")
	need(t, l.inspect(t, id).State.Health.Status == "healthy", "node is not healthy")
	token := l.compose(t, "exec", "-T", "node", "cat", "/fixtures/api-token")
	need(t, token != "", "missing fixture token")
	for _, header := range [][]string{nil, {"--header", "Authorization: Bearer wrong"}} {
		args := append([]string{"exec", "-T", "node", "wget", "-S", "-O", "/dev/null"}, header...)
		r := l.run(15*time.Second, l.args(append(args, "http://127.0.0.1:8080/api/v1/status")...)...)
		need(t, r.expectedFailure("401 Unauthorized"), "unauthorized API request was not rejected")
	}
	waitNodeConfigured(t, func() string {
		return l.compose(t, "exec", "-T", "node", "pulse-vpn-node", "status")
	}, true)
	l.compose(t, "exec", "-T", "node", "sh", "-c", `kill -KILL "$(pidof mihomo)"`)
	need(t, l.exited(t, id).State.ExitCode != 0, "core crash did not fail agent")
	l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "45", "node")
	failed := l.run(30*time.Second, l.args("run", "--rm", "--no-deps", "-e", "PULSE_NODE_MIHOMO_BINARY=/missing", "node")...)
	need(t, failed.expectedFailure("core configuration validation failed"), "invalid core binary did not fail startup")
	l.compose(t, "stop", "-t", "12", "node")
	state := l.exited(t, id).State
	need(t, state.ExitCode == 0 && !state.OOMKilled, "SIGTERM shutdown was not graceful")
	logs := l.compose(t, "logs", "--no-color", "node")
	need(t, !strings.Contains(logs, token), "API token leaked into logs")
	t.Log("authenticated readiness, rejected auth, crash/recovery, invalid binary, graceful shutdown and log redaction verified")
}
func (l *lab) reality(t *testing.T) {
	l.up(t, "target", "node", "client", "bad-uuid", "bad-key")
	for _, name := range []string{"probe", "reject-bad-uuid", "reject-bad-key"} {
		l.probe(t, name)
	}
	failed := l.run(30*time.Second, l.args("run", "--rm", "--no-deps", "-e", "PULSE_NODE_DATABASE_URL_FILE=", "--entrypoint", "/bin/sh", "node", "-c", `nc -l -p 8443 >/dev/null 2>&1 & while ! netstat -ltn | grep -q ":8443 "; do sleep 0.05; done; exec /usr/local/bin/pulse-vpn-node`)...)
	need(t, failed.expectedFailure("VLESS listen address is unavailable"), "occupied listener port did not fail startup")
	l.compose(t, "stop", "node")
	l.probe(t, "reject-stopped-node")
	t.Log("occupied-port rejection and stopped-node rejection verified")
}
func (l *lab) managed(t *testing.T, kind string) {
	probe := strings.ToLower(kind)
	if kind != "Users" {
		l.compose(t, "run", "--rm", "--no-deps", "-T", "--user", "0:0", "--cap-add", "CHOWN", "--entrypoint", "/bin/chown", "probe", "10001:10001", "/"+probe+"-baseline")
	}
	services := []string{"target", "node", "client"}
	if kind != "Telemetry" {
		services = append(services, "second")
	}
	l.up(t, services...)
	identity := l.coreIdentity(t)
	l.live(t, probe+"-live")
	need(t, l.coreIdentity(t) == identity, "Mihomo PID/start time changed during live checks")
	l.restart(t)
	l.probe(t, probe+"-persist")
	t.Log("unchanged core across live operations and persistent state after restart verified")
}

func (l *lab) managedLifecycle(t *testing.T) {
	l.up(t, "target", "database")
	l.compose(t, "run", "--rm", "--no-deps", "generate", "managed-idle")
	l.compose(t, "run", "--rm", "--no-deps", "migrate")
	l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "node")
	identity := l.coreIdentity(t)
	l.managedProbe(t, "managed-configure", true)
	need(t, l.coreIdentity(t) == identity, "creating connection restarted core")
	// Building the node caches Mihomo in BuildKit, not necessarily in the daemon
	// image store. Fresh CI runners must pull the pinned client image on first use.
	l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "missing", "--wait", "client", "second", "bad-uuid", "replacement")
	l.managedProbe(t, "managed-lifecycle", false)
	need(t, l.coreIdentity(t) == identity, "rotation or deletion restarted core")
	l.restart(t)
	l.managedProbe(t, "managed-persist", false)
	t.Log("PASS: idle installation, singleton live create, credential lifecycle, unchanged core and durable restart verified")
}

func (l *lab) managedProbe(t *testing.T, name string, writable bool) {
	t.Helper()
	args := []string{"run", "--rm", "--no-deps", "-T", "-e", "REALITY_TEST_TARGET_IP=" + l.targetIP(t)}
	service := "probe"
	if writable {
		service = "configure"
	}
	args = append(args, service, name)
	r := l.run(3*time.Minute, l.args(args...)...)
	logProbe(t, r.stdout)
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.HasPrefix(line, "FAIL: ") {
			t.Fatal(line)
		}
	}
	need(t, r.err == nil, "managed probe failed (diagnostics withheld)")
}

// Management readiness deliberately precedes verified VPN application in managed mode.
func waitNodeConfigured(t *testing.T, read func() string, configured bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status := decode[struct {
			Status        string
			VPNConfigured bool
			Core          struct{ Version string }
		}](t, read())
		if status.Status == "ready" && status.VPNConfigured == configured && status.Core.Version == "v1.19.33" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("node did not reach expected verified VPN state")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
