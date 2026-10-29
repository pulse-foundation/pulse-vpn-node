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
)

type Options struct{ Binary, Directory, ControllerAddress string }

type Process struct {
	cmd        *exec.Cmd
	done       chan struct{}
	err        error // Written before done closes; read only after observing done.
	configPath string
	controller *Controller
}

// Start uses ctx only for startup validation. The caller owns Stop for the child lifetime.
func Start(ctx context.Context, o Options) (*Process, error) {
	host, port, err := net.SplitHostPort(o.ControllerAddress)
	n, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || ip == nil || !ip.IsLoopback() || n < 1 || n > 65535 {
		return nil, errors.New("core controller must bind a loopback IP and valid port")
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
	data, err := renderIdle(o.ControllerAddress, secret)
	if err != nil {
		return nil, errors.New("cannot render idle core configuration")
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
	p := &Process{cmd: cmd, done: make(chan struct{}), configPath: path, controller: NewController("http://"+o.ControllerAddress, secret)}
	started = true
	go func() {
		if err := cmd.Wait(); err != nil {
			p.err = errors.New("core process exited unsuccessfully")
		}
		_ = os.RemoveAll(dir)
		close(p.done)
	}()
	return p, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Err() error            { <-p.done; return p.err }

func (p *Process) Version(ctx context.Context) (string, error) {
	select {
	case <-p.done:
		return "", errors.New("core process is stopped")
	default:
	}
	return p.controller.Version(ctx)
}

func (p *Process) Stop(ctx context.Context) error {
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
