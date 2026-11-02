package traffic

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestCollectorFirstLiveObservationHasUnknownRates(t *testing.T) {
	store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}}}}
	c, err := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) {
		return []Connection{{ID: "one", User: "alice", Upload: 1000}}, nil
	}), store)
	if err != nil {
		t.Fatal(err)
	}
	c.step(context.Background())
	view := c.ExportSnapshot()
	if view == nil {
		t.Fatal("missing committed snapshot")
	}
	raw, err := json.Marshal(view.Users[0])
	if err != nil {
		t.Fatal(err)
	}
	var user map[string]json.RawMessage
	if err := json.Unmarshal(raw, &user); err != nil {
		t.Fatal(err)
	}
	var live map[string]json.RawMessage
	if err := json.Unmarshal(user["live"], &live); err != nil || live == nil {
		t.Fatalf("missing first live observation: %s", raw)
	}
	if string(live["activeConnections"]) != "1" || string(live["uploadBytesPerSecond"]) != "null" || string(live["downloadBytesPerSecond"]) != "null" {
		t.Fatalf("first observation must count connections with unknown rates: %s", raw)
	}
}

func TestCollectorLiveRatesUseElapsedTimeAndTrackedConnections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}, {Name: "idle"}}}}
		connections := []Connection{{ID: "one", User: "alice", StartedAt: time.Now().Add(-time.Hour), Upload: 1000, Download: 2000}}
		c, _ := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) { return connections, nil }), store)
		c.step(context.Background())
		time.Sleep(4 * time.Second)
		connections[0].Upload = 1400
		connections[0].Download = 2800
		c.step(context.Background())
		got := c.ExportSnapshot()
		assertLive(t, got.Users[0].Live, 1, 100, 200)
		assertLive(t, got.Users[1].Live, 0, 0, 0)
		if !got.Users[0].Live.ObservedAt.Equal(time.Now()) {
			t.Fatal("observation timestamp changed")
		}
		*got.Users[0].Live.UploadBytesPerSecond = 999
		got.Users[0].Live.ObservedAt = time.Time{}
		*got.ObservedAt = time.Time{}
		again := c.ExportSnapshot()
		assertLive(t, again.Users[0].Live, 1, 100, 200)
		if again.ObservedAt.IsZero() || again.Users[0].Live.ObservedAt.IsZero() {
			t.Fatal("snapshot pointers leaked")
		}
		time.Sleep(11 * time.Second)
		if got := c.ExportSnapshot(); !got.Stale || got.Users[0].Live != nil {
			t.Fatal("stale export retains live measurements")
		}
		if got, err := c.View(context.Background()); err != nil || got.Users[0].Live != nil {
			t.Fatal("stale API view retains live measurements")
		}
	})
}

func TestCollectorLiveContinuity(t *testing.T) {
	for _, kind := range []string{"reset-upload", "reset-download", "changed-start", "old-new-connection", "disappeared", "gap", "source-failure", "write-failure", "read-failure", "replay", "backward-clock", "same-clock", "sequence-gap", "core-generation", "accounting-epoch"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}}}}
				connections := []Connection{{ID: "one", User: "alice", StartedAt: time.Now().Add(-time.Hour), Upload: 1000, Download: 2000}}
				failSource := false
				c, _ := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) {
					if failSource {
						return nil, errors.New("unavailable")
					}
					return connections, nil
				}), store)
				c.step(context.Background())
				time.Sleep(2 * time.Second)
				connections[0].Upload = 1200
				connections[0].Download = 2200
				switch kind {
				case "reset-upload":
					connections[0].Upload = 1
				case "reset-download":
					connections[0].Download = 1
				case "changed-start":
					connections[0].StartedAt = time.Now().Add(-time.Second)
				case "old-new-connection":
					connections = append(connections, Connection{ID: "old", User: "alice", StartedAt: time.Now().Add(-time.Hour), Upload: 5000})
				case "disappeared":
					connections = nil
				case "gap":
					time.Sleep(11 * time.Second)
				case "source-failure":
					failSource = true
				case "write-failure":
					store.writeFail = true
				case "read-failure":
					store.readFail = true
				case "replay":
					store.replay = true
				case "backward-clock":
					store.observationOffset = -3 * time.Second
				case "same-clock":
					store.observationOffset = -2 * time.Second
				case "core-generation":
					c.generation = "new-generation"
				case "accounting-epoch":
					store.committed.AccountingEpoch = "new-epoch"
				case "sequence-gap":
					store.committed.AccountingSequence++
				}
				c.step(context.Background())
				if kind == "source-failure" || kind == "write-failure" || kind == "read-failure" || kind == "replay" {
					if got := c.ExportSnapshot(); got != nil && got.Users[0].Live != nil {
						t.Fatal("failure/replay retained live")
					}
					failSource = false
					store.writeFail = false
					store.readFail = false
					store.replay = false
					time.Sleep(2 * time.Second)
					c.step(context.Background())
				}
				got := c.ExportSnapshot()
				if got == nil {
					t.Fatal("successful sample missing")
				}
				if got.Users[0].Live != nil && (got.Users[0].Live.UploadBytesPerSecond != nil || got.Users[0].Live.DownloadBytesPerSecond != nil) {
					t.Fatalf("%s fabricated a rate: %+v", kind, got.Users[0].Live)
				}
				time.Sleep(2 * time.Second)
				store.observationOffset = 0
				c.step(context.Background())
				assertLive(t, c.ExportSnapshot().Users[0].Live, len(connections), 0, 0)
			})
		})
	}
}

