package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

// InitializeManaged performs a one-time import before the application starts its
// writer. A retained legacy file never overwrites managed keys, users or revocations.
func (s *Store) InitializeManaged(ctx context.Context, seed *node.VLESS) (node.UserSnapshot, error) {
	var users []node.User
	if seed != nil {
		if seed.Validate() != nil {
			return node.UserSnapshot{}, node.ErrInvalid
		}
		users = seed.Users
	}
	if _, err := s.Initialize(ctx, users); err != nil {
		return node.UserSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	revision, _, err := lockState(ctx, tx)
	if err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	var imported bool
	if err = tx.QueryRow(ctx, "SELECT connection_initialized FROM node_user_state WHERE singleton").Scan(&imported); err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	if !imported {
		var raw []byte
		if seed != nil {
			v := *seed
			v.Users = []node.User{}
			raw, err = json.Marshal(v)
			if err != nil {
				return node.UserSnapshot{}, node.ErrInvalid
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE node_user_state SET connection_initialized=true,connection_settings=$1 WHERE singleton", raw); err != nil {
			return node.UserSnapshot{}, node.ErrUnavailable
		}
	}
	result, err := snapshot(ctx, tx, revision)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return node.UserSnapshot{}, node.ErrUnavailable
	}
	return result, nil
}

func (s *Store) CreateConnection(ctx context.Context, c node.ConnectionChange) (node.Operation, error) {
	if !keyPattern.MatchString(c.Key) || c.ExpectedRevision < 1 || c.Transport.Type != "raw" {
		return node.Operation{}, node.ErrInvalid
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return node.Operation{}, node.ErrInvalid
	}
	hash := sha256.Sum256(append([]byte("connection:"), raw...))
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
	var prior node.Operation
	var savedHash []byte
	err = tx.QueryRow(ctx, "SELECT revision,state,error,request_hash FROM node_user_operations WHERE idempotency_key=$1", c.Key).Scan(&prior.Revision, &prior.State, &prior.Error, &savedHash)
	if err == nil {
		if string(savedHash) != string(hash[:]) {
			return node.Operation{}, node.ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return node.Operation{}, node.ErrUnavailable
	}
	if c.ExpectedRevision != revision {
		return node.Operation{}, node.ErrConflict
	}
	var settings []byte
	var managed bool
	if err = tx.QueryRow(ctx, "SELECT connection_initialized,connection_settings FROM node_user_state WHERE singleton").Scan(&managed, &settings); err != nil || !managed {
		return node.Operation{}, node.ErrUnavailable
	}
	if settings != nil {
		var v node.VLESS
		if json.Unmarshal(settings, &v) != nil {
			return node.Operation{}, node.ErrUnavailable
		}
		if v.Port != c.ListenPort || v.Reality.Destination != c.Reality.Destination || !slices.Equal(v.Reality.ServerNames, c.Reality.ServerNames) {
			return node.Operation{}, node.ErrConflict
		}
	} else {
		v, err := c.NewSettings()
		if err != nil {
			return node.Operation{}, err
		}
		settings, err = json.Marshal(v)
		if err != nil {
			return node.Operation{}, node.ErrUnavailable
		}
		if _, err = tx.Exec(ctx, "UPDATE node_user_state SET connection_settings=$1 WHERE singleton", settings); err != nil {
			return node.Operation{}, node.ErrUnavailable
		}
	}
	if revision == int64(1<<63-1) {
		return node.Operation{}, node.ErrUnavailable
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

func changeUser(ctx context.Context, tx pgx.Tx, c node.UserChange, revision int64) error {
	var identity string
	var deleted bool
	err := tx.QueryRow(ctx, "SELECT uuid::text,deleted FROM node_users WHERE name=$1", c.Name).Scan(&identity, &deleted)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return node.ErrUnavailable
	}
	switch c.Kind {
	case "delete":
		if !exists {
			return node.ErrNotFound
		}
		if _, err = tx.Exec(ctx, "UPDATE node_users SET enabled=false,deleted=true WHERE name=$1", c.Name); err != nil {
			return sanitized(err)
		}
		_, err = tx.Exec(ctx, "UPDATE node_user_credentials SET revoked=true WHERE user_name=$1", c.Name)
		return sanitized(err)
	case "rotate":
		if !exists || deleted {
			return node.ErrNotFound
		}
		// A durable retry with the same UUID preserves identity and its generation.
		if identity == c.UUID {
			return nil
		}
	default:
		if exists && !deleted {
			if c.UUID != "" {
				return node.ErrConflict
			}
			_, err = tx.Exec(ctx, "UPDATE node_users SET enabled=$2 WHERE name=$1", c.Name, c.Enabled)
			return sanitized(err)
		}
		if c.UUID == "" {
			return node.ErrInvalid
		}
	}
	// Credential history is never reused. Bound its growth rather than silently
	// forgetting revoked credentials or allowing an unbounded sweep snapshot.
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM node_user_credentials").Scan(&count); err != nil {
		return node.ErrUnavailable
	}
	if count >= 65536 {
		return node.ErrUnavailable
	}
	if !exists || deleted {
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM node_users WHERE NOT deleted").Scan(&count); err != nil {
			return node.ErrUnavailable
		}
		if count >= maxUsers {
			return node.ErrInvalid
		}
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM node_user_credentials WHERE uuid=$1)", c.UUID).Scan(&used); err != nil {
		return node.ErrUnavailable
	}
	if used {
		return node.ErrConflict
	}
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return node.ErrUnavailable
	}
	runtimeName := "c_" + hex.EncodeToString(entropy[:])
	if exists {
		if _, err = tx.Exec(ctx, "UPDATE node_user_credentials SET revoked=true WHERE user_name=$1", c.Name); err != nil {
			return node.ErrUnavailable
		}
		if c.Kind == "rotate" {
			_, err = tx.Exec(ctx, "UPDATE node_users SET uuid=$2,credential_revision=$3 WHERE name=$1", c.Name, c.UUID, revision)
		} else {
			_, err = tx.Exec(ctx, "UPDATE node_users SET uuid=$2,enabled=$3,deleted=false,credential_revision=$4 WHERE name=$1", c.Name, c.UUID, c.Enabled, revision)
		}
	} else {
		_, err = tx.Exec(ctx, "INSERT INTO node_users(name,uuid,enabled,credential_revision) VALUES($1,$2,$3,$4)", c.Name, c.UUID, c.Enabled, revision)
	}
	if err != nil {
		return sanitized(err)
	}
	_, err = tx.Exec(ctx, "INSERT INTO node_user_credentials(uuid,user_name,runtime_name) VALUES($1,$2,$3)", c.UUID, c.Name, runtimeName)
	return sanitized(err)
}
