package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

// Inspect the public representation as well as persistence, so missing fields
// and omitted nulls cannot look like an unknown timestamp.
func assertActivity(t *testing.T, s *Store, want string) {
	t.Helper()
	totals, err := s.TrafficTotals(context.Background())
	if err != nil || len(totals.Users) != 1 {
		t.Fatalf("read activity: %v", err)
	}
	data, err := json.Marshal(totals.Users[0])
	if err != nil {
		t.Fatal(err)
	}
	var user map[string]json.RawMessage
	if err = json.Unmarshal(data, &user); err != nil {
		t.Fatal(err)
	}
	value, ok := user["lastActivityAt"]
	if !ok {
		t.Fatal("missing lastActivityAt")
	}
	if want == "" {
		if string(value) != "null" {
			t.Fatalf("want unknown activity, got %s", value)
		}
		return
	}
	var got time.Time
	if err = json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	expected, err := time.Parse(time.RFC3339, want)
	if err != nil || !got.Equal(expected) {
		t.Fatalf("activity = %s, want %s", value, want)
	}
}

func TestTrafficLastActivityRequiresPositiveDelta(t *testing.T) {
	s, _ := trafficStore(t)
	assertActivity(t, s, "")
	applySample(t, s, sample(1, 0, 0))
	assertActivity(t, s, "")
	applySample(t, s, sample(2, 10, 0))
	assertActivity(t, s, "2026-10-30T12:00:02Z")
	applySample(t, s, sample(3, 10, 0)) // idle
	assertActivity(t, s, "2026-10-30T12:00:02Z")
	applySample(t, s, sample(4, 10, 20)) // download only
	assertActivity(t, s, "2026-10-30T12:00:04Z")
	applySample(t, s, sample(3, 999, 999)) // stale
	applySample(t, s, sample(4, 999, 999)) // duplicate
	assertActivity(t, s, "2026-10-30T12:00:04Z")
	backward := sample(5, 20, 30)
	backward.ObservedAt = trafficTime.Add(time.Second)
	applySample(t, s, backward)
	assertActivity(t, s, "2026-10-30T12:00:04Z")
	applySample(t, s, sample(6, 0, 0)) // counter reset alone
	assertActivity(t, s, "2026-10-30T12:00:04Z")
	changed := sample(7, 999, 999)
	changed.Connections[0].StartedAt = trafficTime.Add(time.Second)
	applySample(t, s, changed) // uncertain continuity, rebaseline
	assertActivity(t, s, "2026-10-30T12:00:04Z")
	changed.Sequence = 8
	changed.ObservedAt = trafficTime.Add(8 * time.Second)
	changed.Connections[0].Download++
	applySample(t, s, changed)
	assertActivity(t, s, "2026-10-30T12:00:08Z")
}

func TestTrafficLastActivitySurvivesPruningRestartAndBackup(t *testing.T) {
	s, dsn := trafficStore(t)
	ctx := context.Background()
	applySample(t, s, sample(1, 10, 20)) // first observation counts
	empty := sample(2, 0, 0)
	empty.ObservedAt = trafficTime.Add(25 * time.Hour)
	empty.Connections = nil
	applySample(t, s, empty)
	assertActivity(t, s, "2026-10-30T12:00:01Z")
	s.Close()
	reopened, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if _, err = reopened.StartTraffic(ctx, "core-2"); err != nil {
		t.Fatal(err)
	}
	assertActivity(t, reopened, "2026-10-30T12:00:01Z")
	backup, err := reopened.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testStore(t)
	if _, err = target.RestoreBackup(ctx, backup, nil); err != nil {
		t.Fatal(err)
	}
	assertActivity(t, target, "2026-10-30T12:00:01Z")
}

func TestTrafficActivityMigrationPreservesExistingTotals(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `CREATE TABLE node_schema_migrations(version integer PRIMARY KEY,checksum bytea NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for i, file := range schemaMigrationFiles[:6] {
		raw, err := migrationFiles.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if _, err = s.pool.Exec(ctx, "INSERT INTO node_schema_migrations(version,checksum) VALUES($1,$2)", i+1, sum[:]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Initialize(ctx, []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "INSERT INTO node_traffic_totals(user_name,upload_bytes,download_bytes) VALUES('alice',100,200)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertTraffic(t, s, 100, 200, 0, 0)
	assertActivity(t, s, "") // historic totals cannot establish a timestamp
}

func TestTrafficActivityRestorePreviousSchema(t *testing.T) {
	source, _ := trafficStore(t)
	ctx := context.Background()
	applySample(t, source, sample(1, 100, 200))
	backup, err := source.ExportBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Version 6 differs only by the absent nullable activity column.
	backup.SchemaVersion = 6
	backup.TrafficTotals = json.RawMessage(`[{"user_name":"alice","upload_bytes":100,"download_bytes":200}]`)
	target, _ := testStore(t)
	if _, err = target.RestoreBackup(ctx, backup, nil); err != nil {
		t.Fatal(err)
	}
	assertTraffic(t, target, 100, 200, 1, 0) // restore starts a new accounting baseline
	assertActivity(t, target, "")
	for _, version := range []int{5, 8} {
		backup.SchemaVersion = version
		if _, err = target.RestoreBackup(ctx, backup, nil); err == nil {
			t.Fatalf("unsupported backup schema %d accepted", version)
		}
	}
}

func TestTrafficLastActivityIsAttributedPerUser(t *testing.T) {
	s, _ := trafficStore(t)
	ctx := context.Background()
	if _, err := s.Change(ctx, node.UserChange{Key: "add-bob", Name: "bob", UUID: "b47ac10b-58cc-4372-a567-0e02b2c3d479", Enabled: true, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bobRuntime string
	for _, user := range state.Active() {
		if user.Name != "alice" {
			bobRuntime = user.Name
		}
	}
	v := sample(1, 0, 0)
	v.Connections = append(v.Connections, traffic.Connection{ID: "bob", User: bobRuntime, StartedAt: trafficTime, Download: 10})
	applySample(t, s, v)
	v = sample(2, 0, 0)
	v.Connections = append(v.Connections, traffic.Connection{ID: "unknown", User: "unknown", StartedAt: trafficTime, Upload: 100})
	applySample(t, s, v)
	totals, err := s.TrafficTotals(ctx)
	if err != nil || len(totals.Users) != 2 || totals.SkippedConnections != 1 {
		t.Fatalf("attribution: %+v %v", totals, err)
	}
	if totals.Users[0].Name != "alice" || totals.Users[0].LastActivityAt != nil {
		t.Fatal("idle user acquired another user's activity")
	}
	if totals.Users[1].Name != "bob" || totals.Users[1].LastActivityAt == nil || !totals.Users[1].LastActivityAt.Equal(trafficTime.Add(time.Second)) {
		t.Fatal("active user's activity lost or advanced by unknown traffic")
	}
}
