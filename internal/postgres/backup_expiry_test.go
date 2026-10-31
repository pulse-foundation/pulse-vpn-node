package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func TestRestorePreservesExpiryAfterDeadlineRemovedWithoutRenewal(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(-time.Hour)
	state, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Change(ctx, node.UserChange{Key: "finite", Name: "alice", Enabled: true, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = source.ReconcileAccessPolicy(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Change(ctx, node.UserChange{Key: "unlimited-without-renewal", Name: "alice", Enabled: true, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 2}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Active()) != 0 || state.Users[0].ExpiryEnforcedRevision == 0 || state.Users[0].ExpiresAt != nil {
		t.Fatal("source latch not preserved")
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	intent := &RecoveryIntent{RequestID: "44444444-4444-4444-8444-444444444444", Complete: true, ControllerUTC: time.Now().UTC(), ClockSynchronized: true, Users: []RecoveryUser{{Name: "alice", UUID: testUUID, Enabled: true, PolicyRevision: 2}}}
	restored, err := target.RestoreBackup(ctx, backup, intent)
	if err != nil {
		t.Fatal(err)
	}
	state, err = target.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Active()) != 0 || state.Users[0].ExpiryEnforcedRevision == 0 || state.Users[0].RecoveryHoldID == "" {
		t.Fatal("restore cleared source expiry evidence")
	}
	retry, err := target.RestoreBackup(ctx, backup, intent)
	if err != nil || retry.Generation != restored.Generation {
		t.Fatal("held restore retry changed generation")
	}
	_, err = target.Change(ctx, node.UserChange{Key: "ordinary-renewal", Name: "alice", Enabled: true, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 3, Renew: true}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = target.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Active()) != 0 {
		t.Fatal("ordinary renewal bypassed recovery hold")
	}
	release := &node.AccessRecoveryRelease{HoldID: restored.Generation, ControllerUTC: time.Now().UTC(), ClockSynchronized: true}
	_, err = target.Change(ctx, node.UserChange{Key: "explicit-recovery", Name: "alice", Enabled: true, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 4, Renew: true, Recovery: release}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = target.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Active()) != 1 || state.Users[0].UUID != testUUID {
		t.Fatal("explicit recovery did not restore original access")
	}
}
