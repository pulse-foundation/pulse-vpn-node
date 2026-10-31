// Package postgres owns the node's private desired-user database. One node
// process must own each database; it does not lease runtime ownership between
// competing agents. SQL transactions never extend across core controller calls.
package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

const queryTimeout = 10 * time.Second
const maxUsers = 1024

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var keyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct{ pool *pgxpool.Pool }

var _ node.UserStore = (*Store)(nil)

// Open establishes a bounded pool without creating or changing schema. Call
// Migrate explicitly from the deployment migration command before serving.
func Open(ctx context.Context, dsn string) (*Store, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, node.ErrUnavailable
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, node.ErrUnavailable
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, node.ErrUnavailable
	}
	return &Store{pool: pool}, nil
}
func (s *Store) Close() { s.pool.Close() }

// Migrate applies reviewed embedded SQL atomically and records its checksum.
// Existing migration contents cannot change silently, and a newer schema is
// rejected rather than guessed at. The transaction lock serializes migrators.
func (s *Store) Migrate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(72781155962411)"); err != nil {
		return node.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS node_schema_migrations (version integer PRIMARY KEY, checksum bytea NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return node.ErrUnavailable
	}
	// The explicit order is the reviewed schema history. Reject gaps, unknown
	// versions and changed checksums before applying any pending migration.
	files := []string{"migrations/001_users.sql", "migrations/002_traffic.sql", "migrations/003_node_lifecycle.sql", "migrations/004_access_policy.sql"}
	rows, err := tx.Query(ctx, "SELECT version,checksum FROM node_schema_migrations ORDER BY version")
	if err != nil {
		return node.ErrUnavailable
	}
	var saved [][]byte
	for rows.Next() {
		var version int
		var checksum []byte
		if err := rows.Scan(&version, &checksum); err != nil || version != len(saved)+1 || version > len(files) {
			rows.Close()
			return node.ErrUnavailable
		}
		saved = append(saved, checksum)
	}
	rows.Close()
	if rows.Err() != nil {
		return node.ErrUnavailable
	}
	for i, file := range files {
		sql, err := migrationFiles.ReadFile(file)
		if err != nil {
			return node.ErrUnavailable
		}
		sum := sha256.Sum256(sql)
		if i < len(saved) {
			if string(saved[i]) != string(sum[:]) {
				return node.ErrUnavailable
			}
			continue
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return node.ErrUnavailable
		}
		if _, err = tx.Exec(ctx, "INSERT INTO node_schema_migrations(version,checksum) VALUES($1,$2)", i+1, sum[:]); err != nil {
			return node.ErrUnavailable
		}
	}
	return sanitized(tx.Commit(ctx))
}

func validUUID(id string) bool {
	return uuidPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}
func sanitized(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return node.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "23505":
			return node.ErrConflict
		case "23514", "22001", "22P02":
			return node.ErrInvalid
		}
	}
	return node.ErrUnavailable
}

func lockState(ctx context.Context, tx pgx.Tx) (int64, bool, error) {
	var revision int64
	var initialized bool
	err := tx.QueryRow(ctx, "SELECT revision,initialized FROM node_user_state WHERE singleton FOR UPDATE").Scan(&revision, &initialized)
	return revision, initialized, err
}
func snapshot(ctx context.Context, tx pgx.Tx, revision int64) (node.UserSnapshot, error) {
	result := node.UserSnapshot{Revision: revision, Users: []node.ManagedUser{}}
	var settings []byte
	if err := tx.QueryRow(ctx, "SELECT connection_initialized,connection_settings FROM node_user_state WHERE singleton").Scan(&result.Managed, &settings); err != nil {
		return result, node.ErrUnavailable
	}
	if settings != nil {
		if json.Unmarshal(settings, &result.Connection) != nil || result.Connection == nil || result.Connection.Validate() != nil {
			return result, node.ErrUnavailable
		}
	}
	rows, err := tx.Query(ctx, "SELECT u.name,u.uuid::text,u.enabled,u.credential_revision,c.runtime_name,u.expires_at,u.policy_revision,u.expiry_enforced_revision,u.renewal_revision FROM node_users u JOIN node_user_credentials c ON c.uuid=u.uuid WHERE NOT u.deleted ORDER BY u.name LIMIT $1", maxUsers+1)
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var u node.ManagedUser
		if err := rows.Scan(&u.Name, &u.UUID, &u.Enabled, &u.CredentialRevision, &u.RuntimeName, &u.ExpiresAt, &u.PolicyRevision, &u.ExpiryEnforcedRevision, &u.RenewalRevision); err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
		result.Users = append(result.Users, u)
	}
	if rows.Err() != nil || len(result.Users) > maxUsers {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	rows.Close()
	rows, err = tx.Query(ctx, "SELECT runtime_name FROM node_user_credentials WHERE revoked ORDER BY runtime_name")
	if err != nil {
		return result, node.ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			return result, node.ErrUnavailable
		}
		result.RevokedNames = append(result.RevokedNames, name)
	}
	if rows.Err() != nil {
		return result, node.ErrUnavailable
	}
	return result, nil
}

