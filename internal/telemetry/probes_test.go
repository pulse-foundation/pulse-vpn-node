package telemetry

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestProbesEmptyAndTCP(t *testing.T) {
	empty := NewProbes(ProbeOptions{}).Sample(context.Background())
	if empty.Kind != "connectivity" || empty.Checks == nil || len(empty.Checks) != 0 {
		t.Fatalf("empty sample: %+v", empty)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "local", Type: "tcp", Address: listener.Addr().String()}}, Timeout: time.Second, Concurrency: 1})
	s := p.Sample(context.Background())
	c := s.Checks[0]
	if c.Status != "success" || c.RTTMilliseconds == nil || c.LossPercent != nil || c.Scope != "container" || c.Vantage != "node_direct" || c.ObservedAt.After(s.ObservedAt) {
		t.Fatalf("check: %+v", c)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), listener.Addr().String()) {
		t.Fatal("target leaked")
	}
	listener.Close()
	c = p.Sample(context.Background()).Checks[0]
	if c.Status != "failed" || c.RTTMilliseconds != nil || c.Reason != "connection_failed" {
		t.Fatalf("closed listener: %+v", c)
	}
}

func trustProbeServer(p *Probes, server *httptest.Server) {
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	p.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func TestProbesHTTPSDirectBoundedAndVerified(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	var redirected atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/destination", http.StatusFound)
		case "/destination":
			redirected.Add(1)
		case "/failure":
			w.WriteHeader(503)
		case "/large":
			io.WriteString(w, strings.Repeat("x", 4096))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	for _, tc := range []struct{ path, status, reason string }{
		{"/", "success", ""}, {"/redirect", "failed", "http_status"}, {"/failure", "failed", "http_status"}, {"/large", "success", ""},
	} {
		p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "web", Type: "https", Address: server.URL + tc.path}}, Timeout: time.Second, Concurrency: 1})
		trustProbeServer(p, server)
		if tc.path == "/" {
			// Use a non-loopback URL to catch ProxyFromEnvironment: Go skips
			// loopback URLs automatically, even with HTTPS_PROXY configured.
			p.targets[0].Address = "https://example.com/"
			p.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "example.com:443" {
					return nil, errInvalidTarget
				}
				return p.dialer.DialContext(ctx, network, server.Listener.Addr().String())
			}
		}
		c := p.Sample(context.Background()).Checks[0]
		if c.Status != tc.status || c.Reason != tc.reason {
			t.Fatalf("%s: %+v", tc.path, c)
		}
	}
	if proxyCalls.Load() != 0 || redirected.Load() != 0 {
		t.Fatal("probe used proxy or followed redirect")
	}
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "web", Type: "https", Address: server.URL}}, Timeout: time.Second, Concurrency: 1})
	c := p.Sample(context.Background()).Checks[0]
	if c.Status != "failed" || c.Reason != "tls_failed" {
		t.Fatalf("untrusted TLS: %+v", c)
	}
}

func TestProbesHTTPSCancellationAndConcurrency(t *testing.T) {
	var active, maximum atomic.Int32
	entered := make(chan struct{}, 16)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	targets := make([]ProbeTarget, 8)
	for i := range targets {
		targets[i] = ProbeTarget{Name: "slow", Type: "https", Address: server.URL}
	}
	p := NewProbes(ProbeOptions{Targets: targets, Timeout: time.Second, Concurrency: 2})
	trustProbeServer(p, server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Sample, 1)
	go func() { done <- p.Sample(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("requests did not start")
		}
	}
	cancel()
	select {
	case s := <-done:
		if len(s.Checks) != 8 {
			t.Fatal("missing checks")
		}
		for _, c := range s.Checks {
			if c.Status != "unavailable" || c.Reason != "canceled" {
				t.Fatalf("cancel: %+v", c)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not join probes")
	}
	if maximum.Load() > 2 {
		t.Fatalf("concurrency exceeded: %d", maximum.Load())
	}
}

func TestProbesHTTPSBodyDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()
		headersSent := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		go func() {
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				t.Error("cannot read probe request", err)
				return
			}
			defer request.Body.Close()
			if _, err := io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\n"); err != nil {
				t.Error("cannot send probe headers", err)
				return
			}
			close(headersSent)
			<-release // Never finish the body, even when the request is cancelled.
		}()
		p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "slow", Type: "https", Address: "https://probe.test/"}}, Timeout: time.Second, Concurrency: 1})
		// Model an already established TLS connection so this tests the body only.
		// Real certificate verification is covered by TestProbesHTTPSDirectBoundedAndVerified.
		p.transport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) { return client, nil }
		done := make(chan Sample, 1)
		go func() { done <- p.Sample(context.Background()) }()
		synctest.Wait()
		select {
		case <-headersSent:
		default:
			t.Fatal("probe did not reach response body")
		}
		select {
		case <-done:
			t.Fatal("probe completed before body or deadline")
		default:
		}
		time.Sleep(time.Second) // Advances virtual time, not wall-clock time.
		synctest.Wait()
		select {
		case sample := <-done:
			c := sample.Checks[0]
			if c.Status != "failed" || c.Reason != "timeout" || c.RTTMilliseconds != nil {
				t.Fatalf("body deadline: %+v", c)
			}
		default:
			t.Fatal("body read ignored deadline")
		}
	})
}

