//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"
)

func (l *lab) privateNetworks(t *testing.T) {
	l.up(t, "node", "client", "second")
	privateID := l.compose(t, "ps", "-q", "private-target")
	privateIP := l.inspect(t, privateID).NetworkSettings.Networks[l.project+"_back"].IPAddress
	need(t, privateIP != "", "missing private test target")
	// Prove the denied service is live and reachable from the node's namespace.
	l.compose(t, "exec", "-T", "node", "wget", "-q", "-O", "/dev/null", "http://private-target:8081/health")
	check := func() {
		result := l.run(time.Minute, l.args("run", "--rm", "--no-deps", "-T",
			"-e", "REALITY_TEST_TARGET_IP="+l.targetIP(t),
			"-e", "REALITY_TEST_PRIVATE_IP="+privateIP, "probe", "private-networks")...)
		logProbe(t, result.stdout)
		for _, line := range strings.Split(result.stdout, "\n") {
			if strings.HasPrefix(line, "FAIL: ") {
				t.Fatal(line)
			}
		}
		need(t, result.err == nil, "private network probe failed (diagnostics withheld)")
	}
	check()
	identity := l.coreIdentity(t)
	// Existing live-user test exercises add/disable/re-enable while another
	// user's TCP and UDP sessions remain open. The same renderer must keep denies.
	l.live(t, "users-live")
	need(t, l.coreIdentity(t) == identity, "policy/user reload restarted the core")
	check()
	l.restart(t)
	check()
	t.Log("private destinations rejected before/after user reload and restart")
}