func TestCollectorLiveCountsNewConnectionsWithoutHistoricSpike(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}}}}
		var connections []Connection
		c, _ := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) { return connections, nil }), store)
		c.step(context.Background())
		time.Sleep(4 * time.Second)
		connections = []Connection{{ID: "new", User: "alice", StartedAt: time.Now().Add(-time.Second), Upload: 400, Download: 800}, {ID: "unknown", StartedAt: time.Now(), Upload: 9000}}
		c.step(context.Background())
		assertLive(t, c.ExportSnapshot().Users[0].Live, 1, 100, 200)
		// A concurrent durable snapshot from another collection cannot be paired with this live data.
		store.committed.AccountingSequence++
		if got, err := c.View(context.Background()); err != nil || got.Users[0].Live != nil {
			t.Fatal("mixed committed totals and live observation")
		}
	})
}

func assertLive(t *testing.T, got *Live, count int, up, down float64) {
	t.Helper()
	if got == nil || got.UploadBytesPerSecond == nil || got.DownloadBytesPerSecond == nil || got.ActiveConnections != count || *got.UploadBytesPerSecond != up || *got.DownloadBytesPerSecond != down {
		t.Fatalf("live = %+v; want connections=%d rates=%v/%v", got, count, up, down)
	}
}

func TestCollectorConcurrentLiveReadsAreIsolated(t *testing.T) {
	store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}}}}
	var counter int64
	c, _ := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) {
		counter++
		return []Connection{{ID: "one", User: "alice", StartedAt: time.Unix(1, 0), Upload: counter, Download: counter}}, nil
	}), store)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				view, err := c.View(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				views := []*View{&view, c.ExportSnapshot()}
				for _, v := range views {
					if v != nil {
						for _, u := range v.Users {
							if u.Live != nil {
								u.Live.ObservedAt = time.Time{}
								if u.Live.UploadBytesPerSecond != nil {
									*u.Live.UploadBytesPerSecond = 999
								}
							}
						}
					}
				}
			}
		}()
	}
	for range 50 {
		c.step(context.Background())
	}
	close(done)
	wg.Wait()
	if got := c.ExportSnapshot(); got == nil || got.Users[0].Live == nil || got.Users[0].Live.ObservedAt.IsZero() {
		t.Fatal("read mutated live snapshot")
	}
}

func TestCollectorDoesNotInventIdleObservationForNewlyAddedUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}}}}
		c, _ := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) { return nil, nil }), store)
		c.step(context.Background())
		time.Sleep(2 * time.Second)
		c.step(context.Background())
		store.committed.Users = append(store.committed.Users, UserTotal{Name: "new-user"})
		got, err := c.View(context.Background())
		if err != nil || got.Users[1].Live != nil {
			t.Fatal("user created after observation reported as observed idle")
		}
		time.Sleep(2 * time.Second)
		c.step(context.Background())
		if got := c.ExportSnapshot().Users[1].Live; got == nil || got.UploadBytesPerSecond != nil || got.DownloadBytesPerSecond != nil {
			t.Fatal("new user's first observation fabricated rates")
		}
	})
}

func TestCollectorSkippedAccountingPreservesCountWithUnknownRates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice"}, {Name: "bob"}}}}
		connections := []Connection{{ID: "one", User: "alice", StartedAt: time.Now().Add(-time.Hour), Upload: 100}, {ID: "two", User: "bob", StartedAt: time.Now().Add(-time.Hour), Upload: 100}}
		c, _ := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) { return connections, nil }), store)
		c.step(context.Background())
		time.Sleep(2 * time.Second)
		connections[0].Upload = 200
		connections[1].Upload = 300
		store.unrated = []string{"alice"}
		c.step(context.Background())
		got := c.ExportSnapshot()
		if live := got.Users[0].Live; live == nil || live.ActiveConnections != 1 || live.UploadBytesPerSecond != nil || live.DownloadBytesPerSecond != nil {
			t.Fatal("skipped accounting fabricated rates or lost observed count")
		}
		assertLive(t, got.Users[1].Live, 1, 100, 0)
		store.unrated = nil
		time.Sleep(2 * time.Second)
		c.step(context.Background())
		if got := c.ExportSnapshot().Users[0].Live; got == nil || got.UploadBytesPerSecond != nil {
			t.Fatal("skipped previous baseline fabricated rate")
		}
		time.Sleep(2 * time.Second)
		c.step(context.Background())
		assertLive(t, c.ExportSnapshot().Users[0].Live, 1, 0, 0)
	})
}
