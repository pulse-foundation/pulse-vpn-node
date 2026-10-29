package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

const testUUID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

func TestInitializeNormalizesValidSeedUUIDCase(t *testing.T) {
	store, _ := testStore(t)
	seed := []node.User{{Name: "alice", UUID: strings.ToUpper(testUUID)}}
	snapshot, err := store.Initialize(context.Background(), seed)
	if err != nil || len(snapshot.Users) != 1 || snapshot.Users[0].UUID != testUUID {
		t.Fatal("valid uppercase seed rejected or not normalized")
	}
	if seed[0].UUID != strings.ToUpper(testUUID) {
		t.Fatal("mutated caller seed")
	}
}

// Each test owns a random schema in an explicitly supplied disposable database.
// Run: TEST_DATABASE_URL_FILE=/path/to/dsn go test -race ./internal/postgres
func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if path := os.Getenv("TEST_DATABASE_URL_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("read test database configuration")
		}
		dsn = strings.TrimSpace(string(b))
	}
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL_FILE or TEST_DATABASE_URL for disposable PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("configure test database")
	}
	t.Cleanup(admin.Close)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "node_test_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create test schema")
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("drop owned test schema")
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse test database configuration")
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dsn
}

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, dsn := openTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, dsn
}

func TestDurableUserChanges(t *testing.T) {
	s, dsn := testStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}})
	if err != nil || snap.Revision != 1 || len(snap.Users) != 1 {
		t.Fatalf("initialize: revision=%d users=%d error=%v", snap.Revision, len(snap.Users), err)
	}
	disable := node.UserChange{Key: "disable-alice", Name: "alice", ExpectedRevision: 1}
	op, err := s.Change(ctx, disable)
	if err != nil || op.Revision != 2 || op.State != "queued" {
		t.Fatalf("change: %+v %v", op, err)
	}
	replay, err := s.Change(ctx, disable)
	if err != nil || replay != op {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := disable
	changed.Enabled = true
	if _, err := s.Change(ctx, changed); !errors.Is(err, node.ErrConflict) {
		t.Fatalf("different payload: %v", err)
	}
	if err := s.RecordApply(ctx, 2, "database-password-must-not-leak"); err != nil {
		t.Fatal(err)
	}
	failed, err := s.Operation(ctx, 2)
	if err != nil || failed.State != "failed" || strings.Contains(failed.Error, "password") {
		t.Fatalf("failure: %+v %v", failed, err)
	}
	// Reopening proves desired state, operations and idempotency survive process state loss.
	s.Close()
	reopened, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	snap, err = reopened.Initialize(ctx, []node.User{{Name: "different", UUID: "not-a-uuid"}})
	if err != nil || snap.Revision != 2 || len(snap.Users) != 1 || snap.Users[0].Enabled {
		t.Fatalf("restart: revision=%d users=%d error=%v", snap.Revision, len(snap.Users), err)
	}
	replay, err = reopened.Change(ctx, disable)
	if err != nil || replay != failed {
		t.Fatalf("durable replay: %+v %v", replay, err)
	}
	if err := reopened.RecordApply(ctx, 2, ""); err != nil {
		t.Fatal(err)
	}
	initial, err := reopened.Operation(ctx, 1)
	if err != nil || initial.State != "superseded" {
		t.Fatalf("initial operation: %+v %v", initial, err)
	}
}

func TestConcurrentChangesAndCoherentSnapshots(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Change(ctx, node.UserChange{Key: fmt.Sprintf("key-%d", i), Name: fmt.Sprintf("user-%d", i), UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Enabled: true, ExpectedRevision: 1})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, node.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful writers: %d", successes)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || snap.Revision != 2 || len(snap.Users) != 1 {
		t.Fatalf("atomic snapshot: revision=%d users=%d error=%v", snap.Revision, len(snap.Users), err)
	}
	identical := node.UserChange{Key: "same-key", Name: "second", UUID: testUUID, Enabled: true, ExpectedRevision: 2}
	ops := make(chan node.Operation, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); op, err := s.Change(ctx, identical); ops <- op; errs <- err }()
	}
	wg.Wait()
	close(ops)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for op := range ops {
		if op.Revision != 3 {
			t.Fatalf("same key revision: %d", op.Revision)
		}
	}
	// Every snapshot observes users and revision from the same committed state.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(3); i < 23; i++ {
			_, err := s.Change(ctx, node.UserChange{Key: fmt.Sprintf("append-%d", i), Name: fmt.Sprintf("append-%d", i), UUID: fmt.Sprintf("00000000-0000-4000-8001-%012d", i), Enabled: true, ExpectedRevision: i})
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for {
		snap, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(snap.Users)) != snap.Revision-1 {
			t.Errorf("incoherent snapshot: revision=%d count=%d", snap.Revision, len(snap.Users))
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func TestInvalidChangesLeaveNoPartialState(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change node.UserChange
		want   error
	}{
		{"replace identity", node.UserChange{Key: "replace", Name: "alice", UUID: "00000000-0000-4000-8000-000000000001", Enabled: true, ExpectedRevision: 1}, node.ErrConflict},
		{"reuse disabled identity", node.UserChange{Key: "reuse", Name: "bob", UUID: testUUID, ExpectedRevision: 1}, node.ErrConflict},
		{"missing new UUID", node.UserChange{Key: "missing", Name: "bob", Enabled: true, ExpectedRevision: 1}, node.ErrInvalid},
		{"noncanonical UUID", node.UserChange{Key: "uppercase", Name: "bob", UUID: strings.ToUpper(testUUID), Enabled: true, ExpectedRevision: 1}, node.ErrInvalid},
		{"bad name", node.UserChange{Key: "bad-name", Name: "bad name", UUID: testUUID, ExpectedRevision: 1}, node.ErrInvalid},
		{"stale revision", node.UserChange{Key: "stale", Name: "alice", ExpectedRevision: 0}, node.ErrConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Change(ctx, tc.change); !errors.Is(err, tc.want) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
			snap, err := s.Snapshot(ctx)
			if err != nil || snap.Revision != 1 || len(snap.Users) != 1 || !snap.Users[0].Enabled {
				t.Fatalf("partial state: revision=%d count=%d error=%v", snap.Revision, len(snap.Users), err)
			}
		})
	}
	if _, err := s.Operation(ctx, 2); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("unexpected operation: %v", err)
	}
}

