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
		t.Run(c.name, func(t *testing.T) {
			l := s.start(t, c.project)
			switch c.name {
			case "Postgres":
				l.up(t, "database")
				l.compose(t, "run", "--rm", "--no-deps", "postgres-check")
			case "Lifecycle":
				l.lifecycle(t)
			case "Reality":
				l.reality(t)
			default:
				l.managed(t, c.name)
			}
		})
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
	status := decode[struct {
		Status        string
		VPNConfigured bool
		Core          struct{ Version string }
	}](t, l.compose(t, "exec", "-T", "node", "pulse-vpn-node", "status"))
	need(t, status.Status == "ready" && status.VPNConfigured && status.Core.Version == "v1.19.33", "unexpected authenticated status/core version")
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
	failed := l.run(30*time.Second, l.args("run", "--rm", "--no-deps", "--entrypoint", "/bin/sh", "node", "-c", `nc -l -p 8443 >/dev/null 2>&1 & while ! netstat -ltn | grep -q ":8443 "; do sleep 0.05; done; exec /usr/local/bin/pulse-vpn-node`)...)
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
