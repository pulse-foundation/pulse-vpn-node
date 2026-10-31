package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

var trafficTime = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func trafficStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, dsn := testStore(t)
	if _, err := s.Initialize(context.Background(), []node.User{{Name: "alice", UUID: testUUID}}); err != nil {
		t.Fatal(err)
	}
	if seq, err := s.StartTraffic(context.Background(), "core-1"); err != nil || seq != 0 {
		t.Fatalf("start: %d %v", seq, err)
	}
	return s, dsn
}
func sample(sequence, upload, download int64) traffic.Snapshot {
	return traffic.Snapshot{Generation: "core-1", Sequence: sequence, ObservedAt: trafficTime.Add(time.Duration(sequence) * time.Second), Connections: []traffic.Connection{{ID: "connection-1", User: "alice", StartedAt: trafficTime, Upload: upload, Download: download}}}
}
func applySample(t *testing.T, s *Store, v traffic.Snapshot) {
	t.Helper()
	if err := s.ApplyTraffic(context.Background(), v); err != nil {
		t.Fatal(err)
	}
}
func assertTraffic(t *testing.T, s *Store, up, down, resets, skipped int64) traffic.Totals {
	t.Helper()
	got, err := s.TrafficTotals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Users) != 1 || got.Users[0].UploadBytes != up || got.Users[0].DownloadBytes != down || got.ResetCount != resets || got.SkippedConnections != skipped {
		t.Fatalf("traffic: %+v", got)
	}
	return got
}

func TestTrafficReplayOrderingAndReopen(t *testing.T) {
	s, dsn := trafficStore(t)
	ctx := context.Background()
	if got := assertTraffic(t, s, 0, 0, 0, 0); got.ObservedAt != nil {
		t.Fatal("observation exists before sample")
	}
	applySample(t, s, sample(1, 1000, 2000))
	applySample(t, s, sample(3, 2000, 4000))
	applySample(t, s, sample(2, 1500, 3000))
	applySample(t, s, sample(3, 2000, 4000))
	applySample(t, s, sample(4, 2000, 4000))
	assertTraffic(t, s, 2000, 4000, 0, 0)
	s.Close()
	reopened, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if seq, err := reopened.StartTraffic(ctx, "core-1"); err != nil || seq != 4 {
		t.Fatalf("resume: %d %v", seq, err)
	}
	applySample(t, reopened, sample(5, 2500, 4500))
	assertTraffic(t, reopened, 2500, 4500, 0, 0)
	// Fresh sequence distinguishes genuine reset from the stale sample above.
	applySample(t, reopened, sample(6, 100, 4700))
	applySample(t, reopened, sample(7, 150, 4800))
	assertTraffic(t, reopened, 2550, 4800, 1, 0)
	if _, err = reopened.StartTraffic(ctx, "core-2"); err != nil {
		t.Fatal(err)
	}
	if got := assertTraffic(t, reopened, 2550, 4800, 1, 0); got.ObservedAt != nil {
		t.Fatal("new generation retained observation")
	}
	if err = reopened.ApplyTraffic(ctx, sample(8, 9999, 9999)); !errors.Is(err, node.ErrConflict) {
		t.Fatalf("old generation: %v", err)
	}
	next := sample(1, 20, 30)
	next.Generation = "core-2"
	applySample(t, reopened, next)
	assertTraffic(t, reopened, 2570, 4830, 1, 0)
}

