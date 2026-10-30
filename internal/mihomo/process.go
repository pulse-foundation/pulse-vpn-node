package mihomo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

type Options struct {
	Binary, Directory, ControllerAddress string
	VLESS                                *node.VLESS
}

type Process struct {
	cmd               *exec.Cmd
	done              chan struct{}
	err               error // Written before done closes; read only after observing done.
	configPath        string
	controller        *Controller
	probeAddress      string
	vpnPort           int
	binary            string
	controllerAddress string
	startupVLESS      *node.VLESS
	operations        chan struct{} // A single writer, with context-aware acquisition.
	lifetime          context.Context
	cancel            context.CancelFunc
}

// Start uses ctx only for startup validation. The caller owns Stop for the child lifetime.
func Start(ctx context.Context, o Options) (*Process, error) {
	o.VLESS = copyVLESS(o.VLESS)
	host, port, err := net.SplitHostPort(o.ControllerAddress)
	n, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || ip == nil || !ip.IsLoopback() || n < 1 || n > 65535 {
		return nil, errors.New("core controller must bind a loopback IP and valid port")
	}
	probeAddress := ""
	if o.VLESS != nil {
		if err := o.VLESS.Validate(); err != nil {
			return nil, err
		}
		if o.VLESS.Port == n {
			return nil, errors.New("VLESS port conflicts with core controller")
		}
		// Fail before spawning if occupied; readiness and end-to-end tests check the actual listener.
		l, err := net.Listen("tcp", o.VLESS.Address())
		if err != nil {
			return nil, errors.New("VLESS listen address is unavailable")
		}
		_ = l.Close()
		probeAddress = o.VLESS.ProbeAddress()
	}
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return nil, errors.New("cannot create runtime directory")
	}
	dir, err := os.MkdirTemp(o.Directory, "mihomo-")
	if err != nil {
		return nil, errors.New("cannot create private core directory")
	}
	started := false
	defer func() {
		if !started {
			_ = os.RemoveAll(dir)
		}
	}()
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, errors.New("cannot create controller secret")
	}
	secret := hex.EncodeToString(entropy[:])
	data, err := render(o.ControllerAddress, secret, o.VLESS)
	if err != nil {
		return nil, errors.New("cannot render core configuration")
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, errors.New("cannot write private core configuration")
	}
	validationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	validation := exec.CommandContext(validationCtx, o.Binary, "-t", "-d", dir, "-f", path)
	validation.Stdout = io.Discard
	validation.Stderr = io.Discard
	validation.WaitDelay = time.Second
	if err := validation.Run(); err != nil {
		return nil, errors.New("core configuration validation failed")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cmd := exec.Command(o.Binary, "-d", dir, "-f", path)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("cannot start core process")
	}
	// Startup/request cancellation never owns the serving child lifetime.
	lifetime, lifetimeCancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &Process{
		cmd: cmd, done: make(chan struct{}), configPath: path,
		controller:   NewController("http://"+o.ControllerAddress, secret),
		probeAddress: probeAddress, binary: o.Binary, controllerAddress: o.ControllerAddress,
		startupVLESS: o.VLESS, operations: make(chan struct{}, 1),
		lifetime: lifetime, cancel: lifetimeCancel,
	}
	p.operations <- struct{}{}
	if o.VLESS != nil {
		p.vpnPort = o.VLESS.Port
	}
	started = true
	go func() {
		if err := cmd.Wait(); err != nil {
			p.err = errors.New("core process exited unsuccessfully")
		}
		p.cancel()
		// Let cancelled operations release files before removing the runtime directory.
		<-p.operations
		_ = os.RemoveAll(dir)
		close(p.done)
		p.operations <- struct{}{}
	}()
	return p, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Err() error            { <-p.done; return p.err }

func (p *Process) Version(ctx context.Context) (string, error) {
	if p.lifetime.Err() != nil {
		return "", errors.New("core process is stopped")
	}
	select {
	case <-p.done:
		return "", errors.New("core process is stopped")
	default:
	}
	return p.controller.Version(ctx)
}

// Ready verifies controller, listener ownership and TCP reachability, not end-to-end VPN health.
func (p *Process) Ready(ctx context.Context) (string, error) {
	version, err := p.Version(ctx)
	if err != nil {
		return "", err
	}
	if p.probeAddress != "" {
		if err := ownsListener(ctx, p.cmd.Process.Pid, p.vpnPort); err != nil {
			return "", err
		}
		d := net.Dialer{Timeout: time.Second}
		conn, err := d.DialContext(ctx, "tcp", p.probeAddress)
		if err != nil {
			return "", errors.New("VLESS listener unavailable")
		}
		_ = conn.Close()
	}
	return version, nil
}

func (p *Process) Stop(ctx context.Context) error {
	p.cancel()
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.New("cannot signal core process")
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return errors.New("cannot kill core process")
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-p.done:
			return nil
		case <-timer.C:
			return errors.New("core process did not exit after kill")
		}
	}
}
