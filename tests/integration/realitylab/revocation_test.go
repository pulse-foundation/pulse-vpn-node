package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

// Exercise retry timing with virtual time; the Docker Users/Expiry/Managed
// scenarios still prove TCP and UDP revocation through the real core/client.
func TestUDPRevocationWaitsForTransientTransfer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		probes := 0
		err := awaitUDPRevocation(func(deadline time.Time) error {
			probes++
			if probes == 1 {
				return nil
			}
			time.Sleep(time.Until(deadline))
			return fmt.Errorf("read: %w", errUDPEchoTimeout)
		})
		if err != nil {
			t.Fatalf("transient UDP transfer was rejected: %v", err)
		}
		if probes != 2 || time.Since(started) != 3100*time.Millisecond {
			t.Fatalf("did not retry and observe a full quiet interval: probes=%d elapsed=%v", probes, time.Since(started))
		}
	})
}

func TestUDPRevocationRejectsPersistentTransfer(t *testing.T) {
	for _, latency := range []time.Duration{0, 2900 * time.Millisecond} {
		t.Run(latency.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				probes := 0
				err := awaitUDPRevocation(func(deadline time.Time) error {
					probes++
					if time.Until(deadline) != 3*time.Second || deadline.After(started.Add(10*time.Second)) {
						t.Fatal("probe must retain its full timeout within the cleanup window")
					}
					time.Sleep(latency)
					return nil
				})
				if !errors.Is(err, errUDPStillTransfers) {
					t.Fatalf("persistent UDP transfer was not rejected: %v", err)
				}
				if elapsed := time.Since(started); probes < 2 || elapsed < 7*time.Second || elapsed > 10*time.Second {
					t.Fatalf("unexpected cleanup budget: probes=%d elapsed=%v", probes, elapsed)
				}
			})
		})
	}
}

func TestUDPRevocationAllowsQuietIntervalEndingAtCleanupDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		err := awaitUDPRevocation(func(deadline time.Time) error {
			if time.Since(started) < 7*time.Second {
				return nil
			}
			time.Sleep(time.Until(deadline))
			return errUDPEchoTimeout
		})
		if err != nil || time.Since(started) != 10*time.Second {
			t.Fatalf("full quiet interval at cleanup deadline was rejected: err=%v elapsed=%v", err, time.Since(started))
		}
	})
}

func TestUDPRevocationPreservesProbeFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("probe transport unavailable")
		started := time.Now()
		err := awaitUDPRevocation(func(time.Time) error { return failure })
		if !errors.Is(err, failure) || time.Since(started) != 0 {
			t.Fatalf("probe failure must return immediately with its cause: %v", err)
		}
	})
}

func TestRevokedRejectsUDPProbeFaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func([]byte) []byte
	}{
		{"malformed frame", func([]byte) []byte { return []byte{1, 0, 0, 1} }},
		{"invalid address", func([]byte) []byte { return []byte{0, 0, 0, 99} }},
		{"truncated port", func([]byte) []byte { return []byte{0, 0, 0, 1, 127, 0, 0, 1} }},
		{"payload integrity", func(packet []byte) []byte { packet[len(packet)-1] ^= 1; return packet }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := revocationSessions(t, tc.response)
			if err := s.revoked(); err == nil {
				t.Fatal("unexpected UDP probe fault was accepted as revocation")
			}
		})
	}
	t.Run("closed local transport", func(t *testing.T) {
		s := revocationSessions(t, func(packet []byte) []byte { return packet })
		s.udp.conn.Close()
		if err := s.revoked(); err == nil {
			t.Fatal("closed local transport was accepted as revocation")
		}
	})
}

func revocationSessions(t *testing.T, response func([]byte) []byte) *liveSessions {
	t.Helper()
	t.Setenv("REALITY_TEST_TARGET_IP", "127.0.0.1")
	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	t.Cleanup(func() { relay.Close(); <-stopped })
	go func() {
		defer close(stopped)
		packet := make([]byte, 2048)
		for {
			n, addr, err := relay.ReadFromUDP(packet)
			if err != nil {
				return
			}
			if reply := response(packet[:n]); reply != nil {
				if _, err := relay.WriteToUDP(reply, addr); err != nil {
					return
				}
			}
		}
	}()
	conn, err := net.DialUDP("udp4", nil, relay.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	tcp, peer := net.Pipe()
	peer.Close()
	control, controlPeer := net.Pipe()
	controlPeer.Close()
	s := &liveSessions{
		tcp: &tcpSession{conn: tcp, reader: bufio.NewReader(tcp)},
		udp: &udpSession{control: control, conn: conn},
	}
	t.Cleanup(s.close)
	return s
}
