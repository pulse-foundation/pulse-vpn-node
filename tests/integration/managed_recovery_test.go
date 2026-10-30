//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"testing"
)

func (l *lab) managedRecovery(t *testing.T) {
	l.up(t, "target", "database")
	l.compose(t, "run", "--rm", "--no-deps", "generate", "managed-idle")
	l.compose(t, "run", "--rm", "--no-deps", "migrate")
	// Occupy the VPN port before every agent start, including container restart.
	// Only this disposable project's node gets the test entrypoint.
	override := filepath.Join(t.TempDir(), "occupied.yaml")
	content := `services:
  node:
    entrypoint: ["/bin/sh", "-c"]
    command:
      - 'nc -l -p 8443 >/dev/null 2>&1 & while ! netstat -ltn | grep -q ":8443 "; do sleep 0.05; done; exec /usr/local/bin/pulse-vpn-node'
`
	if err := os.WriteFile(override, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	l.extraCompose = override
	t.Cleanup(func() { l.extraCompose = "" })
	l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "30", "node")
	l.managedProbe(t, "managed-create-failure", true)
	l.compose(t, "restart", "node")
	// The saved but failed creation must not prevent management readiness.
	l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "30", "node")
	l.managedProbe(t, "managed-still-failed", false)
	identity := l.coreIdentity(t)
	l.compose(t, "exec", "-T", "node", "killall", "nc")
	l.managedProbe(t, "managed-recovered", false)
	need(t, l.coreIdentity(t) == identity, "recovery restarted core")
	t.Log("failed creation survives restart with API available; releasing port recovers the same revision without core restart")
}