func (s *Store) Initialize(ctx context.Context, seed []node.User) (node.UserSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	revision, initialized, err := lockState(ctx, tx)
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	if !initialized {
		seed = append([]node.User(nil), seed...)
		for i := range seed {
			seed[i].UUID = strings.ToLower(seed[i].UUID)
		}
		if len(seed) > maxUsers {
			return node.UserSnapshot{}, node.ErrInvalid
		}
		names := make(map[string]bool, len(seed))
		ids := make(map[string]bool, len(seed))
		for _, u := range seed {
			if !namePattern.MatchString(u.Name) || !validUUID(u.UUID) || names[u.Name] || ids[u.UUID] {
				return node.UserSnapshot{}, node.ErrInvalid
			}
			names[u.Name] = true
			ids[u.UUID] = true
		}
		for _, u := range seed {
			if _, err = tx.Exec(ctx, "INSERT INTO node_users(name,uuid,enabled) VALUES($1,$2,true)", u.Name, u.UUID); err != nil {
				return node.UserSnapshot{}, sanitized(err)
			}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO node_user_credentials(uuid,user_name,runtime_name) SELECT uuid,name,name FROM node_users ON CONFLICT(uuid) DO NOTHING"); err != nil {
			return node.UserSnapshot{}, sanitized(err)
		}
		revision = 1
		if _, err = tx.Exec(ctx, "UPDATE node_user_state SET initialized=true,revision=1 WHERE singleton"); err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
		if _, err = tx.Exec(ctx, "INSERT INTO node_user_operations(revision,state) VALUES(1,'queued')"); err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
	}
	result, err := snapshot(ctx, tx, revision)
	if err != nil {
		return node.UserSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	return result, nil
}

func (s *Store) Snapshot(ctx context.Context) (node.UserSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var revision int64
	var initialized bool
	if err = tx.QueryRow(ctx, "SELECT revision,initialized FROM node_user_state WHERE singleton").Scan(&revision, &initialized); err != nil || !initialized {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	result, err := snapshot(ctx, tx, revision)
	if err != nil {
		return node.UserSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	return result, nil
}

func (s *Store) Change(ctx context.Context, c node.UserChange) (node.Operation, error) {
	if !keyPattern.MatchString(c.Key) || !namePattern.MatchString(c.Name) || c.ExpectedRevision < 0 || (c.UUID != "" && !validUUID(c.UUID)) || (c.Kind != "" && c.Kind != "delete" && c.Kind != "rotate") || (c.Kind == "delete" && c.UUID != "") || (c.Kind == "rotate" && c.UUID == "") {
		return node.Operation{}, node.ErrInvalid
	}
	if c.AccessPolicy != nil {
		if (c.Kind != "" && c.Kind != "rotate") || (c.Kind == "rotate" && c.AccessPolicy.Renew) || c.AccessPolicy.Revision < 1 || c.AccessPolicy.ExpiresAt != nil && (c.AccessPolicy.ExpiresAt.Year() < 1 || c.AccessPolicy.ExpiresAt.Year() > 9999) {
			return node.Operation{}, node.ErrInvalid
		}
	}
	// Explicit fields make the durable hash independent of future domain fields.
	body, err := json.Marshal(struct {
		Name, UUID       string
		Enabled          bool
		ExpectedRevision int64
	}{c.Name, c.UUID, c.Enabled, c.ExpectedRevision})
	if err != nil {
		return node.Operation{}, node.ErrInvalid
	}
	if c.Kind != "" {
		body = append([]byte(c.Kind+":"), body...)
	}
	if c.AccessPolicy != nil {
		policy, _ := json.Marshal(c.AccessPolicy)
		body = append(append(body, '\n'), policy...)
	}
	hash := sha256.Sum256(body)
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.Operation{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	revision, initialized, err := lockState(ctx, tx)
	if err != nil || !initialized {
		return node.Operation{}, node.ErrUnavailable
	}
	var existing node.Operation
	var savedHash []byte
	err = tx.QueryRow(ctx, "SELECT revision,state,error,request_hash FROM node_user_operations WHERE idempotency_key=$1", c.Key).Scan(&existing.Revision, &existing.State, &existing.Error, &savedHash)
	if err == nil {
		if string(savedHash) != string(hash[:]) {
			return node.Operation{}, node.ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return node.Operation{}, node.ErrUnavailable
	}
	if c.ExpectedRevision != revision {
		return node.Operation{}, node.ErrConflict
	}

	// A user intent cannot be verified applied without a connection. Idle nodes
	// expose reads/revisions so callers can create the singleton first.
	var idle bool
	if err = tx.QueryRow(ctx, "SELECT connection_initialized AND connection_settings IS NULL FROM node_user_state WHERE singleton").Scan(&idle); err != nil {
		return node.Operation{}, node.ErrUnavailable
	}
	if idle {
		return node.Operation{}, node.ErrConflict
	}
	// Overflow is an unavailable store, never a wrapped credential revision.
	if revision == int64(1<<63-1) {
		return node.Operation{}, node.ErrUnavailable
	}
	if err = changeUser(ctx, tx, c, revision+1); err != nil {
		return node.Operation{}, err
	}
	if c.AccessPolicy != nil {
		if err = changeAccessPolicy(ctx, tx, c); err != nil {
			return node.Operation{}, err
		}
	}
	revision++
	if _, err = tx.Exec(ctx, "UPDATE node_user_state SET revision=$1 WHERE singleton", revision); err != nil {
		return node.Operation{}, node.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, "INSERT INTO node_user_operations(revision,idempotency_key,request_hash,state) VALUES($1,$2,$3,'queued')", revision, c.Key, hash[:]); err != nil {
		return node.Operation{}, sanitized(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return node.Operation{}, node.ErrUnavailable
	}
	return node.Operation{Revision: revision, State: "queued"}, nil
}

func (s *Store) Operation(ctx context.Context, revision int64) (node.Operation, error) {
	if revision < 1 {
		return node.Operation{}, node.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var op node.Operation
	err := s.pool.QueryRow(ctx, "SELECT revision,state,error FROM node_user_operations WHERE revision=$1", revision).Scan(&op.Revision, &op.State, &op.Error)
	if err != nil {
		return node.Operation{}, sanitized(err)
	}
	return op, nil
}

func (s *Store) RecordApply(ctx context.Context, revision int64, applyError string) error {
	if revision < 1 {
		return node.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	latest, initialized, err := lockState(ctx, tx)
	if err != nil || !initialized {
		return node.ErrUnavailable
	}
	state, message := "applied", ""
	if applyError != "" {
		state, message = "failed", "runtime apply failed"
	}
	tag, err := tx.Exec(ctx, "UPDATE node_user_operations SET state=$2,error=$3 WHERE revision=$1", revision, state, message)
	if err != nil {
		return node.ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return node.ErrNotFound
	}
	if applyError == "" {
		if _, err = tx.Exec(ctx, "UPDATE node_user_state SET applied_revision=GREATEST(applied_revision,$1) WHERE singleton", revision); err != nil {
			return node.ErrUnavailable
		}
		if revision == latest {
			if _, err = tx.Exec(ctx, "UPDATE node_user_operations SET state='superseded',error='' WHERE revision<$1 AND state IN ('queued','failed')", revision); err != nil {
				return node.ErrUnavailable
			}
		}
	}
	return sanitized(tx.Commit(ctx))
}

// String deliberately omits pool and connection configuration.
func (s *Store) String() string { return "postgres user store" }
