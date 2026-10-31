package node

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEffectiveAccess(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	for _, tc := range []struct {
		name    string
		user    ManagedUser
		enabled bool
		reason  string
	}{
		{"expires_while_pulse_offline", ManagedUser{Enabled: true, ExpiresAt: &past}, false, "expired"},
		{"clock_rollback_does_not_revive", ManagedUser{Enabled: true, ExpiresAt: &future, ExpiryEnforcedRevision: 3}, false, "expired"},
		{"manual_disable", ManagedUser{Enabled: false, ExpiresAt: &future}, false, "disabled"},
		{"unlimited", ManagedUser{Enabled: true}, true, "enabled"},
		{"deadline_boundary", ManagedUser{Enabled: true, ExpiresAt: &now}, false, "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled, reason := EffectiveAccess(tc.user, now)
			if enabled != tc.enabled || reason != tc.reason {
				t.Fatalf("got %v %s", enabled, reason)
			}
		})
	}
}

func TestExpiryDuringLocalStoreOutage(t *testing.T) {
	future := time.Now().Add(time.Hour)
	store := &memoryUsers{snapshot: UserSnapshot{Revision: 1, Users: []ManagedUser{{Name: "expiring", Enabled: true, ExpiresAt: &future}, {Name: "healthy", Enabled: true}}}}
	core := &userCore{}
	manager := NewUserManager(store, core)
	manager.step(context.Background())
	store.readErr = errors.New("offline")
	manager.now = func() time.Time { return future }
	manager.step(context.Background())
	if core.applies != 2 || len(core.revoked) != 1 || core.revoked[0] != "expiring" {
		t.Fatal("deadline extended during local database outage")
	}
	manager.step(context.Background())
	if core.applies != 2 || len(core.revoked) != 1 {
		t.Fatal("expiry cleanup not retained")
	}
}

type observingUsers struct{ memoryUsers }

func (s *observingUsers) AccessPolicyEvents(context.Context, int64) ([]AccessPolicyEvent, error) {
	return nil, nil
}
func (s *observingUsers) ReconcileAccessPolicy(_ context.Context, _ time.Time, observed ...AccessExpiryObservation) (UserSnapshot, error) {
	if s.readErr != nil {
		return UserSnapshot{}, s.readErr
	}
	for _, e := range observed {
		for i, u := range s.snapshot.Users {
			if e.Name == u.Name && e.RenewalRevision == u.RenewalRevision {
				s.snapshot.Revision++
				s.snapshot.Users[i].ExpiryEnforcedRevision = s.snapshot.Revision
			}
		}
	}
	return s.snapshot, nil
}

type capturedUsers struct {
	userCore
	users []User
}

func (c *capturedUsers) ApplyUsers(ctx context.Context, users []User) error {
	c.users = users
	return c.userCore.ApplyUsers(ctx, users)
}

func TestEmergencyExpiryCarriesRenewalEpochThroughClockRollback(t *testing.T) {
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	store := &observingUsers{memoryUsers: memoryUsers{snapshot: UserSnapshot{Revision: 1, Users: []ManagedUser{{Name: "alice", Enabled: true, ExpiresAt: &deadline}, {Name: "bob", Enabled: true}}}}}
	core := &capturedUsers{}
	manager := NewUserManager(store, core)
	manager.now = func() time.Time { return now }
	manager.step(context.Background())
	store.readErr = errors.New("SQL offline")
	manager.now = func() time.Time { return deadline }
	manager.step(context.Background())
	if len(core.users) != 1 || core.users[0].Name != "bob" {
		t.Fatal("emergency expiry did not remove target")
	}
	manager.now = func() time.Time { return now }
	manager.step(context.Background())
	store.readErr = nil
	manager.step(context.Background())
	store.snapshot.Revision++
	manager.step(context.Background())
	if len(core.users) != 1 || core.users[0].Name != "bob" || store.snapshot.Users[0].ExpiryEnforcedRevision == 0 {
		t.Fatal("rollback/recovery/unrelated revision revived access")
	}
	// Explicit newer renewal after wall-clock correction replaces the old epoch.
	next := now.Add(30 * time.Minute)
	store.snapshot.Users[0].ExpiresAt = &next
	store.snapshot.Users[0].ExpiryEnforcedRevision = 0
	store.snapshot.Users[0].RenewalRevision = 2
	store.snapshot.Revision++
	manager.step(context.Background())
	if len(core.users) != 2 {
		t.Fatal("explicit newer renewal poisoned by prior wall-clock observation")
	}
}
