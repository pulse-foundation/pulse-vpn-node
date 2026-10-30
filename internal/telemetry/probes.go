package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Probes samples explicitly configured direct-egress targets. The application
// owns its sequential sampling loop; each call joins its bounded workers.
type Probes struct {
	targets     []ProbeTarget
	timeout     time.Duration
	concurrency int
	resolver    *net.Resolver
	dialer      *net.Dialer
	transport   *http.Transport
	openICMP    func() (net.PacketConn, uint16, error)
}

// NewProbes takes startup-validated configuration, copying target ownership.
// Bounds also apply here so another internal caller cannot create unbounded work.
func NewProbes(opts ProbeOptions) *Probes {
	timeout := opts.Timeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 4 {
		concurrency = 4
	}
	targets := opts.Targets
	if len(targets) > 16 {
		targets = targets[:16]
	}
	resolver := &net.Resolver{PreferGo: true}
	dialer := &net.Dialer{Timeout: timeout, Resolver: resolver}
	return &Probes{
		targets: append([]ProbeTarget(nil), targets...), timeout: timeout, concurrency: concurrency, resolver: resolver, dialer: dialer,
		transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, DisableCompression: true,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 4096},
		openICMP: openPingSocket,
	}
}

func (p *Probes) Sample(ctx context.Context) Sample {
	checks := make([]Check, len(p.targets))
	var workers sync.WaitGroup
	for worker := 0; worker < p.concurrency && worker < len(p.targets); worker++ {
		workers.Add(1)
		go func(start int) {
			defer workers.Done()
			for i := start; i < len(p.targets); i += p.concurrency {
				checks[i] = p.check(ctx, p.targets[i])
			}
		}(worker)
	}
	workers.Wait()
	p.transport.CloseIdleConnections()
	return Sample{Kind: "connectivity", ObservedAt: time.Now().UTC(), Checks: checks}
}

func (p *Probes) check(parent context.Context, target ProbeTarget) Check {
	c := Check{Name: target.Name, Type: target.Type, Scope: "container", Vantage: "node_direct", Status: "success"}
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	started := time.Now()
	var err error
	if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		switch target.Type {
		case "dns":
			var addresses []string
			addresses, err = p.resolver.LookupHost(ctx, target.Address)
			if err == nil && len(addresses) == 0 {
				err = errNoDNSAnswers
			}
		case "tcp":
			var conn net.Conn
			conn, err = p.dialer.DialContext(ctx, "tcp", target.Address)
			if err == nil {
				err = conn.Close()
			}
		case "https":
			err = p.https(ctx, target.Address)
		case "icmp":
			c.RTTMilliseconds, c.LossPercent, err = p.icmp(ctx, target.Address)
		default:
			err = errUnsupportedProbe
		}
	}
	if err != nil {
		c.Status, c.Reason = probeFailure(ctx, target.Type, err)
		// A canceled attempt is not a packet-loss measurement.
		if c.Status == "unavailable" {
			c.RTTMilliseconds = nil
			c.LossPercent = nil
		}
	} else if target.Type != "icmp" {
		elapsed := float64(time.Since(started)) / float64(time.Millisecond)
		c.RTTMilliseconds = &elapsed
	}
	c.ObservedAt = time.Now().UTC()
	return c
}

var (
	errNoDNSAnswers     = errors.New("no DNS answers")
	errHTTPStatus       = errors.New("HTTP status")
	errInvalidTarget    = errors.New("invalid target")
	errUnsupportedProbe = errors.New("unsupported probe")
	errICMPUnavailable  = errors.New("ICMP socket unavailable")
	errPacketLoss       = errors.New("ICMP packet loss")
)

func (p *Probes) https(ctx context.Context, address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errInvalidTarget
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errInvalidTarget
	}
	client := &http.Client{Transport: p.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errHTTPStatus
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return err
}

func probeFailure(ctx context.Context, kind string, err error) (string, string) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "unavailable", "canceled"
	}
	if errors.Is(err, errICMPUnavailable) {
		return "unavailable", "socket_unavailable"
	}
	if errors.Is(err, errUnsupportedProbe) {
		return "unavailable", "unsupported"
	}
	if errors.Is(err, errInvalidTarget) {
		return "unavailable", "invalid_target"
	}
	if errors.Is(err, errPacketLoss) {
		return "failed", "packet_loss"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "failed", "timeout"
	}
	var timed net.Error
	if errors.As(err, &timed) && timed.Timeout() {
		return "failed", "timeout"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "failed", "tls_failed"
	}
	if errors.Is(err, errHTTPStatus) {
		return "failed", "http_status"
	}
	if kind == "dns" {
		return "failed", "dns_failed"
	}
	return "failed", "connection_failed"
}

// icmp sends three nonce-tagged echoes over a Linux unprivileged ping socket.
// Each echo gets one third of the remaining deadline. Socket close on context
// cancellation unblocks I/O; the callback is joined before returning.
func (p *Probes) icmp(ctx context.Context, address string) (*float64, *float64, error) {
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil {
		return nil, nil, errInvalidTarget
	}
	conn, id, err := p.openICMP()
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, nil, errICMPUnavailable
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, nil, errICMPUnavailable
	}
	received := 0
	var totalRTT time.Duration
	for sequence := uint16(1); sequence <= 3; sequence++ {
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		echoDeadline := time.Now().Add(time.Until(deadline) / time.Duration(4-sequence))
		if err = conn.SetDeadline(echoDeadline); err != nil {
			return nil, nil, errICMPUnavailable
		}
		packet := make([]byte, 24)
		packet[0] = 8
		binary.BigEndian.PutUint16(packet[4:6], id)
		binary.BigEndian.PutUint16(packet[6:8], sequence)
		copy(packet[8:], nonce[:])
		binary.BigEndian.PutUint16(packet[2:4], icmpChecksum(packet))
		sent := time.Now()
		if _, err = conn.WriteTo(packet, &net.UDPAddr{IP: ip}); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, errICMPUnavailable
		}
		for {
			buffer := make([]byte, 256)
			n, source, readErr := conn.ReadFrom(buffer)
			if readErr != nil {
				if ctx.Err() == context.Canceled {
					return nil, nil, ctx.Err()
				}
				// The context's close callback may beat the socket deadline.
				// A completed echo window is still measured loss in that case.
				if ctx.Err() == context.DeadlineExceeded {
					break
				}
				var timed net.Error
				if errors.As(readErr, &timed) && timed.Timeout() {
					break
				}
				return nil, nil, errICMPUnavailable
			}
			sourceIP := packetSourceIP(source)
			if n != 24 || !sourceIP.Equal(ip) || buffer[0] != 0 || buffer[1] != 0 || binary.BigEndian.Uint16(buffer[4:6]) != id || binary.BigEndian.Uint16(buffer[6:8]) != sequence || string(buffer[8:n]) != string(nonce[:]) {
				continue
			}
			received++
			totalRTT += time.Since(sent)
			break
		}
	}
	loss := float64(3-received) * 100 / 3
	var rtt *float64
	if received > 0 {
		ms := float64(totalRTT) / float64(time.Millisecond) / float64(received)
		rtt = &ms
	}
	if received != 3 {
		return rtt, &loss, errPacketLoss
	}
	return rtt, &loss, nil
}

func packetSourceIP(address net.Addr) net.IP {
	switch a := address.(type) {
	case *net.UDPAddr:
		return a.IP
	case *net.IPAddr:
		return a.IP
	default:
		return nil
	}
}

func icmpChecksum(packet []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(packet); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[i : i+2]))
	}
	if len(packet)%2 != 0 {
		sum += uint32(packet[len(packet)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
