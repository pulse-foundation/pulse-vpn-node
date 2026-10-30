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
	Name               string `json:"name"`
	RuntimeName        string `json:"-"`
	CredentialRevision int64  `json:"credentialRevision"`
	UUID               string `json:"-"`
	Enabled            bool   `json:"enabled"`
}
type UserSnapshot struct {
	Revision     int64
	Users        []ManagedUser
	Connection   *VLESS
	Managed      bool
	RevokedNames []string
}

func (s UserSnapshot) Active() []User {
	users := []User{}
	for _, u := range s.Users {
		if u.Enabled {
			name := u.RuntimeName
			if name == "" {
				name = u.Name
			}
			users = append(users, User{Name: name, UUID: u.UUID})
		}
	}
	return users
}
func (s UserSnapshot) Revoked() []string {
	names := append([]string{}, s.RevokedNames...)
	for _, u := range s.Users {
		if !u.Enabled {
			name := u.RuntimeName
			if name == "" {
				name = u.Name
			}
			names = append(names, name)
		}
	}
	return names
}

// UserChange keeps a stable accounting name. UUID is required for creation or
// explicit rotation, omitted for enable/disable and deletion. Retired UUIDs are
// never reused; rotation preserves enabled state and accounting.
type UserChange struct {
	Kind             string // empty/update, delete, or rotate
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

// ManagedStore extends the same revision stream with its singleton listener.
// InitializeManaged imports a legacy startup file once, never overwriting SQL state.
type ManagedStore interface {
	UserStore
	InitializeManaged(context.Context, *VLESS) (UserSnapshot, error)
	CreateConnection(context.Context, ConnectionChange) (Operation, error)
}
type ManagedRuntime interface {
	// ReservedPorts identifies runtime-owned TCP ports that a listener must not use.
	// This admission check is independent of whether a VPN listener is already active.
	ReservedPorts() []int
	ApplyState(context.Context, *VLESS) error
}
