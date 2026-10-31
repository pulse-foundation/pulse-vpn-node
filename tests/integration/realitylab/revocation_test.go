package main

import (
	"bufio"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TCP and UDP are independent cleanup boundaries. Closing the TCP peer before
// the first UDP exchange deliberately reproduces a sweep that has not yet
// reached the UDP tracker; no scheduler timing or Docker core is required.
func TestRevokedWaitsForUDPAfterTCPClosure(t *testing.T) {
	var packets atomic.Int32
	s := revocationSessions(t, func(packet []byte) []byte {
		if packets.Add(1) == 1 {
			return packet
		}
		return nil
	})
	if err := s.revoked(); err != nil {
		t.Fatalf("TCP closed before UDP cleanup completed: %v", err)
	}
	if packets.Load() < 2 {
		t.Fatal("did not observe UDP cessation after the transient echo")
	}
}

func TestRevokedRejectsPersistentUDPTransfer(t *testing.T) {
	s := revocationSessions(t, func(packet []byte) []byte { return packet })
	started := time.Now()
	err := s.revoked()
	if err == nil || !strings.Contains(err.Error(), "still transfers") {
		t.Fatalf("persistent UDP transfer was not rejected: %v", err)
	}
	if time.Since(started) > 11*time.Second {
		t.Fatal("UDP revocation assertion exceeded its bounded deadline")
	}
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