func TestTrafficDisabledAndUnknownAttribution(t *testing.T) {
	s, _ := trafficStore(t)
	if _, err := s.Change(context.Background(), node.UserChange{Key: "disable", Name: "alice", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	v := sample(1, 100, 200)
	v.Connections = append(v.Connections, traffic.Connection{ID: "unknown", User: "unknown", StartedAt: trafficTime, Upload: 999}, traffic.Connection{ID: "system", StartedAt: trafficTime, Upload: 999})
	applySample(t, s, v)
	if got := assertTraffic(t, s, 100, 200, 0, 2); got.Users[0].Enabled {
		t.Fatal("disabled identity lost")
	}
	empty := sample(2, 0, 0)
	empty.Connections = nil
	applySample(t, s, empty)
	assertTraffic(t, s, 100, 200, 0, 0)
	applySample(t, s, sample(3, 120, 230))
	assertTraffic(t, s, 120, 230, 0, 0)
}

func TestTrafficWriteFailureRollsBackCheckpointAndSequence(t *testing.T) {
	s, _ := trafficStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `ALTER TABLE node_traffic_state ADD CONSTRAINT reject_fixture CHECK(sequence<>1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyTraffic(ctx, sample(1, 1000, 2000)); err == nil {
		t.Fatal("injected write accepted")
	}
	assertTraffic(t, s, 0, 0, 0, 0)
	var count int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM node_traffic_checkpoints").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial checkpoint: %d %v", count, err)
	}
	if seq, err := s.StartTraffic(ctx, "core-1"); err != nil || seq != 0 {
		t.Fatalf("partial sequence: %d %v", seq, err)
	}
	if _, err := s.pool.Exec(ctx, "ALTER TABLE node_traffic_state DROP CONSTRAINT reject_fixture"); err != nil {
		t.Fatal(err)
	}
	applySample(t, s, sample(2, 1500, 2500))
	assertTraffic(t, s, 1500, 2500, 0, 0)
}

func TestTrafficConcurrentSnapshotsSerialize(t *testing.T) {
	s, _ := trafficStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := int64(1); i <= 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.ApplyTraffic(context.Background(), sample(i, i*100, i*200)) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertTraffic(t, s, 3200, 6400, 0, 0)
}

func TestTrafficPruningDoesNotRecountAndPreservesActiveOld(t *testing.T) {
	s, _ := trafficStore(t)
	v := sample(1, 100, 200)
	v.Connections = append(v.Connections, traffic.Connection{ID: "active", User: "alice", StartedAt: trafficTime, Upload: 10, Download: 20})
	applySample(t, s, v)
	v = sample(2, 0, 0)
	v.ObservedAt = trafficTime.Add(25 * time.Hour)
	v.Connections = []traffic.Connection{{ID: "active", User: "alice", StartedAt: trafficTime, Upload: 30, Download: 40}}
	applySample(t, s, v)
	assertTraffic(t, s, 130, 240, 0, 0)
	var count int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM node_traffic_checkpoints").Scan(&count); err != nil || count != 1 {
		t.Fatalf("prune count: %d %v", count, err)
	}
	v = sample(3, 200, 300)
	v.ObservedAt = trafficTime.Add(25*time.Hour + time.Second)
	applySample(t, s, v)
	assertTraffic(t, s, 130, 240, 0, 1)
	// A backward wall clock must not regress the durable pruning watermark.
	v = sample(4, 300, 400)
	applySample(t, s, v)
	assertTraffic(t, s, 130, 240, 0, 1)
}

func TestTrafficCapacityPreservesExistingCheckpoints(t *testing.T) {
	s, _ := trafficStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO node_traffic_checkpoints(connection_id,user_name,started_at,upload_bytes,download_bytes,last_seen) SELECT 'capacity-'||i,'alice',$1,10,20,$1 FROM generate_series(1,$2) i`, trafficTime, traffic.MaxCheckpoints); err != nil {
		t.Fatal(err)
	}
	v := sample(1, 100, 200)
	v.Connections = append(v.Connections, traffic.Connection{ID: "capacity-1", User: "alice", StartedAt: trafficTime, Upload: 15, Download: 27})
	applySample(t, s, v)
	assertTraffic(t, s, 5, 7, 0, 1)
	var count int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM node_traffic_checkpoints").Scan(&count); err != nil || count != traffic.MaxCheckpoints {
		t.Fatalf("capacity: %d %v", count, err)
	}
}

