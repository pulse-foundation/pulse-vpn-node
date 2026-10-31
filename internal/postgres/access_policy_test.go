package postgres

import (
	"context"
	"errors"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
	"testing"
	"time"
)

func TestDurableAccessExpiry(t *testing.T) {
	s, dsn := testStore(t)
	ctx := context.Background()
	_, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	_, err = s.Change(ctx, node.UserChange{Key: "policy", Name: "alice", Enabled: true, ExpectedRevision: 1, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.ReconcileAccessPolicy(ctx, deadline)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Revision != 3 || !snap.Users[0].Enabled || snap.Users[0].ExpiryEnforcedRevision != 3 || len(snap.Active()) != 0 {
		t.Fatal("expiry did not latch independently of desired enablement")
	}
	s.Close()
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err = s.ReconcileAccessPolicy(ctx, now)
	if err != nil || len(snap.Active()) != 0 {
		t.Fatal("restart or clock rollback revived access")
	}
	buffer, err := telemetry.NewBuffer()
	if err != nil {
		t.Fatal(err)
	}
	for range telemetry.MaxRecords + 1 {
		if err = buffer.Publish(telemetry.Sample{Kind: "runtime"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := buffer.Page("", 0, 1)
	if err != nil || page.FirstSequence <= 1 {
		t.Fatal("telemetry eviction fixture failed")
	}
	events, err := s.AccessPolicyEvents(ctx, 0)
	if err != nil || len(events) != 1 || events[0].PolicyRevision != 1 {
		t.Fatal("durable expiry evidence missing")
	}
	// Ordinary newer policy or legacy enable does not renew an expired identity.
	_, err = s.Change(ctx, node.UserChange{Key: "enable", Name: "alice", Enabled: true, ExpectedRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	future := now.Add(2 * time.Hour)
	_, err = s.Change(ctx, node.UserChange{Key: "renew-disabled", Name: "alice", Enabled: false, ExpectedRevision: 4, AccessPolicy: &node.AccessPolicyChange{Revision: 2, ExpiresAt: &future, Renew: true}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(ctx)
	if err != nil || snap.Users[0].ExpiryEnforcedRevision != 0 || snap.Users[0].Enabled || len(snap.Active()) != 0 {
		t.Fatal("explicit_renewal_preserves_manual_disable")
	}
	_, err = s.Change(ctx, node.UserChange{Key: "stale", Name: "alice", Enabled: true, ExpectedRevision: 5, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &future, Renew: true}})
	if !errors.Is(err, node.ErrConflict) {
		t.Fatal("stale renewal accepted")
	}
	events, err = s.AccessPolicyEvents(ctx, 0)
	if err != nil || len(events) != 1 {
		t.Fatal("renewal erased recovery evidence")
	}
}

func TestRotationRetainsExpiryAndMapsNewCredential(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(-time.Second)
	if _, err := s.Change(ctx, node.UserChange{Key: "finite", Name: "alice", Enabled: true, ExpectedRevision: 1, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}}); err != nil {
		t.Fatal(err)
	}
	// A restart's first reconciliation checks a deadline before returning any active user.
	snap, err := s.ReconcileAccessPolicy(ctx, time.Now())
	if err != nil || len(snap.Active()) != 0 {
		t.Fatal("restart_checks_deadline_before_listen")
	}
	policy := &node.AccessPolicyChange{Revision: 2, ExpiresAt: &deadline}
	rotation := node.UserChange{Key: "rotate", Kind: "rotate", Name: "alice", UUID: "00000000-0000-4000-8000-000000000001", ExpectedRevision: 3, AccessPolicy: policy}
	if _, err = s.Change(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(ctx)
	if err != nil || len(snap.Active()) != 0 || !snap.Users[0].Enabled || snap.Users[0].CredentialRevision != 4 || snap.Users[0].PolicyRevision != 2 {
		t.Fatal("rotation lost expiry/intent")
	}
	events, err := s.AccessPolicyEvents(ctx, 3)
	if err != nil || len(events) != 1 || events[0].PolicyRevision != 2 || events[0].CredentialRevision != 4 {
		t.Fatal("rotation lost expiry attribution for new credential")
	}
	rotation.Key = "retry-rotate"
	rotation.ExpectedRevision = 4
	if _, err = s.Change(ctx, rotation); err != nil {
		t.Fatal("identical rotation policy retry rejected")
	}
	rotation.Key = "different-credential"
	rotation.ExpectedRevision = 5
	rotation.UUID = "00000000-0000-4000-8000-000000000002"
	if _, err = s.Change(ctx, rotation); !errors.Is(err, node.ErrConflict) {
		t.Fatal("same policy reused for a different credential")
	}
}

func TestObservedExpirySurvivesClockRollbackAndAllowsRenewal(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	if _, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(ctx, node.UserChange{Key: "finite", Name: "alice", Enabled: true, ExpectedRevision: 1, AccessPolicy: &node.AccessPolicyChange{Revision: 1, ExpiresAt: &deadline}}); err != nil {
		t.Fatal(err)
	}
	// The runtime observed expiry while SQL was unavailable, then UTC rolled back.
	observed := node.AccessExpiryObservation{Name: "alice", RenewalRevision: 0, ObservedAt: deadline}
	snap, err := s.ReconcileAccessPolicy(ctx, now, observed)
	if err != nil || len(snap.Active()) != 0 || snap.Users[0].ExpiryEnforcedRevision == 0 {
		t.Fatal("lost emergency expiry after clock rollback")
	}
	if _, err = s.Change(ctx, node.UserChange{Key: "other", Name: "bob", UUID: "00000000-0000-4000-8000-000000000002", Enabled: true, ExpectedRevision: snap.Revision}); err != nil {
		t.Fatal(err)
	}
	snap, err = s.ReconcileAccessPolicy(ctx, now)
	if err != nil || len(snap.Active()) != 1 || snap.Active()[0].Name == "alice" {
		t.Fatal("unrelated mutation revived expired user")
	}
	correctedDeadline := now.Add(30 * time.Minute)
	if _, err = s.Change(ctx, node.UserChange{Key: "renew", Name: "alice", Enabled: true, ExpectedRevision: snap.Revision, AccessPolicy: &node.AccessPolicyChange{Revision: 2, ExpiresAt: &correctedDeadline, Renew: true}}); err != nil {
		t.Fatal(err)
	}
	snap, err = s.ReconcileAccessPolicy(ctx, now, observed)
	if err != nil || len(snap.Active()) != 2 {
		t.Fatal("old expiry observation poisoned an explicit newer renewal after clock correction")
	}
}
