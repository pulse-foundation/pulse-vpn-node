package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"time"
)

const BackupSchemaVersion = 6
const maxBackupSize = 64 << 20

// DatabaseBackup is a secret-bearing logical snapshot, never an HTTP/log DTO.
// Connection secrets, retired credentials, audit history and totals are included.
// Live collector checkpoints are intentionally not restored across installations.
type DatabaseBackup struct {
	FormatVersion int             `json:"formatVersion"`
	SchemaVersion int             `json:"schemaVersion"`
	Revision      int64           `json:"revision"`
	State         json.RawMessage `json:"state"`
	Users         json.RawMessage `json:"users"`
	Credentials   json.RawMessage `json:"credentials"`
	Operations    json.RawMessage `json:"operations"`
	PolicyEvents  json.RawMessage `json:"policyEvents"`
	TrafficTotals json.RawMessage `json:"trafficTotals"`
}

type RecoveryUser struct {
	Name               string     `json:"name"`
	UUID               string     `json:"uuid"`
	Enabled            bool       `json:"enabled"`
	PolicyRevision     int64      `json:"policyRevision"`
	CredentialRevision int64      `json:"credentialRevision"`
	ExpiresAt          *time.Time `json:"expiresAt"`
	ExpiryEnforced     bool       `json:"expiryEnforced"`
	ForceHold          bool       `json:"forceHold"`
}
type RecoveryIntent struct {
	RequestID         string         `json:"requestId,omitempty"`
	Complete          bool           `json:"complete"`
	RevisionFloor     int64          `json:"revisionFloor"`
	ControllerUTC     time.Time      `json:"controllerUtc"`
	ClockSynchronized bool           `json:"clockSynchronized"`
	Users             []RecoveryUser `json:"users"`
}
type RecoveryResult struct {
	RequestID  string   `json:"requestId,omitempty"`
	Generation string   `json:"generation"`
	Revision   int64    `json:"revision"`
	Pending    bool     `json:"pending"`
	HeldUsers  []string `json:"heldUsers"`
}