func TestTrafficOverflowIsAtomic(t *testing.T) {
	t.Run("per-user stored total", func(t *testing.T) {
		s, _ := trafficStore(t)
		applySample(t, s, sample(1, math.MaxInt64, 0))
		v := sample(2, 1, 1)
		v.Connections[0].ID = "other"
		if err := s.ApplyTraffic(context.Background(), v); !errors.Is(err, node.ErrUnavailable) {
			t.Fatalf("overflow: %v", err)
		}
		assertTraffic(t, s, math.MaxInt64, 0, 0, 0)
		if seq, err := s.StartTraffic(context.Background(), "core-1"); err != nil || seq != 1 {
			t.Fatalf("sequence: %d %v", seq, err)
		}
	})
	t.Run("sample sum", func(t *testing.T) {
		s, _ := trafficStore(t)
		v := sample(1, math.MaxInt64, 0)
		v.Connections = append(v.Connections, traffic.Connection{ID: "other", User: "alice", StartedAt: trafficTime, Upload: 1})
		if err := s.ApplyTraffic(context.Background(), v); !errors.Is(err, node.ErrUnavailable) {
			t.Fatalf("overflow: %v", err)
		}
		assertTraffic(t, s, 0, 0, 0, 0)
	})
	t.Run("reset diagnostic", func(t *testing.T) {
		s, _ := trafficStore(t)
		applySample(t, s, sample(1, 100, 200))
		if _, err := s.pool.Exec(context.Background(), "UPDATE node_traffic_state SET reset_count=$1", int64(math.MaxInt64)); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyTraffic(context.Background(), sample(2, 1, 2)); !errors.Is(err, node.ErrUnavailable) {
			t.Fatalf("overflow: %v", err)
		}
		assertTraffic(t, s, 100, 200, math.MaxInt64, 0)
	})
}

func TestTrafficInputValidationIsAtomic(t *testing.T) {
	cases := map[string]func(*traffic.Snapshot){"negative": func(v *traffic.Snapshot) { v.Connections[0].Upload = -1 }, "zero sequence": func(v *traffic.Snapshot) { v.Sequence = 0 }, "duplicate": func(v *traffic.Snapshot) {
		other := v.Connections[0]
		other.User = "bob"
		v.Connections = append(v.Connections, other)
	}, "missing time": func(v *traffic.Snapshot) { v.Connections[0].StartedAt = time.Time{} }, "future start": func(v *traffic.Snapshot) { v.Connections[0].StartedAt = v.ObservedAt.Add(time.Second) }, "too many": func(v *traffic.Snapshot) { v.Connections = make([]traffic.Connection, traffic.MaxConnections+1) }, "oversized identity": func(v *traffic.Snapshot) { v.Connections[0].ID = strings.Repeat("x", 129) }}
	s, _ := trafficStore(t)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := sample(1, 100, 200)
			mutate(&v)
			if err := s.ApplyTraffic(context.Background(), v); !errors.Is(err, node.ErrInvalid) {
				t.Fatalf("invalid accepted: %v", err)
			}
			assertTraffic(t, s, 0, 0, 0, 0)
		})
	}
}

// Drop the COMMIT acknowledgement after PostgreSQL has sent CommandComplete.
// The caller sees EOF but the real transaction has committed; no fake SQL store
// or application-only commit-error hook substitutes for this failure boundary.
type lostCommitConn struct {
	net.Conn
	armed    *atomic.Bool
	dropping atomic.Bool
}

func (c *lostCommitConn) Write(p []byte) (int, error) {
	if bytes.Contains(bytes.ToLower(p), []byte("commit")) && c.armed.CompareAndSwap(true, false) {
		c.dropping.Store(true)
	}
	return c.Conn.Write(p)
}
func (c *lostCommitConn) Read(p []byte) (int, error) {
	if !c.dropping.Load() {
		return c.Conn.Read(p)
	}
	var response []byte
	for {
		n, err := c.Conn.Read(p)
		response = append(response, p[:n]...)
		if bytes.Contains(response, []byte("COMMIT\x00")) {
			c.Conn.Close()
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		if len(response) > 4096 {
			c.Conn.Close()
			return 0, io.ErrUnexpectedEOF
		}
	}
}
func TestTrafficUncertainCommitReplay(t *testing.T) {
	s, dsn := trafficStore(t)
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse fixture")
	}
	// TLS framing would hide CommandComplete. This is a disposable local DB.
	cfg.ConnConfig.TLSConfig = nil
	cfg.ConnConfig.Fallbacks = nil
	cfg.MaxConns = 1
	var armed atomic.Bool
	dialer := net.Dialer{Timeout: 5 * time.Second}
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &lostCommitConn{Conn: conn, armed: &armed}, nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("open fault connection")
	}
	t.Cleanup(pool.Close)
	fault := &Store{pool: pool}
	if err = pool.Ping(ctx); err != nil {
		t.Fatal("connect fault fixture")
	}
	armed.Store(true)
	if err = fault.ApplyTraffic(ctx, sample(1, 1000, 2000)); !errors.Is(err, node.ErrUnavailable) {
		t.Fatalf("lost commit response: %v", err)
	}
	assertTraffic(t, s, 1000, 2000, 0, 0)
	applySample(t, fault, sample(1, 1000, 2000))
	applySample(t, fault, sample(2, 1500, 2500))
	assertTraffic(t, s, 1500, 2500, 0, 0)
}

