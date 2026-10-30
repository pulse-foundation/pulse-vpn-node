package mihomo

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func copyVLESS(v *node.VLESS) *node.VLESS {
	if v == nil {
		return nil
	}
	copy := *v
	copy.Users = append([]node.User(nil), v.Users...)
	copy.Reality.ShortIDs = append([]string(nil), v.Reality.ShortIDs...)
	copy.Reality.ServerNames = append([]string(nil), v.Reality.ServerNames...)
	return &copy
}

// operation bounds work and serializes mutation/cleanup without holding a state
// mutex across I/O. Stop and child exit cancel the active operation and waiters.
func (p *Process) operation(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	stop := context.AfterFunc(p.lifetime, cancel)
	release := func() { stop(); cancel() }
	select {
	case <-ctx.Done():
		release()
		return nil, nil, errors.New("core operation cancelled")
	case <-p.operations:
	}
	if ctx.Err() != nil || p.lifetime.Err() != nil {
		p.operations <- struct{}{}
		release()
		return nil, nil, errors.New("core operation cancelled")
	}
	return ctx, func() { p.operations <- struct{}{}; release() }, nil
}

// ApplyUsers validates an immutable desired snapshot with the startup binary,
// reloads the existing child, and retains the artifact only after readiness.
// Failed or ambiguous reloads must be retried with current desired state: restoring
// the previous artifact could re-enable revoked credentials.
func (p *Process) ApplyUsers(ctx context.Context, users []node.User) error {
	ctx, release, err := p.operation(ctx)
	if err != nil {
		return err
	}
	defer release()
	if p.startupVLESS == nil {
		return errors.New("live users require a configured VLESS listener")
	}
	candidate := copyVLESS(p.startupVLESS)
	candidate.Users = append([]node.User(nil), users...)
	data, err := render(p.controllerAddress, p.controller.secret, candidate)
	if err != nil {
		return errors.New("invalid live user configuration")
	}
	dir := filepath.Dir(p.configPath)
	path := filepath.Join(dir, "candidate.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return errors.New("cannot write private candidate configuration")
	}
	defer os.Remove(path)
	validationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	validation := exec.CommandContext(validationCtx, p.binary, "-t", "-d", dir, "-f", path)
	validation.Stdout = io.Discard
	validation.Stderr = io.Discard
	validation.WaitDelay = time.Second
	if err := validation.Run(); err != nil {
		return errors.New("core candidate validation failed")
	}
	// Check the running core version before applying, as well as after reload.
	if _, err := p.Version(ctx); err != nil {
		return err
	}
	if err := p.controller.Apply(ctx, data); err != nil {
		return err
	}
	if _, err := p.Ready(ctx); err != nil {
		return errors.New("core reload postconditions failed")
	}
	if ctx.Err() != nil || p.lifetime.Err() != nil {
		return errors.New("core operation cancelled")
	}
	if err := os.Rename(path, p.configPath); err != nil {
		return errors.New("cannot retain verified core configuration")
	}
	return nil
}

// SweepRevoked performs one bounded sample and selective close pass. The owner
// must repeat it for sessions accepted before removal but observed afterward.
func (p *Process) SweepRevoked(ctx context.Context, names []string) (int, error) {
	ctx, release, err := p.operation(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	return p.controller.SweepRevoked(ctx, names)
}

var _ node.UserRuntime = (*Process)(nil)
