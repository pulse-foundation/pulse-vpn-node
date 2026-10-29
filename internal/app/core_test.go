package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

type testCore struct {
	runtime  *testRuntime
	err      error
	settings *node.VLESS
}

func (c *testCore) Start(_ context.Context, settings *node.VLESS) (node.Runtime, error) {
	c.settings = settings
	if c.err != nil {
		return nil, c.err
	}
	return c.runtime, nil
}

type testRuntime struct {
	done    chan struct{}
	ready   func(context.Context) (string, error)
	stopped bool
}

func (r *testRuntime) Ready(ctx context.Context) (string, error) { return r.ready(ctx) }
func (r *testRuntime) Done() <-chan struct{}                     { return r.done }
func (r *testRuntime) Stop(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("shutdown context already cancelled")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("shutdown must be bounded")
	}
	r.stopped = true
	return nil
}

func TestRunUsesIndependentCoreAndStopsIt(t *testing.T) {
	for _, scenario := range []string{"cancel serving", "exit serving", "cancel startup", "exit startup"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime := &testRuntime{done: make(chan struct{})}
			runtime.ready = func(context.Context) (string, error) {
				if strings.HasPrefix(scenario, "cancel") {
					cancel()
				} else {
					close(runtime.done)
				}
				if strings.HasSuffix(scenario, "startup") {
					return "", errors.New("not ready")
				}
				return "independent-core-v2", nil
			}
			core := &testCore{runtime: runtime}
			settings := &node.VLESS{}
			err := run(ctx, config.Config{Listen: "127.0.0.1:0", Token: strings.Repeat("x", 64), VLESS: settings}, core)
			if (err == nil) != (scenario == "cancel serving") {
				t.Fatalf("unexpected result: %v", err)
			}
			if !runtime.stopped {
				t.Fatal("runtime was not stopped")
			}
			if core.settings != settings {
				t.Fatal("VPN settings not passed to selected core")
			}
		})
	}
}

func TestRunPropagatesAdapterStartupFailure(t *testing.T) {
	want := errors.New("adapter failed")
	core := &testCore{err: want}
	err := run(context.Background(), config.Config{Listen: "127.0.0.1:0"}, core)
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitReadyReturnsSelectedCoreVersion(t *testing.T) {
	runtime := &testRuntime{done: make(chan struct{}), ready: func(context.Context) (string, error) { return "independent-core-v2", nil }}
	version, err := waitReady(context.Background(), runtime)
	if err != nil || version != "independent-core-v2" {
		t.Fatalf("got %q, %v", version, err)
	}
}