func TestTrafficMigrationUpgradePreservesUsers(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	sql, err := migrationFiles.ReadFile("migrations/001_users.sql")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(sql)
	if _, err = s.pool.Exec(ctx, `CREATE TABLE node_schema_migrations(version integer PRIMARY KEY,checksum bytea NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO node_schema_migrations(version,checksum) VALUES(1,$1)`, sum[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "INSERT INTO node_users(name,uuid,enabled) VALUES('alice',$1,true)", testUUID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "UPDATE node_user_state SET initialized=true,revision=1 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "INSERT INTO node_user_operations(revision,state) VALUES(1,'applied')"); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertTraffic(t, s, 0, 0, 0, 0)
	var newest int
	if err = s.pool.QueryRow(ctx, "SELECT max(version) FROM node_schema_migrations").Scan(&newest); err != nil || newest != 5 {
		t.Fatalf("migration version %d %v", newest, err)
	}
}

func TestMigrationRejectsUnknownAndGappedHistory(t *testing.T) {
	for _, version := range []int{6, 0} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			s, _ := testStore(t)
			ctx := context.Background()
			if _, err := s.pool.Exec(ctx, "UPDATE node_schema_migrations SET version=$1 WHERE version=1", version); err != nil {
				t.Fatal(err)
			}
			if err := s.Migrate(ctx); !errors.Is(err, node.ErrUnavailable) {
				t.Fatalf("invalid history accepted: %v", err)
			}
		})
	}
}

func TestTrafficFirstObservationAndChangedStart(t *testing.T) {
	s, _ := trafficStore(t)
	// A long-running connection observed for the first time is countable until
	// pruning creates ambiguity. Merely passing the retention window is not loss.
	v := sample(1, 100, 200)
	v.ObservedAt = trafficTime.Add(25 * time.Hour)
	applySample(t, s, v)
	assertTraffic(t, s, 100, 200, 0, 0)
	v.Sequence = 2
	v.Connections[0].StartedAt = trafficTime.Add(time.Hour)
	v.Connections[0].Upload = 1000
	v.Connections[0].Download = 2000
	applySample(t, s, v)
	assertTraffic(t, s, 100, 200, 1, 0)
	v.Sequence = 3
	v.Connections[0].Upload = 1010
	v.Connections[0].Download = 2020
	applySample(t, s, v)
	assertTraffic(t, s, 110, 220, 1, 0)
}

func TestTrafficMaximumSampleUsesBoundedBatches(t *testing.T) {
	s, _ := trafficStore(t)
	v := sample(1, 0, 0)
	v.Connections = make([]traffic.Connection, traffic.MaxConnections)
	for i := range v.Connections {
		v.Connections[i] = traffic.Connection{ID: fmt.Sprintf("batch-%d", i), User: "alice", StartedAt: trafficTime, Upload: 1, Download: 2}
	}
	applySample(t, s, v)
	assertTraffic(t, s, traffic.MaxConnections, 2*traffic.MaxConnections, 0, 0)
	v.Sequence = 2
	applySample(t, s, v)
	assertTraffic(t, s, traffic.MaxConnections, 2*traffic.MaxConnections, 0, 0)
}
