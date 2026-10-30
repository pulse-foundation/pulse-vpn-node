package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/app"
	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		return runInit(os.Args[2:], os.Stdout)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "migrate" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return app.Migrate(ctx, cfg)
		}
		if len(os.Args) != 2 || (os.Args[1] != "healthcheck" && os.Args[1] != "status") {
			return errors.New("usage: pulse-vpn-node [init|healthcheck|status|migrate]")
		}
		return check(cfg, os.Args[1] == "status")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return app.Run(ctx, cfg)
}

func check(cfg config.Config, printStatus bool) error {
	address, err := healthcheckAddress(cfg.Listen)
	if err != nil {
		return errors.New("invalid healthcheck address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/v1/status", nil)
	if err != nil {
		return errors.New("cannot create healthcheck request")
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("agent is unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("agent is not ready")
	}
	if printStatus {
		if _, err := io.Copy(os.Stdout, io.LimitReader(res.Body, 4096)); err != nil {
			return errors.New("cannot print status")
		}
	}
	return nil
}

func healthcheckAddress(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", errors.New("invalid healthcheck address")
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port), nil
}
