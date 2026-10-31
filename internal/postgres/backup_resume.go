package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"time"
)

func recoveryRequestHash(backup DatabaseBackup, intent *RecoveryIntent) ([]byte, error) {
	var canonical *RecoveryIntent
	if intent != nil {
		copied := *intent
		copied.ControllerUTC = time.Time{}
		copied.ClockSynchronized = false
		copied.Users = append([]RecoveryUser(nil), intent.Users...)
		// A persisted Pulse projection of this restore can newly assert ForceHold on
		// retry. Resume separately proves every requested hold still exists on Node.
		for i := range copied.Users {
			copied.Users[i].ForceHold = false
		}
		canonical = &copied
	}
	raw, err := json.Marshal(struct {
		Backup DatabaseBackup
		Intent *RecoveryIntent
	}{backup, canonical})
	if err != nil {
		return nil, node.ErrInvalid
	}
	hash := sha256.Sum256(raw)
	return hash[:], nil
}
func resumeRecovery(ctx context.Context, tx pgx.Tx, revision int64, intent *RecoveryIntent, hash []byte) (RecoveryResult, error) {
	if intent == nil || intent.RequestID == "" {
		return RecoveryResult{}, node.ErrConflict
	}
	var requestID string
	var saved []byte
	if err := tx.QueryRow(ctx, "SELECT COALESCE(recovery_request_id::text,''),recovery_request_hash FROM node_user_state WHERE singleton").Scan(&requestID, &saved); err != nil {
		return RecoveryResult{}, node.ErrUnavailable
	}
	if requestID != intent.RequestID || string(saved) != string(hash) {
		return RecoveryResult{}, node.ErrConflict
	}
	state, err := snapshot(ctx, tx, revision)
	if err != nil {
		return RecoveryResult{}, err
	}
	for _, user := range intent.Users {
		if !user.ForceHold {
			continue
		}
		held := false
		for _, existing := range state.Users {
			if existing.Name == user.Name && existing.RecoveryHoldID != "" {
				held = true
			}
		}
		if !held {
			return RecoveryResult{}, node.ErrConflict
		}
	}
	return recoveryResult(state)
}
func recoveryResult(state node.UserSnapshot) (RecoveryResult, error) {
	if state.Recovery == nil {
		return RecoveryResult{}, node.ErrConflict
	}
	result := RecoveryResult{RequestID: state.Recovery.RequestID, Generation: state.Recovery.Generation, Revision: state.Recovery.RestoreRevision, Pending: state.Recovery.Pending, HeldUsers: []string{}}
	for _, u := range state.Users {
		if u.RecoveryHoldID != "" {
			result.HeldUsers = append(result.HeldUsers, u.Name)
		}
	}
	return result, nil
}
func (s *Store) RecoveryResult(ctx context.Context) (RecoveryResult, error) {
	state, err := s.Snapshot(ctx)
	if err != nil {
		return RecoveryResult{}, err
	}
	return recoveryResult(state)
}
