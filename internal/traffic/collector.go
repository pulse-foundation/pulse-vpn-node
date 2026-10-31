package traffic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"
)

type View struct {
	Totals
	Approximate         bool   `json:"approximate"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
	State               string `json:"state"`
	Stale               bool   `json:"stale"`
	Error               string `json:"error,omitempty"`
}

// Collector has one application-owned Run loop. Persistence owns every counter
// baseline; failed or ambiguous commits never advance an in-memory byte total.
type Collector struct {
	source      Source
	store       Store
	generation  string
	sequence    int64
	initialized bool
	mu          sync.RWMutex
	state       string
	exported    *View
}

// NewCollector must follow a confirmed new owned child start, never an unverified
// adoption or an agent-only restart of the same child. No PID is used as identity.
func NewCollector(source Source, store Store) (*Collector, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.New("cannot initialize traffic generation")
	}
	return &Collector{source: source, store: store, generation: hex.EncodeToString(nonce[:]), state: "starting"}, nil
}

func (c *Collector) Run(ctx context.Context) {
	for ctx.Err() == nil {
		c.step(ctx)
		timer := time.NewTimer(PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Collector) step(ctx context.Context) {
	err := c.collect(ctx)
	c.mu.Lock()
	c.state = "collecting"
	if err != nil {
		c.state = "degraded"
	}
	c.mu.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	view, readErr := c.View(readCtx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exported = nil
	if readErr == nil && view.ObservedAt != nil {
		c.exported = &view
	}
}

// ExportSnapshot is a cached, bounded read: database failure cannot delay the
// independent heartbeat loop. The observation time remains the last committed time.
func (c *Collector) ExportSnapshot() *View {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.exported == nil {
		return nil
	}
	copy := *c.exported
	copy.Users = append([]UserTotal{}, copy.Users...)
	copy.Stale = copy.ObservedAt == nil || time.Since(*copy.ObservedAt) > 5*PollInterval
	return &copy
}

func (c *Collector) collect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if !c.initialized {
		sequence, err := c.store.StartTraffic(ctx, c.generation)
		if err != nil {
			return err
		}
		c.sequence = sequence
		c.initialized = true
	}
	readCtx, cancelRead := context.WithTimeout(ctx, 3*time.Second)
	connections, err := c.source.Traffic(readCtx)
	cancelRead()
	if err != nil {
		return err
	}
	if c.sequence == math.MaxInt64 || len(connections) > MaxConnections {
		return errors.New("traffic observation limit exceeded")
	}
	c.sequence++
	return c.store.ApplyTraffic(ctx, Snapshot{Generation: c.generation, Sequence: c.sequence, ObservedAt: time.Now().UTC(), Connections: connections})
}

// View reads bounded durable totals, without contacting the core. An unavailable
// database is an error, never a synthetic zero. Collection faults leave VPN service running.
func (c *Collector) View(ctx context.Context) (View, error) {
	totals, err := c.store.TrafficTotals(ctx)
	if err != nil {
		return View{}, errors.New("traffic statistics unavailable")
	}
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	view := View{Totals: totals, Approximate: true, PollIntervalSeconds: int(PollInterval / time.Second), State: state,
		Stale: totals.ObservedAt == nil || time.Since(*totals.ObservedAt) > 5*PollInterval}
	if state == "degraded" {
		view.Error = "traffic collection unavailable"
	}
	return view, nil
}