func TestProbesDNSDirectAndCanceled(t *testing.T) {
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "dns", Type: "dns", Address: "localhost"}}, Timeout: time.Second, Concurrency: 1})
	if !p.resolver.PreferGo {
		t.Fatal("DNS resolver must be pure Go")
	}
	if c := p.Sample(context.Background()).Checks[0]; c.Status != "success" {
		t.Fatalf("localhost DNS: %+v", c)
	}
	p.resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	p.targets[0].Address = "probe.invalid."
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c := p.Sample(ctx).Checks[0]; c.Status != "unavailable" || c.Reason != "canceled" {
		t.Fatalf("canceled DNS: %+v", c)
	}
	c := p.Sample(context.Background()).Checks[0]
	if c.Status != "failed" || c.Reason != "timeout" {
		t.Fatalf("DNS timeout: %+v", c)
	}
}

// A local UDP endpoint carries ICMP frames to exercise reply validation and
// packet loss deterministically without contacting another host.
type localPingConn struct {
	net.PacketConn
	remote net.Addr
}

func (c localPingConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return c.PacketConn.WriteTo(b, c.remote)
}

func TestProbesICMPEchoIdentityAndLoss(t *testing.T) {
	for _, tc := range []struct {
		name       string
		drop       bool
		wantStatus string
		wantLoss   float64
	}{
		{"all replies", false, "success", 0}, {"one reply lost", true, "failed", 100.0 / 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			defer func() { server.Close(); <-done }()
			go func() {
				defer close(done)
				for {
					packet := make([]byte, 256)
					n, addr, err := server.ReadFrom(packet)
					if err != nil {
						return
					}
					if n != 24 {
						continue
					}
					if tc.drop && packet[7] == 2 {
						continue
					}
					packet = packet[:n]
					packet[0] = 0
					wrong := append([]byte(nil), packet...)
					wrong[8] ^= 255
					server.WriteTo(wrong, addr)
					wrong = append([]byte(nil), packet...)
					wrong[4] ^= 255
					server.WriteTo(wrong, addr)
					wrong = append([]byte(nil), packet...)
					wrong[7] ^= 255
					server.WriteTo(wrong, addr)
					server.WriteTo(packet, addr)
				}
			}()
			p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "ping", Type: "icmp", Address: "127.0.0.1"}}, Timeout: 120 * time.Millisecond, Concurrency: 1})
			p.openICMP = func() (net.PacketConn, uint16, error) {
				conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
				return localPingConn{conn, server.LocalAddr()}, 1234, err
			}
			c := p.Sample(context.Background()).Checks[0]
			if c.Status != tc.wantStatus || c.LossPercent == nil || *c.LossPercent != tc.wantLoss || c.RTTMilliseconds == nil {
				t.Fatalf("ICMP: %+v", c)
			}
		})
	}
}

func TestProbesICMPUnavailable(t *testing.T) {
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "ping", Type: "icmp", Address: "127.0.0.1"}}, Timeout: time.Second, Concurrency: 1})
	p.openICMP = func() (net.PacketConn, uint16, error) { return nil, 0, errICMPUnavailable }
	c := p.Sample(context.Background()).Checks[0]
	if c.Status != "unavailable" || c.Reason != "socket_unavailable" || c.LossPercent != nil || c.RTTMilliseconds != nil {
		t.Fatalf("missing permissions: %+v", c)
	}
}

func TestProbesICMPCancelUnblocksRead(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "ping", Type: "icmp", Address: "127.0.0.1"}}, Timeout: time.Second, Concurrency: 1})
	p.openICMP = func() (net.PacketConn, uint16, error) {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		return localPingConn{conn, server.LocalAddr()}, 1234, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Sample, 1)
	go func() { done <- p.Sample(ctx) }()
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = server.ReadFrom(make([]byte, 256)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case s := <-done:
		c := s.Checks[0]
		if c.Status != "unavailable" || c.Reason != "canceled" || c.LossPercent != nil {
			t.Fatalf("canceled ICMP: %+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("ping read did not unblock")
	}
}

func TestProbesICMPRejectsForgedEcho(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func() { server.Close(); <-done }()
	go func() {
		defer close(done)
		for {
			packet := make([]byte, 256)
			n, addr, err := server.ReadFrom(packet)
			if err != nil {
				return
			}
			packet[0] = 0
			packet[8] ^= 255
			server.WriteTo(packet[:n], addr)
		}
	}()
	p := NewProbes(ProbeOptions{Targets: []ProbeTarget{{Name: "ping", Type: "icmp", Address: "127.0.0.1"}}, Timeout: 120 * time.Millisecond, Concurrency: 1})
	p.openICMP = func() (net.PacketConn, uint16, error) {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		return localPingConn{conn, server.LocalAddr()}, 1234, err
	}
	c := p.Sample(context.Background()).Checks[0]
	if c.Status != "failed" || c.RTTMilliseconds != nil || c.LossPercent == nil || *c.LossPercent != 100 {
		t.Fatalf("forged ICMP accepted: %+v", c)
	}
}
