package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
)

func TestTelemetryResourcesDoNotWaitForCoreAndWorkersJoin(t *testing.T) {
	started := make(chan struct{}, 1)
	core := &testRuntime{done: make(chan struct{}), ready: func(ctx context.Context) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", errors.New("private details")
	}}
	cfg := config.DefaultTelemetry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer, stop, err := startTelemetry(ctx, cfg, core, nil, nil, time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runtime sampling never started")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for buffer.Latest("system", time.Second).Sample == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if buffer.Latest("system", time.Second).Sample == nil {
		t.Fatal("resource collection waited for core observation")
	}
	if p, err := buffer.Page("", 0, 10); err != nil || len(p.Records) == 0 {
		t.Fatal("buffer unavailable during core outage")
	}
	cancel()
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("telemetry workers did not join")
	}
}
