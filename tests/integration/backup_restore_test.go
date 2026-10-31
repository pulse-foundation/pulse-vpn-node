//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func (l *lab) backupRestore(t *testing.T) {
	l.up(t, "target", "node", "client", "second")
	l.managedProbe(t, "backup-setup", false)
	raw := l.compose(t, "exec", "-T", "node", "pulse-vpn-node", "backup")
	var snapshot struct {
		Revision int64
		Users    []struct {
			Name, UUID     string
			PolicyRevision int64      `json:"policy_revision"`
			ExpiresAt      *time.Time `json:"expires_at"`
		}
	}
	need(t, json.Unmarshal([]byte(raw), &snapshot) == nil, "invalid backup (content withheld)")
	token := l.compose(t, "exec", "-T", "node", "cat", "/fixtures/api-token")
	need(t, !strings.Contains(raw, token), "snapshot included management credential")
	epoch := l.compose(t, "exec", "-T", "database", "psql", "-U", "pulse_test", "-d", "pulse_test", "-Atc", "SELECT accounting_epoch FROM node_traffic_state")
	time.Sleep(9 * time.Second) // Node enforces finite expiry without the controller.
	l.managedProbe(t, "expiry-persist", false)
	users := []map[string]any{}
	for _, u := range snapshot.Users {
		users = append(users, map[string]any{"name": u.Name, "uuid": u.UUID, "enabled": true, "policyRevision": u.PolicyRevision, "expiresAt": u.ExpiresAt})
	}
	restore := func(current []map[string]any) {
		l.compose(t, "stop", "node")
		// Only this owned disposable project's DB is reset. Client fixtures are kept
		// byte-for-byte, proving REALITY/UUID/SNI/Short ID survive the snapshot.
		l.compose(t, "exec", "-T", "database", "psql", "-U", "pulse_test", "-d", "pulse_test", "-v", "ON_ERROR_STOP=1", "-c", "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
		l.compose(t, "run", "--rm", "--no-deps", "migrate")
		payload, err := json.Marshal(map[string]any{"snapshot": json.RawMessage(raw), "currentIntent": map[string]any{"complete": true, "revisionFloor": snapshot.Revision, "controllerUtc": time.Now().UTC(), "clockSynchronized": true, "users": current}})
		need(t, err == nil, "restore input invalid")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := l.command(ctx, l.args("run", "--rm", "--no-deps", "-T", "node", "restore")...)
		cmd.Stdin = bytes.NewReader(payload)
		var out, stderr boundedOutput
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		need(t, cmd.Run() == nil && !out.overflow && !stderr.overflow, "restore command failed (content withheld)")
		l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "node")
	}
	restore(users)
	l.managedProbe(t, "backup-held", false)
	nextEpoch := l.compose(t, "exec", "-T", "database", "psql", "-U", "pulse_test", "-d", "pulse_test", "-Atc", "SELECT accounting_epoch FROM node_traffic_state")
	need(t, nextEpoch != epoch, "restore reused accounting epoch")
	l.restart(t)
	l.managedProbe(t, "backup-held", false)
	l.managedProbe(t, "backup-release", false)
	current := []map[string]any{}
	for _, u := range users {
		if u["name"] != "second-test" {
			current = append(current, u)
		}
	}
	restore(current)
	l.managedProbe(t, "backup-revoked", false)
	t.Log("original profile survives; offline-expired old snapshot held across restart; explicit post-hold renewal works; deletion survives second restore; fresh accounting epoch and no management credential in backup")
}
