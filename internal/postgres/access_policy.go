package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"time"
)

func changeAccessPolicy(ctx context.Context, tx pgx.Tx, c node.UserChange) error {
	var prior, credentialRevision int64
	var expiresAt *time.Time
	if err := tx.QueryRow(ctx, "SELECT policy_revision,credential_revision,expires_at FROM node_users WHERE name=$1", c.Name).Scan(&prior, &credentialRevision, &expiresAt); err != nil {
		return node.ErrUnavailable
	}
	policy := c.AccessPolicy
	if policy.Revision == prior && c.Kind == "rotate" && credentialRevision <= c.ExpectedRevision && ((expiresAt == nil && policy.ExpiresAt == nil) || (expiresAt != nil && policy.ExpiresAt != nil && expiresAt.Equal(policy.ExpiresAt.Truncate(time.Microsecond)))) {
		return nil
	}
	if policy.Revision <= prior {
		return node.ErrConflict
	}
	if policy.Renew && policy.ExpiresAt != nil && !policy.ExpiresAt.After(time.Now()) {
		return node.ErrInvalid
	}
	clearHold, err := authorizeRecoveryRelease(ctx, tx, c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE node_users SET policy_revision=$2,expires_at=$3,renewal_revision=CASE WHEN $4 THEN $2 ELSE renewal_revision END,
 expiry_enforced_revision=CASE WHEN $4 THEN 0 WHEN expiry_enforced_revision>0 THEN $5 ELSE 0 END,
 released_recovery_hold_id=CASE WHEN $6 THEN recovery_hold_id ELSE released_recovery_hold_id END,recovery_release_policy_revision=CASE WHEN $6 THEN $2 ELSE recovery_release_policy_revision END,
 recovery_hold_id=CASE WHEN $6 THEN NULL ELSE recovery_hold_id END,recovery_hold_revision=CASE WHEN $6 THEN 0 ELSE recovery_hold_revision END WHERE name=$1`, c.Name, policy.Revision, policy.ExpiresAt, policy.Renew, c.ExpectedRevision+1, clearHold)
	if err != nil {
		return sanitized(err)
	}
	// A superseding policy/rotation which retains the latch needs its own durable
	// evidence, attributed to that policy and credential generation.
	_, err = tx.Exec(ctx, `INSERT INTO node_access_policy_events(revision,name,policy_revision,credential_revision,enforced_at)
 SELECT expiry_enforced_revision,name,policy_revision,credential_revision,$2 FROM node_users WHERE name=$1 AND expiry_enforced_revision>0`, c.Name, time.Now().UTC())
	return sanitized(err)
}

// The same singleton lock serializes intent, local expiry and coherent snapshots.
// Commit the latch and evidence before any credentials reach the runtime writer.
func (s *Store) ReconcileAccessPolicy(ctx context.Context, now time.Time, observations ...node.AccessExpiryObservation) (node.UserSnapshot, error) {
	if len(observations) > maxUsers {
		return node.UserSnapshot{}, node.ErrInvalid
	}
	if observations == nil {
		observations = []node.AccessExpiryObservation{}
	}
	for _, observation := range observations {
		if !namePattern.MatchString(observation.Name) || observation.RenewalRevision < 0 || observation.ObservedAt.IsZero() {
			return node.UserSnapshot{}, node.ErrInvalid
		}
	}
	raw, err := json.Marshal(observations)
	if err != nil {
		return node.UserSnapshot{}, node.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	revision, initialized, err := lockState(ctx, tx)
	if err != nil || !initialized {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	var due bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_users u WHERE NOT deleted AND expiry_enforced_revision=0 AND
 (expires_at <= $1 OR EXISTS(SELECT 1 FROM jsonb_to_recordset($2::jsonb) AS e(name text,renewal_revision bigint) WHERE e.name=u.name AND e.renewal_revision=u.renewal_revision)))`, now, raw).Scan(&due); err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	if due {
		if revision == int64(1<<63-1) {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
		revision++
		_, err = tx.Exec(ctx, `WITH expired AS (
   UPDATE node_users u SET expiry_enforced_revision=$2 WHERE NOT deleted AND expiry_enforced_revision=0 AND
   (expires_at <= $1 OR EXISTS(SELECT 1 FROM jsonb_to_recordset($3::jsonb) AS e(name text,renewal_revision bigint) WHERE e.name=u.name AND e.renewal_revision=u.renewal_revision))
   RETURNING name,policy_revision,credential_revision,renewal_revision
  ) INSERT INTO node_access_policy_events(revision,name,policy_revision,credential_revision,enforced_at)
  SELECT $2,name,policy_revision,credential_revision,COALESCE(
   (SELECT e.observed_at FROM jsonb_to_recordset($3::jsonb) AS e(name text,renewal_revision bigint,observed_at timestamptz)
    WHERE e.name=expired.name AND e.renewal_revision=expired.renewal_revision ORDER BY e.observed_at LIMIT 1),$1) FROM expired`, now, revision, raw)
		if err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
		if _, err = tx.Exec(ctx, "UPDATE node_user_state SET revision=$1 WHERE singleton", revision); err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
		if _, err = tx.Exec(ctx, "INSERT INTO node_user_operations(revision,state) VALUES($1,'queued')", revision); err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
	}
	snap, err := snapshot(ctx, tx, revision)
	if err != nil {
		return node.UserSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	return snap, nil
}

// Pages include an entire revision (at most maxUsers events), so after=revision
// cannot skip users which expired together. The revision itself is the cursor.
func (s *Store) AccessPolicyEvents(ctx context.Context, after int64) ([]node.AccessPolicyEvent, error) {
	if after < 0 {
		return nil, node.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT revision,name,policy_revision,credential_revision,enforced_at FROM node_access_policy_events
 WHERE revision=(SELECT min(revision) FROM node_access_policy_events WHERE revision>$1) ORDER BY name`, after)
	if err != nil {
		return nil, node.ErrUnavailable
	}
	defer rows.Close()
	events := []node.AccessPolicyEvent{}
	for rows.Next() {
		var e node.AccessPolicyEvent
		if rows.Scan(&e.Revision, &e.Name, &e.PolicyRevision, &e.CredentialRevision, &e.EnforcedAt) != nil {
			return nil, node.ErrUnavailable
		}
		events = append(events, e)
	}
	if rows.Err() != nil {
		return nil, node.ErrUnavailable
	}
	return events, nil
}

// Recovery is an explicit, fresh controller authorization, never an implication
// of an ordinary renewal that may have been queued before the restore.
func authorizeRecoveryRelease(ctx context.Context, tx pgx.Tx, c node.UserChange) (bool, error) {
	p := c.AccessPolicy
	if p.Recovery == nil {
		return false, nil
	}
	r := p.Recovery
	if c.Kind != "" || !p.Renew || !uuidPattern.MatchString(r.HoldID) || !r.ClockSynchronized || r.ControllerUTC.IsZero() || time.Since(r.ControllerUTC).Abs() > 120*time.Second {
		return false, node.ErrInvalid
	}
	var hold string
	var floor int64
	if err := tx.QueryRow(ctx, "SELECT COALESCE(recovery_hold_id::text,''),recovery_hold_revision FROM node_users WHERE name=$1", c.Name).Scan(&hold, &floor); err != nil {
		return false, node.ErrUnavailable
	}
	if hold == "" || hold != r.HoldID || p.Revision <= floor {
		return false, node.ErrConflict
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(r.ControllerUTC) {
		return false, node.ErrInvalid
	}
	return true, nil
}
