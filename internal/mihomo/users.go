package mihomo

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	p.stateMu.RLock()
	candidate := copyVLESS(p.startupVLESS)
	p.stateMu.RUnlock()
	if candidate == nil {
		return errors.New("live users require a configured VLESS listener")
	}
	candidate.Users = append([]node.User(nil), users...)
	return p.ApplyState(ctx, candidate)
}

// ReservedPorts reports the controller port owned by this runtime. Keep this
// detail in the adapter rather than duplicating it in API or persistence policy.
func (p *Process) ReservedPorts() []int {
	_, port, _ := net.SplitHostPort(p.controllerAddress)
	n, _ := strconv.Atoi(port)
	return []int{n}
}

// ApplyState is the single mutation path for both the listener and all users.
// Candidate verification uses candidate settings, never the previous listener.
func (p *Process) ApplyState(ctx context.Context, candidate *node.VLESS) error {
	ctx, release, err := p.operation(ctx)
	if err != nil {
		return err
	}
	defer release()
	candidate = copyVLESS(candidate)
	p.stateMu.RLock()
	previous := copyVLESS(p.startupVLESS)
	uncertain := p.uncertain
	p.stateMu.RUnlock()
	if candidate != nil {
		if candidate.Validate() != nil {
			return errors.New("invalid live configuration")
		}
		_, controllerPort, _ := net.SplitHostPort(p.controllerAddress)
		if strconv.Itoa(candidate.Port) == controllerPort {
			return errors.New("VLESS port conflicts with core controller")
		}
		if previous == nil || previous.Address() != candidate.Address() {
			// An earlier ambiguous controller reply may have bound the candidate already.
			// Only accept that case if this child demonstrably owns the port.
			if !uncertain || ownsListener(ctx, p.cmd.Process.Pid, candidate.Port) != nil {
				listener, err := net.Listen("tcp", candidate.Address())
				if err != nil {
					return errors.New("VLESS listen address is unavailable")
				}
				_ = listener.Close()
			}
		}
	}
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
	p.stateMu.Lock()
	p.uncertain = true
	p.stateMu.Unlock()
	if err := p.controller.Apply(ctx, data); err != nil {
		return err
	}
	if _, err := p.readySettings(ctx, candidate); err != nil {
		return errors.New("core reload postconditions failed")
	}
	if ctx.Err() != nil || p.lifetime.Err() != nil {
		return errors.New("core operation cancelled")
	}
	if err := os.Rename(path, p.configPath); err != nil {
		return errors.New("cannot retain verified core configuration")
	}
	p.stateMu.Lock()
	p.startupVLESS = copyVLESS(candidate)
	p.uncertain = false
	p.stateMu.Unlock()
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

var _ node.ManagedRuntime = (*Process)(nil)
