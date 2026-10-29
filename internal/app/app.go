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
	"github.com/pulse-foundation/pulse-vpn-node/internal/mihomo"
)

func Run(ctx context.Context, cfg config.Config) error {
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return errors.New("cannot bind management listener")
	}
	defer listener.Close()
	core, err := mihomo.Start(ctx, mihomo.Options{Binary: cfg.Binary, Directory: cfg.RuntimeDir, ControllerAddress: "127.0.0.1:19090"})
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
	if err := waitReady(ctx, core); err != nil {
		return err
	}
	server := &http.Server{Handler: httpapi.New(cfg.Token, core.Version), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 * 1024}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	slog.Info("agent ready", "coreVersion", mihomo.Version, "vpnConfigured", false)
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

func waitReady(ctx context.Context, core *mihomo.Process) error {
	readyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := core.Version(readyCtx); err == nil {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return errors.New("core readiness deadline exceeded or startup cancelled")
		case <-core.Done():
			return errors.New("core exited before readiness")
		case <-ticker.C:
		}
	}
}
