package node

import (
	"context"
	"slices"
	"sync"
	"time"
)

type UserRuntimeStatus struct {
	AppliedRevision  int64  `json:"appliedRevision"`
	ApplyingRevision int64  `json:"applyingRevision,omitempty"`
	CleanupState     string `json:"cleanupState"`
	Error            string `json:"error,omitempty"`
}
type UsersView struct {
	Revision int64             `json:"revision"`
	Users    []ManagedUser     `json:"users"`
	Runtime  UserRuntimeStatus `json:"runtime"`
}

// UserManager has one application-owned writer. API calls only commit intent;
// polling and retries survive the lifetime of any individual HTTP request.
type UserManager struct {
	reservedPorts      []int
	store              UserStore
	runtime            UserRuntime
	mu                 sync.RWMutex
	status             UserRuntimeStatus
	applied            UserSnapshot // writer-owned; never exposed to HTTP callers
	cleanup            UserSnapshot // latest attempted intent, including an uncertain reload
	connection         *Connection  // protected by mu, verified applied public state
	recorded           bool
	now                func() time.Time
	expiryObservations []AccessExpiryObservation
	emergencyPending   bool
}

func NewUserManager(store UserStore, runtime UserRuntime, reservedPorts ...int) *UserManager {
	return &UserManager{now: time.Now, reservedPorts: slices.Clone(reservedPorts), store: store, runtime: runtime, status: UserRuntimeStatus{CleanupState: "pending"}}
}
func (m *UserManager) Managed() bool { _, ok := m.store.(ManagedStore); return ok }
func (m *UserManager) Connection() *Connection {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.connection == nil {
		return nil
	}
	c := *m.connection
	c.Reality.ServerNames = append([]string(nil), c.Reality.ServerNames...)
	c.Reality.ShortIDs = append([]string(nil), c.Reality.ShortIDs...)
	return &c
}
func (m *UserManager) CreateConnection(ctx context.Context, c ConnectionChange) (Operation, error) {
	store, ok := m.store.(ManagedStore)
	if !ok {
		return Operation{}, ErrUnavailable
	}
	runtime, ok := m.runtime.(ManagedRuntime)
	if !ok {
		return Operation{}, ErrUnavailable
	}
	if slices.Contains(m.reservedPorts, c.ListenPort) || slices.Contains(runtime.ReservedPorts(), c.ListenPort) {
		return Operation{}, ErrInvalid
	}
	return store.CreateConnection(ctx, c)
}
func (m *UserManager) Status() UserRuntimeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}
func (m *UserManager) View(ctx context.Context) (UsersView, error) {
	snapshot, err := m.store.Snapshot(ctx)
	if err != nil {
		return UsersView{}, err
	}
	return UsersView{Revision: snapshot.Revision, Users: snapshot.Users, Runtime: m.Status()}, nil
}
func (m *UserManager) Change(ctx context.Context, change UserChange) (Operation, error) {
	return m.store.Change(ctx, change)
}
func (m *UserManager) Operation(ctx context.Context, revision int64) (Operation, error) {
	return m.store.Operation(ctx, revision)
}

func (m *UserManager) Run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		failed := m.step(ctx)
		if failed {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		} else {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *UserManager) step(ctx context.Context) bool {
	message := ""
	now := m.now().UTC() // UTC strips Go's monotonic reading; deadlines are wall-clock values.
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	var snapshot UserSnapshot
	var err error
	if store, ok := m.store.(AccessPolicyStore); ok {
		snapshot, err = store.ReconcileAccessPolicy(readCtx, now, m.expiryObservations...)
	} else {
		snapshot, err = m.store.Snapshot(readCtx)
	}
	cancel()
	if err != nil {
		message = "desired user state unavailable"
	} else {
		m.expiryObservations = nil // SQL committed the matching epochs or an explicit newer renewal superseded them.
		// Do not overwrite a verified result whose durable acknowledgement is
		// still pending. Retry it before the next runtime transition, even when
		// that newer transition will fail. Existing revocation sweeps continue.
		if !m.recordApplied(ctx) {
			message = "user application result unavailable"
		} else if snapshot.Revision != m.applied.Revision {
			// An errored controller call may already have applied. Never keep
			// disconnecting a re-enabled identity from an older verified snapshot;
			// also retain newly disabled identities for best-effort cleanup.
			m.cleanup = snapshot
			m.mu.Lock()
			m.status.ApplyingRevision = snapshot.Revision
			m.mu.Unlock()
			applyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err = m.applySnapshot(applyCtx, snapshot)
			cancel()
			if err != nil {
				message = "user configuration application failed"
				saveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_ = m.store.RecordApply(saveCtx, snapshot.Revision, message)
				cancel()
			} else {
				m.applied = snapshot
				m.emergencyPending = false
				connection, _ := DescribeConnection(snapshot.Connection)
				m.mu.Lock()
				m.connection = connection
				m.mu.Unlock()
				m.recorded = false
			}
		}
		if message == "" && !m.recordApplied(ctx) {
			message = "user application result unavailable"
		}
	}
	// A local DB/result-write outage must not extend a known deadline. Remove
	// only expired credentials from the latest attempted snapshot, keep sweeping,
	// and persist the observed deadline before any later re-enable after recovery.
	emergency := m.cleanup
	emergency.Users = slices.Clone(emergency.Users)
	due := false
	for i, u := range emergency.Users {
		if u.ExpiryEnforcedRevision == 0 && u.ExpiresAt != nil && !now.Before(*u.ExpiresAt) {
			emergency.Users[i].ExpiryEnforcedRevision = emergency.Revision
			m.expiryObservations = append(m.expiryObservations, AccessExpiryObservation{Name: u.Name, RenewalRevision: u.RenewalRevision, ObservedAt: now})
			due = true
		}
	}
	if due || m.emergencyPending {
		m.cleanup = emergency
		applyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := m.applySnapshot(applyCtx, emergency)
		cancel()
		m.emergencyPending = err != nil
		if err != nil {
			message = "expired credential removal pending"
		}
	}
	cleanup := "pending"
	if m.cleanup.Revision > 0 {
		sweepCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		closed, err := m.runtime.SweepRevoked(sweepCtx, m.cleanup.Revoked())
		cancel()
		cleanup = "observed_empty"
		if closed > 0 {
			cleanup = "pending"
		}
		if err != nil {
			cleanup = "retrying"
			if message == "" {
				message = "revoked session cleanup pending"
			}
		}
		if m.cleanup.Revision != m.applied.Revision {
			cleanup = "retrying"
		}
	}
	m.mu.Lock()
	m.status = UserRuntimeStatus{AppliedRevision: m.applied.Revision, CleanupState: cleanup, Error: message}
	m.mu.Unlock()
	return message != ""
}

func (m *UserManager) recordApplied(ctx context.Context) bool {
	if m.applied.Revision == 0 || m.recorded {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := m.store.RecordApply(ctx, m.applied.Revision, ""); err != nil {
		return false
	}
	m.recorded = true
	return true
}

func (m *UserManager) applySnapshot(ctx context.Context, snapshot UserSnapshot) error {
	if !snapshot.Managed {
		return m.runtime.ApplyUsers(ctx, snapshot.Active())
	}
	runtime, ok := m.runtime.(ManagedRuntime)
	if !ok {
		return ErrUnavailable
	}
	var settings *VLESS
	if snapshot.Connection != nil {
		v := *snapshot.Connection
		v.Users = snapshot.Active()
		settings = &v
	}
	return runtime.ApplyState(ctx, settings)
}
