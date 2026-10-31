//go:build integration

package integration_test

import (
	"testing"
	"time"
)

func (l *lab) expiry(t *testing.T) {
	l.up(t, "target", "node", "client", "second")
	identity := l.coreIdentity(t)
	l.managedProbe(t, "expiry-live", false)
	need(t, l.coreIdentity(t) == identity, "expiry restarted shared core")
	l.restart(t)
	l.managedProbe(t, "expiry-persist", false)
	l.managedProbe(t, "expiry-before-stop", false)
	l.compose(t, "stop", "node")
	time.Sleep(6 * time.Second) // real deadline passes with no agent or shared core running
	l.restart(t)                // preserve generated credentials and the existing database
	l.managedProbe(t, "expiry-persist", false)
	t.Log("offline expiry closes only target TCP/UDP sessions, keeps other sessions/core, retains desired-enabled and durable evidence across restart")
}
