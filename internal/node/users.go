package node

import (
	"context"
	"errors"
)

var ErrConflict = errors.New("user state conflict")
var ErrNotFound = errors.New("user or operation not found")
var ErrInvalid = errors.New("invalid user change")
var ErrUnavailable = errors.New("user management unavailable")

// UserRuntime is optional; adapters must explicitly implement live user changes.
// Calls are serialized by the reconciler and must not restart the core.
type UserRuntime interface {
	ApplyUsers(context.Context, []User) error
	// SweepRevoked closes only sessions attributed to these immutable user names.
	// An empty observation is not a strict drain barrier; callers keep sweeping.
	SweepRevoked(context.Context, []string) (int, error)
}

type ManagedUser struct {
	Name    string `json:"name"`
	UUID    string `json:"-"`
	Enabled bool   `json:"enabled"`
}
type UserSnapshot struct {
	Revision int64
	Users    []ManagedUser
}

func (s UserSnapshot) Active() []User {
	users := []User{}
	for _, u := range s.Users {
		if u.Enabled {
			users = append(users, User{Name: u.Name, UUID: u.UUID})
		}
	}
	return users
}
func (s UserSnapshot) Revoked() []string {
	names := []string{}
	for _, u := range s.Users {
		if !u.Enabled {
			names = append(names, u.Name)
		}
	}
	return names
}

// Change uses an immutable name/UUID identity. UUID is required for creation and
// omitted for enable/disable; existing UUIDs may never be replaced or reused.
type UserChange struct {
	Key              string
	Name             string
	UUID             string
	Enabled          bool
	ExpectedRevision int64
}
type Operation struct {
	Revision int64  `json:"revision"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
}

// UserStore commits desired state, revision and idempotent operation atomically.
// Implementations must sanitize errors and return coherent, ordered snapshots.
type UserStore interface {
	Initialize(context.Context, []User) (UserSnapshot, error)
	Snapshot(context.Context) (UserSnapshot, error)
	Change(context.Context, UserChange) (Operation, error)
	Operation(context.Context, int64) (Operation, error)
	RecordApply(context.Context, int64, string) error
}
