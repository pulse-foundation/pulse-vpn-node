package traffic

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExportSnapshotNeverReadsDatabase(t *testing.T) {
	activity := time.Date(2026, 10, 30, 12, 0, 2, 0, time.UTC)
	store := &memoryStore{committed: Totals{Users: []UserTotal{{Name: "alice", UploadBytes: 9007199254740993, LastActivityAt: &activity}}}}
	c, err := NewCollector(sourceFunc(func(context.Context) ([]Connection, error) { return nil, nil }), store)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExportSnapshot() != nil {
		t.Fatal("invented initial traffic")
	}
	c.step(context.Background())
	store.readFail = true
	first := c.ExportSnapshot()
	if first == nil || first.Users[0].UploadBytes != 9007199254740993 {
		t.Fatal("no cached durable totals")
	}
	first.Users[0].UploadBytes = 0
	if c.ExportSnapshot().Users[0].UploadBytes != 9007199254740993 {
		t.Fatal("caller mutated cache")
	}
	*first.Users[0].LastActivityAt = time.Time{}
	if got := c.ExportSnapshot().Users[0].LastActivityAt; got == nil || got.IsZero() {
		t.Fatal("caller mutated cached activity timestamp")
	}
	c.source = sourceFunc(func(context.Context) ([]Connection, error) { return nil, errors.New("unavailable") })
	c.step(context.Background())
	if c.ExportSnapshot() != nil {
		t.Fatal("database outage must omit snapshot, not break heartbeat")
	}
}
