package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

func connectionRequest(revision int64, key string) node.ConnectionChange {
	c := node.ConnectionChange{Key: key, ExpectedRevision: revision, ListenPort: 8443}
	c.Transport.Type = "raw"
	c.Reality.Destination = "target.test:443"
	c.Reality.ServerNames = []string{"target.test"}
	return c
}
func TestManagedIdleConnectionPersistenceAndSingleton(t *testing.T) {
	s, dsn := testStore(t)
	ctx := context.Background()
	initial, err := s.InitializeManaged(ctx, nil)
	if err != nil || !initial.Managed || initial.Connection != nil || initial.Revision != 1 {
		t.Fatal("managed idle initialization failed")
	}
	c := connectionRequest(1, "create")
	op, err := s.CreateConnection(ctx, c)
	if err != nil || op.Revision != 2 || op.State != "queued" {
		t.Fatal("create connection failed")
	}
	replay, err := s.CreateConnection(ctx, c)
	if err != nil || replay != op {
		t.Fatal("idempotent create failed")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil || snapshot.Connection == nil || snapshot.Revision != 2 {
		t.Fatal("missing desired connection")
	}
	originalKey := snapshot.Connection.Reality.PrivateKey
	c = connectionRequest(2, "same-settings")
	if _, err = s.CreateConnection(ctx, c); err != nil {
		t.Fatal("identical create failed")
	}
	c = connectionRequest(3, "conflict")
	c.ListenPort = 9443
	if _, err = s.CreateConnection(ctx, c); !errors.Is(err, node.ErrConflict) {
		t.Fatal("divergent singleton create accepted")
	}
	other, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	snapshot, err = other.InitializeManaged(ctx, nil)
	if err != nil || snapshot.Connection == nil || snapshot.Connection.Reality.PrivateKey != originalKey || snapshot.Revision != 3 {
		t.Fatal("restart lost configured state or changed key")
	}
	c = connectionRequest(2, "stale")
	if _, err = other.CreateConnection(ctx, c); !errors.Is(err, node.ErrConflict) {
		t.Fatal("stale create accepted")
	}
}

func TestLegacyImportPreservesRevocationsAndKeys(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	v, err := connectionRequest(1, "unused").NewSettings()
	if err != nil {
		t.Fatal(err)
	}
	v.Users = []node.User{{Name: "alice", UUID: testUUID}}
	if _, err = s.Initialize(ctx, v.Users); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Change(ctx, node.UserChange{Key: "disable", Name: "alice", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.InitializeManaged(ctx, v)
	if err != nil || snapshot.Connection.Reality.PrivateKey != v.Reality.PrivateKey || snapshot.Users[0].Enabled || snapshot.Revision != 2 {
		t.Fatal("legacy import reset keys or revoked user")
	}
	changed, err := connectionRequest(1, "unused").NewSettings()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.InitializeManaged(ctx, changed)
	if err != nil || snapshot.Connection.Reality.PrivateKey != v.Reality.PrivateKey || snapshot.Users[0].Enabled {
		t.Fatal("retained startup file overwrote managed state")
	}
}

func TestRotateDeleteRecreatePreserveAccountingAndRetireCredentials(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartTraffic(ctx, "generation"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	sample := traffic.Snapshot{Generation: "generation", Sequence: 1, ObservedAt: now, Connections: []traffic.Connection{{ID: "old-session", User: "alice", StartedAt: now, Upload: 100, Download: 200}}}
	if err := s.ApplyTraffic(ctx, sample); err != nil {
		t.Fatal(err)
	}
	rotated := "b47ac10b-58cc-4372-a567-0e02b2c3d479"
	op, err := s.Change(ctx, node.UserChange{Kind: "rotate", Key: "rotate", Name: "alice", UUID: rotated, ExpectedRevision: 1})
	if err != nil || op.Revision != 2 {
		t.Fatal("rotation failed")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Users) != 1 || !snapshot.Users[0].Enabled || snapshot.Users[0].CredentialRevision != 2 || snapshot.Active()[0].Name == "alice" || len(snapshot.Revoked()) != 1 || snapshot.Revoked()[0] != "alice" {
		t.Fatal("rotation lost stable identity or revocation generation")
	}
	currentRuntime := snapshot.Active()[0].Name
	sample.Sequence = 2
	sample.Connections[0].Upload = 150
	sample.Connections = append(sample.Connections, traffic.Connection{ID: "new-session", User: currentRuntime, StartedAt: now, Upload: 30, Download: 40})
	if err = s.ApplyTraffic(ctx, sample); err != nil {
		t.Fatal(err)
	}
	totals, err := s.TrafficTotals(ctx)
	if err != nil || len(totals.Users) != 1 || totals.Users[0].UploadBytes != 180 || totals.Users[0].DownloadBytes != 240 {
		t.Fatal("credential generations lost or duplicated accounting")
	}
	if _, err = s.Change(ctx, node.UserChange{Kind: "rotate", Key: "retry-current", Name: "alice", UUID: rotated, ExpectedRevision: 2}); err != nil {
		t.Fatal("current credential rotation retry failed")
	}
	snapshot, _ = s.Snapshot(ctx)
	if snapshot.Users[0].CredentialRevision != 2 || snapshot.Active()[0].Name != currentRuntime {
		t.Fatal("retry changed credential generation")
	}
	if _, err = s.Change(ctx, node.UserChange{Kind: "rotate", Key: "reuse", Name: "alice", UUID: testUUID, ExpectedRevision: 3}); !errors.Is(err, node.ErrConflict) {
		t.Fatal("revoked UUID reused")
	}
	if _, err = s.Change(ctx, node.UserChange{Kind: "delete", Key: "delete", Name: "alice", ExpectedRevision: 3}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = s.Snapshot(ctx)
	if len(snapshot.Users) != 0 || len(snapshot.Active()) != 0 || len(snapshot.Revoked()) != 2 {
		t.Fatal("deleted user visible or credentials not revoked")
	}
	totals, _ = s.TrafficTotals(ctx)
	if len(totals.Users) != 0 {
		t.Fatal("deleted user visible in traffic")
	}
	sample.Sequence = 3
	sample.Connections[0].Upload = 170
	if err = s.ApplyTraffic(ctx, sample); err != nil {
		t.Fatal("late deleted session accounting failed")
	}
	if _, err = s.Change(ctx, node.UserChange{Key: "recreate-old", Name: "alice", UUID: rotated, Enabled: true, ExpectedRevision: 4}); !errors.Is(err, node.ErrConflict) {
		t.Fatal("deleted UUID reused")
	}
	if _, err = s.Change(ctx, node.UserChange{Key: "recreate", Name: "alice", UUID: "c47ac10b-58cc-4372-a567-0e02b2c3d479", Enabled: true, ExpectedRevision: 4}); err != nil {
		t.Fatal(err)
	}
	totals, _ = s.TrafficTotals(ctx)
	if len(totals.Users) != 1 || totals.Users[0].UploadBytes != 200 {
		t.Fatal("recreate reset history")
	}
	snapshot, _ = s.Snapshot(ctx)
	if snapshot.Users[0].CredentialRevision != 5 || snapshot.Active()[0].Name == currentRuntime {
		t.Fatal("recreate reused runtime identity")
	}
}

func TestConnectionAndUsersShareRevisionConflict(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.InitializeManaged(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.CreateConnection(ctx, connectionRequest(1, "connection"))
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := s.Change(ctx, node.UserChange{Key: "user", Name: "alice", UUID: testUUID, Enabled: true, ExpectedRevision: 1})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, node.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("mutations did not serialize on one revision")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil || snapshot.Revision != 2 || (snapshot.Connection != nil && len(snapshot.Users) != 0) {
		t.Fatal("incoherent combined snapshot")
	}
}

func TestLifecycleOperationFailureRollsBackAllIntent(t *testing.T) {
	for _, kind := range []string{"connection", "delete", "rotate"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := testStore(t)
			ctx := context.Background()
			if _, err := s.InitializeManaged(ctx, nil); err != nil {
				t.Fatal(err)
			}
			revision := int64(1)
			if kind != "connection" {
				if _, err := s.CreateConnection(ctx, connectionRequest(revision, "connection")); err != nil {
					t.Fatal(err)
				}
				revision++
				if _, err := s.Change(ctx, node.UserChange{Key: "seed", Name: "alice", UUID: testUUID, Enabled: true, ExpectedRevision: revision}); err != nil {
					t.Fatal(err)
				}
				revision++
			}
			before, err := s.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.pool.Exec(ctx, "ALTER TABLE node_user_operations ADD CONSTRAINT reject_lifecycle CHECK (idempotency_key <> 'reject')"); err != nil {
				t.Fatal(err)
			}
			if kind == "connection" {
				_, err = s.CreateConnection(ctx, connectionRequest(revision, "reject"))
			} else {
				change := node.UserChange{Kind: kind, Key: "reject", Name: "alice", ExpectedRevision: revision}
				if kind == "rotate" {
					change.UUID = "b47ac10b-58cc-4372-a567-0e02b2c3d479"
				}
				_, err = s.Change(ctx, change)
			}
			if !errors.Is(err, node.ErrInvalid) {
				t.Fatal("operation constraint did not reject mutation")
			}
			after, err := s.Snapshot(ctx)
			if err != nil || after.Revision != before.Revision || (after.Connection == nil) != (before.Connection == nil) || len(after.Users) != len(before.Users) || len(after.RevokedNames) != 0 {
				t.Fatal("failed operation left partial intent")
			}
			if kind != "connection" && (after.Users[0].UUID != before.Users[0].UUID || after.Users[0].RuntimeName != before.Users[0].RuntimeName || !after.Users[0].Enabled) {
				t.Fatal("failed operation retired or replaced original credential")
			}
			if _, err = s.Operation(ctx, revision+1); !errors.Is(err, node.ErrNotFound) {
				t.Fatal("failed operation survived transaction rollback")
			}
		})
	}
}

func TestManagedIdleRejectsUnapplicableUserIntent(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.InitializeManaged(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(ctx, node.UserChange{Key: "premature-user", Name: "alice", UUID: testUUID, Enabled: true, ExpectedRevision: 1}); !errors.Is(err, node.ErrConflict) {
		t.Fatal("idle node accepted user operation without a listener")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil || snapshot.Revision != 1 || len(snapshot.Users) != 0 {
		t.Fatal("rejected idle mutation changed desired state")
	}
}
