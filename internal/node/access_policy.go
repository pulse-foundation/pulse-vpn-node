package node

import (
	"context"
	"time"
)

// AccessPolicyChange is an explicitly versioned policy. Omission by a legacy
// caller preserves the deadline and latch. Only Renew on a newer policy clears it.
type AccessRecoveryRelease struct {
	HoldID            string    `json:"holdId"`
	ControllerUTC     time.Time `json:"controllerUtc"`
	ClockSynchronized bool      `json:"clockSynchronized"`
}

type AccessPolicyChange struct {
	Recovery  *AccessRecoveryRelease `json:"recovery,omitempty"`
	Revision  int64                  `json:"revision"`
	ExpiresAt *time.Time             `json:"expiresAt"`
	Renew     bool                   `json:"renew"`
}

type AccessPolicyEvent struct {
	Revision           int64     `json:"revision"`
	Name               string    `json:"name"`
	PolicyRevision     int64     `json:"policyRevision"`
	CredentialRevision int64     `json:"credentialRevision"`
	EnforcedAt         time.Time `json:"enforcedAt"`
}

// Policy evidence is retained in SQL independently of telemetry and user deletion.
// An observation is scoped to the last explicit renewal, not a moving wall-clock
// high-water mark. New renewals after clock correction must remain possible.
type AccessExpiryObservation struct {
	Name            string    `json:"name"`
	RenewalRevision int64     `json:"renewal_revision"`
	ObservedAt      time.Time `json:"observed_at"`
}

type AccessPolicyStore interface {
	ReconcileAccessPolicy(context.Context, time.Time, ...AccessExpiryObservation) (UserSnapshot, error)
	AccessPolicyEvents(context.Context, int64) ([]AccessPolicyEvent, error)
}

func EffectiveAccess(user ManagedUser, now time.Time) (bool, string) {
	if !user.Enabled {
		return false, "disabled"
	}
	if user.RecoveryHoldID != "" {
		return false, "recovery_hold"
	}
	if user.ExpiryEnforcedRevision > 0 || user.ExpiresAt != nil && !now.Before(*user.ExpiresAt) {
		return false, "expired"
	}
	return true, "enabled"
}

func (m *UserManager) AccessPolicySupported() bool { _, ok := m.store.(AccessPolicyStore); return ok }
func (m *UserManager) AccessPolicyEvents(ctx context.Context, after int64) ([]AccessPolicyEvent, error) {
	store, ok := m.store.(AccessPolicyStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return store.AccessPolicyEvents(ctx, after)
}
