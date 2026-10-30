//go:build integration

package integration_test

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// The Go probe waits for an acknowledgement while the host coordinator changes
// database availability. stdout is a bounded protocol, never raw Docker logs.
func (l *lab) live(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := l.command(ctx, l.args("run", "--rm", "--no-deps", "-T", "-e", "REALITY_TEST_TARGET_IP="+l.targetIP(t), "probe", name)...)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	need(t, err == nil, "cannot read live probe")
	stdin, err := cmd.StdinPipe()
	need(t, err == nil, "cannot acknowledge live probe")
	defer stdin.Close()
	need(t, cmd.Start() == nil, "cannot start live probe")
	lines := make(chan string, 16)
	scanned := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				scanned <- ctx.Err()
				return
			}
		}
		scanned <- scanner.Err()
	}()
	waited := false
	defer func() {
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
		// Cancellation unblocks both the pipe and a scanner waiting to send a line.
		if err := <-scanned; err != nil && !t.Failed() {
			t.Error("live probe output could not be read completely")
		}
	}()
	progress := time.NewTimer(100 * time.Second)
	defer progress.Stop()
	outage, recovered := false, false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("live probe exceeded deadline")
		case <-progress.C:
			t.Fatal("live probe progress timed out")
		case line, ok := <-lines:
			if !ok {
				err := cmd.Wait()
				waited = true
				need(t, err == nil, "live probe failed (diagnostics withheld)")
				need(t, outage && recovered, "probe did not exercise database outage and recovery")
				return
			}
			if !progress.Stop() {
				select {
				case <-progress.C:
				default:
				}
			}
			progress.Reset(100 * time.Second)
			switch line {
			case "READY: database outage":
				need(t, !outage && !recovered, "duplicate/out-of-order outage request")
				l.compose(t, "stop", "database")
				outage = true
			case "READY: database recovery":
				need(t, outage && !recovered, "duplicate/out-of-order recovery request")
				l.compose(t, "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "database")
				recovered = true
			default:
				if strings.HasPrefix(line, "FAIL: ") {
					t.Fatal("live probe assertion failed (diagnostics withheld)")
				}
				if strings.HasPrefix(line, "PASS: ") {
					t.Log(line)
				}
				continue
			}
			_, err = io.WriteString(stdin, "continue\n")
			need(t, err == nil, "cannot acknowledge database transition")
		}
	}
}
