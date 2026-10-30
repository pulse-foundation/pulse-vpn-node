package node

import "context"

// Core starts a runtime from validated, core-independent VPN settings. Nil settings
// mean idle operation. An unsupported configuration must fail, never silently
// downgrade transport or security. Implementations return only sanitized errors.
type Core interface {
	// Start uses ctx for startup only. On error it must clean up partial resources
	// and return a nil runtime. On success the caller owns Runtime.Stop.
	// Settings are borrowed for this call; implementations must not mutate them
	// or retain mutable references after returning.
	Start(ctx context.Context, settings *VLESS) (Runtime, error)
}

// Runtime is one owned core instance. Adapter-specific controller protocols,
// configuration formats and process details stay behind this contract.
type Runtime interface {
	// Ready returns the observed, adapter-validated version after verifying local
	// runtime readiness. It must honor ctx and be safe for concurrent API calls.
	// It does not assert end-to-end VPN connectivity.
	Ready(ctx context.Context) (string, error)
	// Done closes once the instance exits and owned child resources are reaped.
	// Every unrequested exit, even with status zero, is a serving failure.
	Done() <-chan struct{}
	// Stop shuts down and reaps the instance. It is safe to call after Done.
	// The caller invokes it once with a fresh bounded context. After its deadline,
	// an adapter may use a short bounded force-stop/reap grace period.
	Stop(ctx context.Context) error
}
