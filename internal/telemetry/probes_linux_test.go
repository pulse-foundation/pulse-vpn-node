//go:build linux

package telemetry

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestProbesLinuxPingSocket(t *testing.T) {
	// Build sandboxes may deny ping sockets even when the runtime host permits
	// them. Verify that environment as unavailable; only an allowed socket may
	// exercise the success branch below.
	fd, socketErr := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_ICMP)
	if socketErr == nil {
		_ = syscall.Close(fd)
	}
	denied := errors.Is(socketErr, syscall.EPERM) || errors.Is(socketErr, syscall.EACCES)
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "local ping", Type: "icmp", Address: "127.0.0.1"}}, Timeout: time.Second, Concurrency: 1})
	c := p.Sample(context.Background()).Checks[0]
	if os.Getenv("PULSE_TEST_PING_DENIED") == "1" || denied {
		if c.Status != "unavailable" || c.Reason != "socket_unavailable" || c.LossPercent != nil || c.RTTMilliseconds != nil {
			t.Fatalf("permission failure: %+v", c)
		}
		return
	}
	if c.Status != "success" || c.LossPercent == nil || *c.LossPercent != 0 || c.RTTMilliseconds == nil {
		t.Fatalf("loopback ping: %+v", c)
	}
}
