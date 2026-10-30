package app

import (
	"context"
	"errors"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/mihomo"
	"github.com/pulse-foundation/pulse-vpn-node/internal/postgres"
)

// Run selects the installed adapter at the composition boundary. Adding another
// core changes this wiring and its packaging/settings, not the serving lifecycle.
func Run(ctx context.Context, cfg config.Config) error {
	adapter := mihomo.NewAdapter(cfg.Binary, cfg.RuntimeDir)
	if cfg.DatabaseURL == "" {
		return run(ctx, cfg, adapter)
	}
	if cfg.VLESS == nil {
		return errors.New("user management requires VLESS settings")
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	store, err := postgres.Open(startup, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	snapshot, err := store.Initialize(startup, cfg.VLESS.Users)
	if err != nil {
		return err
	}
	vpn := *cfg.VLESS
	vpn.Users = snapshot.Active()
	cfg.VLESS = &vpn
	return runManaged(ctx, cfg, adapter, store)
}

func Migrate(ctx context.Context, cfg config.Config) error {
	if cfg.DatabaseURL == "" {
		return errors.New("database URL file is required for migrations")
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Migrate(ctx)
}
