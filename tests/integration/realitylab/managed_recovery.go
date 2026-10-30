package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func managedCreateFailure() error {
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	body := map[string]any{"expectedRevision": s.Revision, "transport": map[string]string{"type": "raw"}, "listenPort": 8443, "reality": map[string]any{"destination": "target:8443", "serverNames": []string{"target.test"}}}
	var op userOperation
	if err = userAPI("PUT", "connection", body, 202, &op); err != nil {
		return err
	}
	if err = writeJSON("failed-creation.json", op); err != nil {
		return err
	}
	return managedCreationResult(false)
}

func managedCreationResult(recovered bool) error {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "failed-creation.json"))
	if err != nil {
		return errors.New("failed creation fixture unavailable")
	}
	var saved userOperation
	if json.Unmarshal(raw, &saved) != nil {
		return errors.New("invalid failed creation fixture")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var op userOperation
		if err = userAPI("GET", "operations/"+strconv.FormatInt(saved.Revision, 10), nil, 200, &op); err != nil {
			return err
		}
		s, err := userSnapshot()
		if err != nil {
			return err
		}
		if !recovered && op.State == "failed" || recovered && op.State == "applied" && s.Runtime.AppliedRevision == saved.Revision {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("creation did not reach expected failure/recovery state")
		}
		time.Sleep(100 * time.Millisecond)
	}

	var status struct {
		Status        string
		VPNConfigured bool
	}
	if err = userAPI("GET", "status", nil, 200, &status); err != nil {
		return err
	}
	if status.Status != "ready" || status.VPNConfigured != recovered {
		return errors.New("management status disagrees with verified listener")
	}
	var view connectionView
	if err = userAPI("GET", "connection", nil, 200, &view); err != nil {
		return err
	}
	if (view.Connection != nil) != recovered {
		return errors.New("failed/unverified connection exposed")
	}
	if recovered && view.Connection.ListenPort != 8443 {
		return errors.New("recovery changed listener")
	}
	s, err := userSnapshot()
	if err != nil || s.Revision != saved.Revision {
		return errors.New("recovery changed durable revision")
	}
	return nil
}
