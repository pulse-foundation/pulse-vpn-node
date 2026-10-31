package node

import (
	"testing"
	"time"
)

func TestRecoveryHoldSurvivesClockCorrection(t *testing.T) {
	future := time.Now().Add(time.Hour)
	user := ManagedUser{Name: "alice", Enabled: true, ExpiresAt: &future, RecoveryHoldID: "11111111-1111-4111-8111-111111111111"}
	if enabled, reason := EffectiveAccess(user, time.Now().Add(-24*time.Hour)); enabled || reason != "recovery_hold" {
		t.Fatal("held credential became active after clock correction")
	}
	user.Enabled = false
	if enabled, reason := EffectiveAccess(user, time.Now()); enabled || reason != "disabled" {
		t.Fatal("manual disable must take precedence")
	}
}