func (s *Store) ExportBackup(ctx context.Context) (DatabaseBackup, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return DatabaseBackup{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	revision, initialized, err := lockState(ctx, tx)
	if err != nil || !initialized {
		return DatabaseBackup{}, node.ErrUnavailable
	}
	if err = verifyBackupSchema(ctx, tx); err != nil {
		return DatabaseBackup{}, err
	}
	result := DatabaseBackup{FormatVersion: 1, SchemaVersion: BackupSchemaVersion, Revision: revision}
	for _, item := range []struct {
		query       string
		destination *json.RawMessage
	}{
		{"SELECT to_jsonb(s) FROM node_user_state s WHERE singleton", &result.State},
		{"SELECT COALESCE(jsonb_agg(u ORDER BY name),'[]'::jsonb) FROM node_users u", &result.Users},
		{"SELECT COALESCE(jsonb_agg(c ORDER BY uuid),'[]'::jsonb) FROM node_user_credentials c", &result.Credentials},
		{"SELECT COALESCE(jsonb_agg(o ORDER BY revision),'[]'::jsonb) FROM node_user_operations o", &result.Operations},
		{"SELECT COALESCE(jsonb_agg(e ORDER BY revision,name),'[]'::jsonb) FROM node_access_policy_events e", &result.PolicyEvents},
		{"SELECT COALESCE(jsonb_agg(t ORDER BY user_name),'[]'::jsonb) FROM node_traffic_totals t", &result.TrafficTotals},
	} {
		if err = tx.QueryRow(ctx, item.query).Scan(item.destination); err != nil {
			return DatabaseBackup{}, node.ErrUnavailable
		}
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > maxBackupSize {
		return DatabaseBackup{}, node.ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return DatabaseBackup{}, node.ErrUnavailable
	}
	return result, nil
}

func recoveryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", node.ErrUnavailable
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// RestoreBackup requires a fresh database. It never overwrites a serving store.
// Without complete current intent every restored identity stays durably held.
func (s *Store) RestoreBackup(ctx context.Context, b DatabaseBackup, intent *RecoveryIntent) (RecoveryResult, error) {
	if b.FormatVersion != 1 || b.SchemaVersion != BackupSchemaVersion || b.Revision < 1 || b.Revision >= int64(1<<63-1) {
		return RecoveryResult{}, node.ErrInvalid
	}
	raw, err := json.Marshal(b)
	if err != nil || len(raw) > maxBackupSize {
		return RecoveryResult{}, node.ErrInvalid
	}
	if err = validateRecoveryIntent(intent); err != nil {
		return RecoveryResult{}, err
	}
	id, err := recoveryID()
	if err != nil {
		return RecoveryResult{}, err
	}
	revision := b.Revision
	if intent != nil && intent.RevisionFloor > revision {
		revision = intent.RevisionFloor
	}
	revision++
	requestHash, err := recoveryRequestHash(b, intent)
	if err != nil {
		return RecoveryResult{}, err
	}
	var requestID any
	if intent != nil && intent.RequestID != "" {
		requestID = intent.RequestID
	}
	result := RecoveryResult{Generation: id, Revision: revision, Pending: intent == nil || !intent.Complete, HeldUsers: []string{}}
	if intent != nil {
		result.RequestID = intent.RequestID
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return result, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	currentRevision, initialized, err := lockState(ctx, tx)
	if err != nil {
		return result, node.ErrUnavailable
	}
	if err = verifyBackupSchema(ctx, tx); err != nil {
		return result, err
	}
	if initialized {
		return resumeRecovery(ctx, tx, currentRevision, intent, requestHash)
	}
	var state struct {
		Revision              int64           `json:"revision"`
		Initialized           bool            `json:"initialized"`
		ConnectionInitialized bool            `json:"connection_initialized"`
		ConnectionSettings    json.RawMessage `json:"connection_settings"`
	}
	if json.Unmarshal(b.State, &state) != nil || !state.Initialized || state.Revision != b.Revision {
		return result, node.ErrInvalid
	}
	var connection any
	if string(state.ConnectionSettings) != "null" && len(state.ConnectionSettings) > 0 {
		connection = []byte(state.ConnectionSettings)
	}
	if _, err = tx.Exec(ctx, `UPDATE node_user_state SET initialized=true,revision=$1,applied_revision=0,connection_initialized=$2,connection_settings=$3,recovery_generation=$4,recovery_pending=$5,recovery_request_id=$6,recovery_request_hash=$7,recovery_restore_revision=$1 WHERE singleton`, revision, state.ConnectionInitialized, connection, id, result.Pending, requestID, requestHash); err != nil {
		return result, sanitized(err)
	}
	for _, item := range []struct {
		query string
		data  json.RawMessage
	}{
		{"INSERT INTO node_users SELECT * FROM jsonb_populate_recordset(NULL::node_users,$1)", b.Users},
		{"INSERT INTO node_user_credentials SELECT * FROM jsonb_populate_recordset(NULL::node_user_credentials,$1)", b.Credentials},
		{"INSERT INTO node_user_operations SELECT * FROM jsonb_populate_recordset(NULL::node_user_operations,$1)", b.Operations},
		{"INSERT INTO node_access_policy_events SELECT * FROM jsonb_populate_recordset(NULL::node_access_policy_events,$1)", b.PolicyEvents},
		{"INSERT INTO node_traffic_totals SELECT * FROM jsonb_populate_recordset(NULL::node_traffic_totals,$1)", b.TrafficTotals},
	} {
		if _, err = tx.Exec(ctx, item.query, []byte(item.data)); err != nil {
			return result, sanitized(err)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE node_users SET recovery_hold_revision=CASE WHEN expires_at IS NOT NULL OR recovery_hold_id IS NOT NULL THEN GREATEST(policy_revision,1) ELSE 0 END,recovery_hold_id=$1`, id); err != nil {
		return result, sanitized(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE node_user_operations SET state='superseded',error='' WHERE state<>'applied'"); err != nil {
		return result, node.ErrUnavailable
	}
	if !result.Pending {
		if err = reconcileRecoveryUsers(ctx, tx, intent, id, revision); err != nil {
			return result, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE node_traffic_state SET accounting_epoch=gen_random_uuid(),accounting_sequence=0,generation='',sequence=0,observed_at=NULL,pruned_before=NULL,reset_count=reset_count+1; DELETE FROM node_traffic_checkpoints`); err != nil {
		return result, node.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, "INSERT INTO node_user_operations(revision,state) VALUES($1,'queued')", revision); err != nil {
		return result, sanitized(err)
	}
	snap, err := snapshot(ctx, tx, revision)
	if err != nil {
		return result, err
	}
	for _, u := range snap.Users {
		if u.RecoveryHoldID != "" {
			result.HeldUsers = append(result.HeldUsers, u.Name)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return result, node.ErrUnavailable
	}
	return result, nil
}

func validateRecoveryIntent(intent *RecoveryIntent) error {
	if intent == nil {
		return nil
	}
	if intent.RequestID != "" && !validUUID(intent.RequestID) {
		return node.ErrInvalid
	}
	if intent.RevisionFloor < 0 || intent.RevisionFloor >= int64(1<<63-1) || len(intent.Users) > maxUsers {
		return node.ErrInvalid
	}
	seen := map[string]bool{}
	uuids := map[string]bool{}
	for _, u := range intent.Users {
		if !namePattern.MatchString(u.Name) || !uuidPattern.MatchString(u.UUID) || u.UUID == "00000000-0000-0000-0000-000000000000" || u.PolicyRevision < 0 || (u.ExpiresAt != nil && u.PolicyRevision < 1) || seen[u.Name] || uuids[u.UUID] {
			return node.ErrInvalid
		}
		seen[u.Name] = true
		uuids[u.UUID] = true
	}
	return nil
}

func reconcileRecoveryUsers(ctx context.Context, tx pgx.Tx, intent *RecoveryIntent, id string, revision int64) error {
	// A corrected trusted clock cannot prove continuity of an offline expiry latch.
	// All accesses finite in either snapshot or current intent remain held.
	if _, err := tx.Exec(ctx, "UPDATE node_users SET deleted=true,enabled=false"); err != nil {
		return node.ErrUnavailable
	}
	trusted := intent.ClockSynchronized && !intent.ControllerUTC.IsZero() && time.Since(intent.ControllerUTC).Abs() <= 120*time.Second
	for _, u := range intent.Users {
		var wasFinite bool
		var oldPolicy int64
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM node_users WHERE name=$1 AND (expires_at IS NOT NULL OR recovery_hold_revision>0)),COALESCE((SELECT policy_revision FROM node_users WHERE name=$1),0)", u.Name).Scan(&wasFinite, &oldPolicy); err != nil {
			return node.ErrUnavailable
		}
		if u.PolicyRevision < oldPolicy {
			return node.ErrConflict
		}
		hold := wasFinite || u.ExpiresAt != nil || u.ForceHold
		var holdID any
		if hold {
			holdID = id
		}
		expiryRevision := int64(0)
		if u.ExpiryEnforced || (trusted && u.ExpiresAt != nil && !intent.ControllerUTC.Before(*u.ExpiresAt)) {
			expiryRevision = revision
		}
		if _, err := tx.Exec(ctx, `INSERT INTO node_users(name,uuid,enabled,deleted,credential_revision,policy_revision,expires_at,expiry_enforced_revision,renewal_revision,recovery_hold_id,recovery_hold_revision)
 VALUES($1,$2,$3,false,$4,$5,$6,$7,0,$8,$5) ON CONFLICT(name) DO UPDATE SET uuid=EXCLUDED.uuid,enabled=EXCLUDED.enabled,deleted=false,credential_revision=EXCLUDED.credential_revision,policy_revision=EXCLUDED.policy_revision,expires_at=EXCLUDED.expires_at,expiry_enforced_revision=EXCLUDED.expiry_enforced_revision,recovery_hold_id=EXCLUDED.recovery_hold_id,recovery_hold_revision=EXCLUDED.recovery_hold_revision`, u.Name, u.UUID, u.Enabled, revision, u.PolicyRevision, u.ExpiresAt, expiryRevision, holdID); err != nil {
			return sanitized(err)
		}
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return node.ErrUnavailable
		}
		// A credential already retired in the snapshot cannot be restored as current.
		var owner string
		var existedRevoked bool
		err := tx.QueryRow(ctx, "SELECT user_name,revoked FROM node_user_credentials WHERE uuid=$1", u.UUID).Scan(&owner, &existedRevoked)
		if err != nil && err != pgx.ErrNoRows {
			return node.ErrUnavailable
		}
		if err == nil && (owner != u.Name || existedRevoked) {
			return node.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO node_user_credentials(uuid,user_name,runtime_name,revoked) VALUES($1,$2,$3,false) ON CONFLICT(uuid) DO UPDATE SET revoked=false WHERE node_user_credentials.user_name=EXCLUDED.user_name`, u.UUID, u.Name, "c_"+hex.EncodeToString(entropy[:])); err != nil {
			return sanitized(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO node_access_policy_events(revision,name,policy_revision,credential_revision,enforced_at) SELECT expiry_enforced_revision,name,policy_revision,credential_revision,$2 FROM node_users WHERE NOT deleted AND expiry_enforced_revision=$1`, revision, time.Now().UTC()); err != nil {
		return sanitized(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE node_user_credentials c SET revoked=true WHERE NOT EXISTS(SELECT 1 FROM node_users u WHERE NOT u.deleted AND u.uuid=c.uuid AND u.name=c.user_name)`); err != nil {
		return node.ErrUnavailable
	}

	return nil
}

// A backup advertises the schema only after every embedded migration is verified.
// Restore performs the same check without applying or repairing migrations.
func verifyBackupSchema(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, "SELECT version,checksum FROM node_schema_migrations ORDER BY version")
	if err != nil {
		return node.ErrUnavailable
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var version int
		var checksum []byte
		if rows.Scan(&version, &checksum) != nil || version != i+1 || i >= len(schemaMigrationFiles) {
			return node.ErrUnavailable
		}
		raw, err := migrationFiles.ReadFile(schemaMigrationFiles[i])
		if err != nil {
			return node.ErrUnavailable
		}
		expected := sha256.Sum256(raw)
		if string(checksum) != string(expected[:]) {
			return node.ErrUnavailable
		}
		i++
	}
	if rows.Err() != nil || i != BackupSchemaVersion {
		return node.ErrUnavailable
	}
	return nil
}