func TestInitializationAndApplyOrdering(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, []node.User{{Name: "a", UUID: testUUID}, {Name: "b", UUID: testUUID}}); !errors.Is(err, node.ErrInvalid) {
		t.Fatalf("invalid seed: %v", err)
	}
	if _, err := s.Snapshot(ctx); !errors.Is(err, node.ErrUnavailable) {
		t.Fatalf("partial initialization: %v", err)
	}
	if _, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	for rev := int64(1); rev < 4; rev++ {
		if _, err := s.Change(ctx, node.UserChange{Key: fmt.Sprintf("toggle-%d", rev), Name: "alice", Enabled: rev%2 == 0, ExpectedRevision: rev}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordApply(ctx, 2, ""); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []int64{1, 3, 4} {
		op, err := s.Operation(ctx, rev)
		if err != nil || op.State != "queued" {
			t.Fatalf("older completion touched revision %d: %+v %v", rev, op, err)
		}
	}
	if err := s.RecordApply(ctx, 3, "failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordApply(ctx, 4, ""); err != nil {
		t.Fatal(err)
	}
	for _, rev := range []int64{1, 3} {
		op, err := s.Operation(ctx, rev)
		if err != nil || op.State != "superseded" {
			t.Fatalf("supersede revision %d: %+v %v", rev, op, err)
		}
	}
	if err := s.RecordApply(ctx, 99, ""); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("unknown apply: %v", err)
	}
}

func TestIdentityLimitRetainsDisabledUsers(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	seed := make([]node.User, 1024)
	for i := range seed {
		seed[i] = node.User{Name: fmt.Sprintf("user-%04d", i), UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)}
	}
	if _, err := s.Initialize(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(ctx, node.UserChange{Key: "disable", Name: seed[0].Name, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Change(ctx, node.UserChange{Key: "over-limit", Name: "extra", UUID: testUUID, Enabled: true, ExpectedRevision: 2}); !errors.Is(err, node.ErrInvalid) {
		t.Fatalf("identity limit: %v", err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || snap.Revision != 2 || len(snap.Users) != 1024 || snap.Users[0].Enabled {
		t.Fatalf("identity retention: revision=%d count=%d error=%v", snap.Revision, len(snap.Users), err)
	}
}

func TestMigrationsAreExplicitAndChecked(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, nil); !errors.Is(err, node.ErrUnavailable) {
		t.Fatalf("unmigrated store: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Initialize(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE node_schema_migrations SET checksum=$1", []byte("changed migration")); err != nil {
		t.Fatal("alter owned migration fixture")
	}
	if err := s.Migrate(ctx); !errors.Is(err, node.ErrUnavailable) {
		t.Fatalf("changed migration accepted: %v", err)
	}
}

func TestOperationWriteFailureRollsBackDesiredState(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Initialize(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// This constraint fails after the user insert and revision update, exercising
	// an actual PostgreSQL transaction rollback rather than input validation.
	if _, err := s.pool.Exec(ctx, "ALTER TABLE node_user_operations ADD CONSTRAINT reject_fixture CHECK (idempotency_key <> 'reject-fixture')"); err != nil {
		t.Fatal("install owned failure fixture")
	}
	change := node.UserChange{Key: "reject-fixture", Name: "alice", UUID: testUUID, Enabled: true, ExpectedRevision: 1}
	if _, err := s.Change(ctx, change); !errors.Is(err, node.ErrInvalid) {
		t.Fatalf("write failure: %v", err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || snap.Revision != 1 || len(snap.Users) != 0 {
		t.Fatalf("partial failed transaction: revision=%d count=%d error=%v", snap.Revision, len(snap.Users), err)
	}
	if _, err := s.Operation(ctx, 2); !errors.Is(err, node.ErrNotFound) {
		t.Fatalf("operation survived rollback: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE node_user_operations DROP CONSTRAINT reject_fixture"); err != nil {
		t.Fatal("remove owned failure fixture")
	}
	if op, err := s.Change(ctx, change); err != nil || op.Revision != 2 {
		t.Fatalf("retry failed transaction: %+v %v", op, err)
	}
}

func TestConcurrentInitializationOnlySeedsOnce(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	snapshots := make(chan node.UserSnapshot, 8)
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, err := s.Initialize(ctx, []node.User{{Name: fmt.Sprintf("seed-%d", i), UUID: testUUID}})
			snapshots <- snap
			errs <- err
		}()
	}
	wg.Wait()
	close(snapshots)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	name := ""
	for snap := range snapshots {
		if snap.Revision != 1 || len(snap.Users) != 1 {
			t.Fatalf("invalid initialization revision=%d users=%d", snap.Revision, len(snap.Users))
		}
		if name == "" {
			name = snap.Users[0].Name
		}
		if snap.Users[0].Name != name {
			t.Error("concurrent initializer replaced seed")
		}
	}
}
