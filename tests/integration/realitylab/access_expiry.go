package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

func liveExpiry() error {
	if err := probe(); err != nil {
		return err
	}
	uuid, err := os.ReadFile(filepath.Join(fixtureDir, "second-uuid"))
	if err != nil {
		return errors.New("second fixture unavailable")
	}
	if err = mutateUser("second-test", true, string(uuid)); err != nil {
		return err
	}
	original, err := openSessions("client:7890")
	if err != nil {
		return err
	}
	defer original.close()
	second, err := openSessions("second:7890")
	if err != nil {
		return err
	}
	defer second.close()
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	deadline := time.Now().UTC().Add(5 * time.Second)
	var op userOperation
	if err = userAPI("PUT", "users/second-test", map[string]any{"enabled": true, "expectedRevision": s.Revision, "accessPolicy": map[string]any{"revision": 1, "expiresAt": deadline, "renew": false}}, 202, &op); err != nil {
		return err
	}
	if err = awaitRevision(op.Revision); err != nil {
		return err
	}
	// No control-plane requests during expiry. Existing second TCP must close;
	// UDP must stop, while original TCP/UDP remain live in the same core.
	if err = second.revoked(); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	return persistedExpiry()
}

func persistedExpiry() error {
	if err := probe(); err != nil {
		return err
	}
	if err := expectRejected("second:7890"); err != nil {
		return err
	}
	if err := requireUsers(true, true); err != nil {
		return err
	}
	var page struct {
		Events []struct {
			Revision       int64
			Name           string
			PolicyRevision int64
		}
	}
	if err := userAPI("GET", "access-policy/events?after=0", nil, 200, &page); err != nil {
		return err
	}
	if len(page.Events) != 1 || page.Events[0].Name != "second-test" || page.Events[0].PolicyRevision != 1 {
		return errors.New("durable expiry evidence missing")
	}
	return nil
}

func expiryBeforeStop() error {
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	deadline := time.Now().UTC().Add(5 * time.Second)
	var op userOperation
	if err = userAPI("PUT", "users/second-test", map[string]any{"enabled": true, "expectedRevision": s.Revision, "accessPolicy": map[string]any{"revision": 2, "expiresAt": deadline, "renew": true}}, 202, &op); err != nil {
		return err
	}
	return awaitRevision(op.Revision)
}
