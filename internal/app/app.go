package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/httpapi"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func run(ctx context.Context, cfg config.Config, adapter node.Core) error {
	return runManaged(ctx, cfg, adapter, nil)
}

func runManaged(ctx context.Context, cfg config.Config, adapter node.Core, store node.UserStore) error {
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("cannot bind management listener")
	}
	defer listener.Close()
	core, err := adapter.Start(ctx, cfg.VLESS)
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := core.Stop(stopCtx); err != nil {
			slog.Error("core shutdown failed", "error", err)
		}
	}()
	version, err := waitReady(ctx, core)
	if err != nil {
		return err
	}
	var manager *node.UserManager
	if store != nil {
		users, ok := core.(node.UserRuntime)
		if !ok {
			return errors.New("selected core does not support live users")
		}
		manager = node.NewUserManager(store, users)
		workerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); manager.Run(workerCtx) }()
		defer func() { cancel(); <-done }()
	}
	server := &http.Server{Handler: httpapi.NewWithUsers(cfg.Token, cfg.VLESS != nil, core.Ready, manager), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 * 1024}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	slog.Info("agent ready", "coreVersion", version, "vpnConfigured", cfg.VLESS != nil)
	var result error
	select {
	case <-ctx.Done():
	case <-core.Done():
		result = errors.New("core exited unexpectedly")
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("management server stopped unexpectedly")
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	return result
}

func waitReady(ctx context.Context, core node.Runtime) (string, error) {
	readyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if version, err := core.Ready(readyCtx); err == nil {
			return version, nil
		}
		select {
		case <-readyCtx.Done():
			return "", errors.New("core readiness deadline exceeded or startup cancelled")
		case <-core.Done():
			return "", errors.New("core exited before readiness")
		case <-ticker.C:
		}
	}
}
