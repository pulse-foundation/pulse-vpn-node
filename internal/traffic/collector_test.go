package traffic

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu        sync.Mutex
	sequence  int64
	samples   []Snapshot
	startFail bool
	writeFail bool
	readFail  bool
	committed Totals
}

func (s *memoryStore) StartTraffic(context.Context, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startFail {
		return 0, errors.New("private database detail")
	}
	return s.sequence, nil
}
func (s *memoryStore) ApplyTraffic(_ context.Context, sample Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
	if s.writeFail {
		return errors.New("private write detail")
	}
	s.sequence = sample.Sequence
	s.committed.ObservedAt = &sample.ObservedAt
	return nil
}
func (s *memoryStore) TrafficTotals(context.Context) (Totals, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFail {
		return Totals{}, errors.New("private database detail")
	}
	return s.committed, nil
}

type sourceFunc func(context.Context) ([]Connection, error)

func (f sourceFunc) Traffic(ctx context.Context) ([]Connection, error) { return f(ctx) }

func TestCollectorRetriesPersistenceWithCumulativeCounters(t *testing.T) {
	store := &memoryStore{startFail: true}
	var calls int
	source := sourceFunc(func(context.Context) ([]Connection, error) {
		calls++
		return []Connection{{ID: "one", User: "alice", Upload: int64(calls * 1000)}}, nil
	})
	c, err := NewCollector(source, store)
	if err != nil {
		t.Fatal(err)
	}
	c.step(context.Background())
	if calls != 0 {
		t.Fatal("source polled before generation registered")
	}
	store.startFail = false
	store.writeFail = true
	c.step(context.Background())
	v, err := c.View(context.Background())
	if err != nil || v.State != "degraded" || !v.Stale || strings.Contains(v.Error, "private") {
		t.Fatal("failed collection not safely exposed")
	}
	store.writeFail = false
	c.step(context.Background())
	if len(store.samples) != 2 || store.samples[1].Connections[0].Upload != 2000 || store.samples[1].Sequence <= store.samples[0].Sequence || store.samples[1].Generation != store.samples[0].Generation {
		t.Fatal("retry must use ordered cumulative observations in same generation")
	}
	v, err = c.View(context.Background())
	if err != nil || v.State != "collecting" || v.Stale || v.ObservedAt == nil {
		t.Fatal("successful collection unavailable")
	}
	for range 3 {
		if _, err = c.View(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("API read triggered collection")
	}
}

func TestCollectorSourceFailurePreservesLastCommittedTime(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	store := &memoryStore{sequence: 10, committed: Totals{ObservedAt: &old}}
	c, err := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) { return nil, errors.New("secret source detail") }), store)
	if err != nil {
		t.Fatal(err)
	}
	c.step(context.Background())
	v, err := c.View(context.Background())
	if err != nil || !v.Stale || v.State != "degraded" || !v.ObservedAt.Equal(old) || len(store.samples) != 0 {
		t.Fatal("source failure altered durable snapshot")
	}
	store.readFail = true
	if _, err = c.View(context.Background()); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("database failure must be sanitized and not return zeros")
	}
}

func TestCollectorLifetimeAndConcurrentReads(t *testing.T) {
	store := &memoryStore{}
	entered := make(chan struct{})
	c, err := NewCollector(sourceFunc(func(ctx context.Context) ([]Connection, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	for range 100 {
		if _, err := c.View(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collector failed to join")
	}
}

func TestNewCoreGetsDistinctGeneration(t *testing.T) {
	a, _ := NewCollector(nil, nil)
	b, _ := NewCollector(nil, nil)
	if a.generation == b.generation || len(a.generation) != 32 {
		t.Fatal("generation is not unique")
	}
}
