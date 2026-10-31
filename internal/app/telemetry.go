package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

type databasePinger interface{ Ping(context.Context) error }

func startTelemetry(ctx context.Context, cfg config.Telemetry, core node.Runtime, manager *node.UserManager, store node.UserStore, agentStart, coreStart time.Time) (*telemetry.Buffer, func(), error) {
	buffer, err := telemetry.NewBuffer()
	if err != nil {
		return nil, nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	start := func(interval, timeout time.Duration, sampler telemetry.Sampler) {
		workers.Go(func() {
			for workerCtx.Err() == nil {
				attempt, stop := context.WithTimeout(workerCtx, timeout)
				sample := sampler.Sample(attempt)
				stop()
				if workerCtx.Err() != nil {
					return
				}
				if err := buffer.Publish(sample); err != nil {
					// Preserve a bounded failed observation rather than losing a sequence
					// silently if a sampler violates its output contract.
					_ = buffer.Publish(telemetry.Sample{Kind: sample.Kind, ObservedAt: time.Now().UTC(), Metrics: []telemetry.Metric{{Name: "collection", Scope: "process", Unit: "state", Available: false, Reason: "invalid_sample", ObservedAt: time.Now().UTC()}}})
				}
				timer := time.NewTimer(interval)
				select {
				case <-workerCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		})
	}
	interval := time.Duration(cfg.SampleIntervalSeconds) * time.Second
	start(interval, 3*time.Second, telemetry.NewResources(cfg.Resources))
	start(time.Duration(cfg.ProbeIntervalSeconds)*time.Second, 21*time.Second, telemetry.NewProbes(cfg.Probes))
	start(interval, 3*time.Second, &runtimeSampler{core: core, manager: manager, store: store, agentStart: agentStart, coreStart: coreStart})
	return buffer, func() { cancel(); workers.Wait() }, nil
}

type runtimeSampler struct {
	core                  node.Runtime
	manager               *node.UserManager
	store                 node.UserStore
	agentStart, coreStart time.Time
}

func (s *runtimeSampler) Sample(ctx context.Context) telemetry.Sample {
	now := time.Now()
	sample := telemetry.Sample{Kind: "runtime", ObservedAt: now.UTC(), Runtime: &telemetry.RuntimeInfo{CoreState: "unavailable", UserState: "not_configured"}}
	gauge := func(name, unit string, value float64) {
		sample.Metrics = append(sample.Metrics, telemetry.Metric{Name: name, Scope: "process", Unit: unit, Available: true, Value: &value, ObservedAt: time.Now().UTC()})
	}
	missing := func(name, unit, reason string) {
		sample.Metrics = append(sample.Metrics, telemetry.Metric{Name: name, Scope: "process", Unit: unit, Reason: reason, ObservedAt: time.Now().UTC()})
	}
	gauge("agent.uptime", "seconds", now.Sub(s.agentStart).Seconds())
	gauge("core.uptime", "seconds", now.Sub(s.coreStart).Seconds())
	readyCtx, cancel := context.WithTimeout(ctx, time.Second)
	version, err := s.core.Ready(readyCtx)
	cancel()
	if err == nil {
		sample.Runtime.CoreState = "ready"
		sample.Runtime.CoreVersion = version
		gauge("core.ready", "boolean", 1)
	} else {
		sample.Runtime.CoreState = "degraded"
		gauge("core.ready", "boolean", 0)
	}
	if source, ok := s.core.(traffic.Source); ok {
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		connections, err := source.Traffic(readCtx)
		cancel()
		if err == nil {
			gauge("core.active_connections", "count", float64(len(connections)))
		} else {
			missing("core.active_connections", "count", "unavailable")
		}
	} else {
		missing("core.active_connections", "count", "unsupported")
	}
	if s.manager != nil {
		status := s.manager.Status()
		v := uint64(status.AppliedRevision)
		sample.Metrics = append(sample.Metrics, telemetry.Metric{Name: "users.applied_revision", Scope: "process", Unit: "revision", Counter: &v, Available: true, ObservedAt: time.Now().UTC()})
		sample.Runtime.UserState = status.CleanupState
		if status.Error != "" {
			sample.Runtime.UserState = "degraded"
		}
	}
	if db, ok := s.store.(databasePinger); ok {
		dbCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := db.Ping(dbCtx)
		cancel()
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			missing("database.reachable", "boolean", "cancelled")
		} else if err != nil {
			gauge("database.reachable", "boolean", 0)
		} else {
			gauge("database.reachable", "boolean", 1)
		}
	} else {
		missing("database.reachable", "boolean", "not_configured")
	}
	return sample
}
