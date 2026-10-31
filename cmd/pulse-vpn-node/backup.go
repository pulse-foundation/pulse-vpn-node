package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/postgres"
	"io"
	"time"
)

// This is a privileged offline transport. Stdout for backup contains secrets and
// must go straight to an encrypted off-node store, never logs or operation DTOs.
func runBackupCommand(cfg config.Config, command string, input io.Reader, output io.Writer) error {
	if cfg.DatabaseURL == "" {
		return errors.New("backup database unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("backup database unavailable")
	}
	defer store.Close()
	if command == "recovery-status" {
		state, err := store.RecoveryResult(ctx)
		if err != nil {
			return errors.New("recovery status unavailable")
		}
		if json.NewEncoder(output).Encode(state) != nil {
			return errors.New("recovery status unavailable")
		}
		return nil
	}
	if command == "backup" {
		snapshot, err := store.ExportBackup(ctx)
		if err != nil {
			return errors.New("backup snapshot failed")
		}
		if json.NewEncoder(output).Encode(snapshot) != nil {
			return errors.New("backup transfer failed")
		}
		return nil
	}
	var request struct {
		Snapshot      postgres.DatabaseBackup  `json:"snapshot"`
		CurrentIntent *postgres.RecoveryIntent `json:"currentIntent"`
	}
	decoder := json.NewDecoder(io.LimitReader(input, (64<<20)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return errors.New("invalid restore input")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid restore input")
	}
	result, err := store.RestoreBackup(ctx, request.Snapshot, request.CurrentIntent)
	if err != nil {
		return errors.New("backup restore failed")
	}
	if json.NewEncoder(output).Encode(result) != nil {
		return errors.New("restore result unavailable")
	}
	return nil
}
