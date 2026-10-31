package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

func backupSetup() error {
	if err := probe(); err != nil {
		return err
	}
	uuid, err := os.ReadFile(filepath.Join(fixtureDir, "second-uuid"))
	if err != nil {
		return errors.New("fixture unavailable")
	}
	if err = mutateUser("second-test", true, string(uuid)); err != nil {
		return err
	}
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	var op userOperation
	if err = userAPI("PUT", "users/second-test", map[string]any{"enabled": true, "expectedRevision": s.Revision, "accessPolicy": map[string]any{"revision": 1, "expiresAt": time.Now().UTC().Add(8 * time.Second), "renew": false}}, 202, &op); err != nil {
		return err
	}
	return awaitRevision(op.Revision)
}

func backupHeld() error {
	if err := probe(); err != nil {
		return err
	}
	if err := expectRejected("second:7890"); err != nil {
		return err
	}
	var s struct {
		Users    []struct{ Name, RecoveryHoldID string }
		Recovery *struct {
			Generation string
			Pending    bool
		}
	}
	if err := userAPI("GET", "users", nil, 200, &s); err != nil {
		return err
	}
	if s.Recovery == nil || s.Recovery.Generation == "" || s.Recovery.Pending {
		return errors.New("recovery evidence missing")
	}
	for _, u := range s.Users {
		if u.Name == "second-test" && u.RecoveryHoldID != "" {
			return nil
		}
	}
	return errors.New("finite access hold missing")
}

func backupRelease() error {
	var s struct {
		Revision int64
		Users    []struct {
			Name, RecoveryHoldID string
			PolicyRevision       int64
		}
	}
	if err := userAPI("GET", "users", nil, 200, &s); err != nil {
		return err
	}
	for _, u := range s.Users {
		if u.Name != "second-test" {
			continue
		}
		var op userOperation
		// A fresh explicit request names the actual hold; the agent separately checks
		// freshness/skew. Production controllers must establish synchronization.
		if err := userAPI("PUT", "users/second-test", map[string]any{"enabled": true, "expectedRevision": s.Revision, "accessPolicy": map[string]any{"revision": u.PolicyRevision + 1, "expiresAt": time.Now().UTC().Add(time.Hour), "renew": true, "recovery": map[string]any{"holdId": u.RecoveryHoldID, "controllerUtc": time.Now().UTC(), "clockSynchronized": true}}}, 202, &op); err != nil {
			return err
		}
		if err := awaitRevision(op.Revision); err != nil {
			return err
		}
		client, err := proxyClient("second:7890")
		if err != nil {
			return err
		}
		return transfer(client, "https")
	}
	return errors.New("recovery target missing")
}

func backupRevoked() error {
	if err := probe(); err != nil {
		return err
	}
	if err := expectRejected("second:7890"); err != nil {
		return err
	}
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	for _, u := range s.Users {
		if u.Name == "second-test" {
			return errors.New("deleted access returned")
		}
	}
	return nil
}
