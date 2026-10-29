package node

import (
	"context"
	"errors"
	"testing"
)

type memoryUsers struct {
	snapshot           UserSnapshot
	readErr, recordErr error
	recorded           []int64
}

func (s *memoryUsers) Initialize(context.Context, []User) (UserSnapshot, error) {
	return s.snapshot, nil
}
func (s *memoryUsers) Snapshot(context.Context) (UserSnapshot, error)        { return s.snapshot, s.readErr }
func (s *memoryUsers) Change(context.Context, UserChange) (Operation, error) { return Operation{}, nil }
func (s *memoryUsers) Operation(context.Context, int64) (Operation, error)   { return Operation{}, nil }
func (s *memoryUsers) RecordApply(_ context.Context, revision int64, message string) error {
	if message == "" {
		s.recorded = append(s.recorded, revision)
	}
	return s.recordErr
}

type userCore struct {
	applies  int
	applyErr error
	revoked  []string
	sweepErr error
}

func (c *userCore) ApplyUsers(context.Context, []User) error { c.applies++; return c.applyErr }
func (c *userCore) SweepRevoked(_ context.Context, names []string) (int, error) {
	c.revoked = append([]string{}, names...)
	return 0, c.sweepErr
}

func TestReconcileRetriesResultWithoutReloadAndSweepsDuringDBOutage(t *testing.T) {
	s := &memoryUsers{snapshot: UserSnapshot{Revision: 1, Users: []ManagedUser{{Name: "disabled", Enabled: false}}}, recordErr: errors.New("database failed")}
	c := &userCore{}
	m := NewUserManager(s, c)
	m.step(context.Background())
	if c.applies != 1 || m.Status().AppliedRevision != 1 || m.Status().Error == "" {
		t.Fatal("application/result state conflated")
	}
	s.recordErr = nil
	m.step(context.Background())
	if c.applies != 1 || m.Status().Error != "" {
		t.Fatal("replayed core after uncertain DB result")
	}
	c.revoked = nil
	s.readErr = errors.New("offline")
	m.step(context.Background())
	if len(c.revoked) != 1 || c.revoked[0] != "disabled" || c.applies != 1 {
		t.Fatal("outage stopped revocation sweep or reloaded core")
	}
}
func TestReconcileNeverMarksFailedOrNewerRevisionApplied(t *testing.T) {
	s := &memoryUsers{snapshot: UserSnapshot{Revision: 2}}
	c := &userCore{applyErr: errors.New("failed")}
	m := NewUserManager(s, c)
	m.step(context.Background())
	if m.Status().AppliedRevision != 0 || len(s.recorded) != 0 {
		t.Fatal("marked failure applied")
	}
	s.snapshot.Revision = 3
	c.applyErr = nil
	m.step(context.Background())
	if m.Status().AppliedRevision != 3 || len(s.recorded) != 1 || s.recorded[0] != 3 {
		t.Fatal("recorded wrong revision")
	}
	c.sweepErr = errors.New("late sessions")
	m.step(context.Background())
	if m.Status().CleanupState != "retrying" || m.Status().AppliedRevision != 3 {
		t.Fatal("cleanup conflated with credentials")
	}
}

func TestAmbiguousApplyUsesLatestCleanupIntentDuringDBOutage(t *testing.T) {
	for _, enable := range []bool{true, false} {
		s := &memoryUsers{snapshot: UserSnapshot{Revision: 1, Users: []ManagedUser{{Name: "alice", Enabled: !enable}}}}
		core := &userCore{}
		m := NewUserManager(s, core)
		m.step(context.Background())
		s.snapshot = UserSnapshot{Revision: 2, Users: []ManagedUser{{Name: "alice", Enabled: enable}}}
		core.applyErr = errors.New("reload reply lost")
		m.step(context.Background())
		s.readErr = errors.New("database offline")
		core.revoked = nil
		m.step(context.Background())
		if m.Status().AppliedRevision != 1 || m.Status().CleanupState != "retrying" {
			t.Fatal("uncertain revision reported verified")
		}
		if enable && len(core.revoked) != 0 {
			t.Fatal("stale cleanup disconnected re-enabled user")
		}
		if !enable && (len(core.revoked) != 1 || core.revoked[0] != "alice") {
			t.Fatal("lost pending revocation during outage")
		}
	}
}

func TestPendingVerifiedResultSurvivesNewerRevision(t *testing.T) {
	for _, newerFails := range []bool{false, true} {
		s := &memoryUsers{snapshot: UserSnapshot{Revision: 1}, recordErr: errors.New("result write failed")}
		core := &userCore{}
		m := NewUserManager(s, core)
		m.step(context.Background())
		s.recorded = nil
		s.snapshot.Revision = 2
		s.recordErr = nil
		if newerFails {
			core.applyErr = errors.New("newer application failed")
		}
		m.step(context.Background())
		if len(s.recorded) == 0 || s.recorded[0] != 1 {
			t.Fatal("lost pending verified result when a newer revision arrived")
		}
		if core.applies != 2 {
			t.Fatal("new desired state was not attempted after saving prior result")
		}
		if newerFails && (len(s.recorded) != 1 || m.Status().AppliedRevision != 1) {
			t.Fatal("failed newer revision displaced verified result")
		}
		if !newerFails && (len(s.recorded) != 2 || s.recorded[1] != 2) {
			t.Fatal("did not record successful revisions in order")
		}
	}
}

func TestPendingResultWriteBlocksNewApplyButContinuesCleanup(t *testing.T) {
	s := &memoryUsers{snapshot: UserSnapshot{Revision: 1, Users: []ManagedUser{{Name: "disabled"}}}, recordErr: errors.New("result write unavailable")}
	core := &userCore{}
	m := NewUserManager(s, core)
	m.step(context.Background())
	s.snapshot = UserSnapshot{Revision: 2}
	core.revoked = nil
	m.step(context.Background())
	if core.applies != 1 || m.Status().AppliedRevision != 1 || m.Status().Error == "" {
		t.Fatal("overwrote unrecorded applied revision")
	}
	if len(core.revoked) != 1 || core.revoked[0] != "disabled" {
		t.Fatal("pending result write stopped existing revocation cleanup")
	}
}
