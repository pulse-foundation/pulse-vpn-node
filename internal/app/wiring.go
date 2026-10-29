package app

import (
	"context"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/mihomo"
)

// Run selects the installed adapter at the composition boundary. Adding another
// core changes this wiring and its packaging/settings, not the serving lifecycle.
func Run(ctx context.Context, cfg config.Config) error {
	return run(ctx, cfg, mihomo.NewAdapter(cfg.Binary, cfg.RuntimeDir))
}
