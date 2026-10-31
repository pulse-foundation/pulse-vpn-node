package postgres

import (
	"context"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"testing"
	"time"
)

func TestRestoreWithoutCurrentIntentDoesNotServe(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.InitializeManaged(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateConnection(ctx, connectionRequest(1, "connection")); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Change(ctx, node.UserChange{Key: "alice", Name: "alice", UUID: testUUID, Enabled: true, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	if _, err = target.RestoreBackup(ctx, backup, nil); err != nil {
		t.Fatal(err)
	}
	state, err := target.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Active()) != 0 || len(state.Users) != 1 || state.Users[0].RecoveryHoldID == "" {
		t.Fatal("restore served stale intent")
	}
	original, _ := source.Snapshot(ctx)
	if state.Connection.Reality.PrivateKey != original.Connection.Reality.PrivateKey || state.Users[0].UUID != testUUID {
		t.Fatal("restore changed VPN identity")
	}
}

func TestRestoredFinitePolicyRequiresPostHoldRecovery(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(time.Hour)
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Change(ctx, node.UserChange{Key: "finite", Name: "alice", Enabled: true, ExpectedRevision: 1, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	intent := &RecoveryIntent{Complete: true, ControllerUTC: time.Now().UTC(), ClockSynchronized: true, Users: []RecoveryUser{{Name: "alice", UUID: testUUID, Enabled: true, PolicyRevision: 1, CredentialRevision: 1, ExpiresAt: &deadline}}}
	if _, err = target.RestoreBackup(ctx, backup, intent); err != nil {
		t.Fatal(err)
	}
	state, err := target.Snapshot(ctx)
	if err != nil || len(state.Active()) != 0 || state.Users[0].RecoveryHoldID == "" {
		t.Fatal("uncertain offline expiry continuity was trusted")
	}
}

func TestRecoveryReleaseRequiresFreshClockAndExplicitNewRevision(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(time.Hour)
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Change(ctx, node.UserChange{Key: "finite", Name: "alice", Enabled: true, ExpectedRevision: 1, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	intent := &RecoveryIntent{Complete: true, ControllerUTC: time.Now().UTC(), ClockSynchronized: true, Users: []RecoveryUser{{Name: "alice", UUID: testUUID, Enabled: true, PolicyRevision: 1, ExpiresAt: &deadline}}}
	if _, err = target.RestoreBackup(ctx, backup, intent); err != nil {
		t.Fatal(err)
	}
	state, _ := target.Snapshot(ctx)
	hold := state.Users[0].RecoveryHoldID
	// A queued ordinary renewal cannot clear a recovery hold.
	if _, err = target.Change(ctx, node.UserChange{Key: "queued-renewal", Name: "alice", Enabled: true, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 2, ExpiresAt: &deadline, Renew: true}}); err != nil {
		t.Fatal(err)
	}
	state, _ = target.Snapshot(ctx)
	if len(state.Active()) != 0 {
		t.Fatal("old renewal cleared hold")
	}
	for _, clock := range []node.AccessRecoveryRelease{{HoldID: hold, ControllerUTC: time.Now(), ClockSynchronized: false}, {HoldID: hold, ControllerUTC: time.Now().Add(-121 * time.Second), ClockSynchronized: true}} {
		if _, err = target.Change(ctx, node.UserChange{Key: "bad-clock", Name: "alice", Enabled: true, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 3, ExpiresAt: &deadline, Renew: true, Recovery: &clock}}); err == nil {
			t.Fatal("untrusted recovery clock released finite access")
		}
	}
	release := &node.AccessRecoveryRelease{HoldID: hold, ControllerUTC: time.Now().UTC(), ClockSynchronized: true}
	if _, err = target.Change(ctx, node.UserChange{Key: "explicit-release", Name: "alice", Enabled: false, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 3, ExpiresAt: &deadline, Renew: true, Recovery: release}}); err != nil {
		t.Fatal(err)
	}
	state, _ = target.Snapshot(ctx)
	if state.Users[0].RecoveryHoldID != "" || len(state.Active()) != 0 {
		t.Fatal("explicit release ignored manual disable")
	}
}

func TestRevocationAfterBackupSurvivesRestore(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	if _, err = target.RestoreBackup(ctx, backup, &RecoveryIntent{Complete: true, Users: []RecoveryUser{}}); err != nil {
		t.Fatal(err)
	}
	state, _ := target.Snapshot(ctx)
	if len(state.Active()) != 0 || len(state.Users) != 0 {
		t.Fatal("deleted identity returned")
	}
	var revoked bool
	if err = target.pool.QueryRow(ctx, "SELECT revoked FROM node_user_credentials WHERE uuid=$1", testUUID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("retired credential resurrected")
	}
}

func TestForwardJumpThenDatabaseLossKeepsRecoveryHold(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(time.Hour)
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Change(ctx, node.UserChange{Key: "finite", Name: "alice", Enabled: true, ExpectedRevision: 1, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	latched, err := source.ReconcileAccessPolicy(ctx, deadline.Add(time.Second))
	if err != nil || latched.Users[0].ExpiryEnforcedRevision == 0 {
		t.Fatal("forward jump did not latch")
	}
	// Source state is lost before Pulse receives the event. Corrected UTC is before
	// expiry, but neither old backup nor latest intent can prove latch continuity.
	target, _ := testStore(t)
	result, err := target.RestoreBackup(ctx, backup, &RecoveryIntent{Complete: true, ControllerUTC: time.Now().UTC(), ClockSynchronized: true, Users: []RecoveryUser{{Name: "alice", UUID: testUUID, Enabled: true, PolicyRevision: 1, ExpiresAt: &deadline}}})
	if err != nil || len(result.HeldUsers) != 1 {
		t.Fatal("uncertain latch continuity ignored")
	}
	state, err := target.ReconcileAccessPolicy(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(state.Active()) != 0 {
		t.Fatal("corrected or rolled back clock released old profile")
	}
}

func TestInconsistentSnapshotRejected(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backup.Revision++
	target, _ := testStore(t)
	if _, err = target.RestoreBackup(ctx, backup, nil); err == nil {
		t.Fatal("inconsistent snapshot accepted")
	}
	var initialized bool
	if err = target.pool.QueryRow(ctx, "SELECT initialized FROM node_user_state WHERE singleton").Scan(&initialized); err != nil || initialized {
		t.Fatal("failed restore partially installed state")
	}
}

func TestBackupRequiresVerifiedSchema(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.pool.Exec(ctx, "UPDATE node_schema_migrations SET checksum=$1 WHERE version=6", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExportBackup(ctx); err == nil {
		t.Fatal("backup advertised incompatible schema")
	}
}

func TestRestoreRotationHoldAndAccountingEpoch(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sourceEpoch string
	if err = source.pool.QueryRow(ctx, "SELECT accounting_epoch::text FROM node_traffic_state").Scan(&sourceEpoch); err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	replacement := "55555555-5555-4555-8555-555555555555"
	result, err := target.RestoreBackup(ctx, backup, &RecoveryIntent{Complete: true, RevisionFloor: 10, Users: []RecoveryUser{{Name: "alice", UUID: replacement, Enabled: true, PolicyRevision: 2, ForceHold: true}}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := target.Snapshot(ctx)
	if err != nil || len(state.Active()) != 0 || len(result.HeldUsers) != 1 || state.Revision != 11 {
		t.Fatal("pending rotation recovery was not held")
	}
	var epoch string
	if err = target.pool.QueryRow(ctx, "SELECT accounting_epoch::text FROM node_traffic_state").Scan(&epoch); err != nil || epoch == sourceEpoch {
		t.Fatal("accounting epoch reused")
	}
	var revoked bool
	if err = target.pool.QueryRow(ctx, "SELECT revoked FROM node_user_credentials WHERE uuid=$1", testUUID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("old rotated UUID resurrected")
	}
}

func TestRestoreResumesAfterLostAcknowledgement(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	intent := &RecoveryIntent{RequestID: "44444444-4444-4444-8444-444444444444", Complete: true, ControllerUTC: time.Now().UTC(), Users: []RecoveryUser{{Name: "alice", UUID: testUUID, Enabled: true}}}
	first, err := target.RestoreBackup(ctx, backup, intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.ControllerUTC = time.Now().UTC()
	again, err := target.RestoreBackup(ctx, backup, intent)
	if err != nil || again.Generation != first.Generation || again.Revision != first.Revision {
		t.Fatal("lost restore acknowledgement cannot resume")
	}
	intent.Users[0].Enabled = false
	if _, err = target.RestoreBackup(ctx, backup, intent); err == nil {
		t.Fatal("changed intent reused committed restore")
	}
}

func TestRecoveryReleaseReceiptSurvivesLostAcknowledgement(t *testing.T) {
	source, _ := testStore(t)
	ctx := context.Background()
	if _, err := source.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	restored, err := target.RestoreBackup(ctx, backup, &RecoveryIntent{Complete: true, Users: []RecoveryUser{{Name: "alice", UUID: testUUID, Enabled: false, PolicyRevision: 1, ForceHold: true}}})
	if err != nil {
		t.Fatal(err)
	}
	release := &node.AccessRecoveryRelease{HoldID: restored.Generation, ControllerUTC: time.Now().UTC(), ClockSynchronized: true}
	if _, err = target.Change(ctx, node.UserChange{Key: "release-ack-lost", Name: "alice", Enabled: false, ExpectedRevision: restored.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 2, Renew: true, Recovery: release}}); err != nil {
		t.Fatal(err)
	}
	state, err := target.Snapshot(ctx)
	if err != nil || state.Users[0].RecoveryHoldID != "" || state.Users[0].ReleasedRecoveryHoldID != restored.Generation || state.Users[0].RecoveryReleasePolicyRevision != 2 || len(state.Active()) != 0 {
		t.Fatal("durable release receipt or manual disable lost")
	}
	if _, err = target.Change(ctx, node.UserChange{Key: "later-policy", Name: "alice", Enabled: false, ExpectedRevision: state.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 3}}); err != nil {
		t.Fatal(err)
	}
	state, err = target.Snapshot(ctx)
	if err != nil || state.Users[0].ReleasedRecoveryHoldID != restored.Generation || state.Users[0].RecoveryReleasePolicyRevision != 2 {
		t.Fatal("ordinary policy erased release receipt")
	}
}
