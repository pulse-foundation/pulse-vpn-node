package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ExportConfig is a private, administrator-installed delivery enrollment, never
// an API response or log payload. Rotating it does not change VPN credentials.
type ExportConfig struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Endpoint             string `json:"endpoint"`
	EnrollmentGeneration string `json:"enrollmentGeneration"`
	Credential           string `json:"credential"`
}

var enrollmentUUID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func (c ExportConfig) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || c.SchemaVersion != 1 || !enrollmentUUID.MatchString(c.EnrollmentGeneration) || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Path != "/api/v1/node-ingest" || u.RawPath != "" || len(c.Endpoint) > 2048 || len(c.Credential) < 16 || len(c.Credential) > 256 {
		return errors.New("invalid telemetry enrollment")
	}
	for _, ch := range c.Credential {
		if ch < 33 || ch > 126 {
			return errors.New("invalid telemetry enrollment")
		}
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("invalid telemetry enrollment destination")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !allowedTelemetryIP(ip) {
		return errors.New("invalid telemetry enrollment destination")
	}
	if port := u.Port(); port != "" && port != "443" {
		return errors.New("invalid telemetry enrollment destination")
	}
	return nil
}
func allowedTelemetryIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func telemetryTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid telemetry destination")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("telemetry destination unavailable")
		}
		for _, ip := range ips {
			if !allowedTelemetryIP(ip) {
				return nil, errors.New("invalid telemetry destination")
			}
		}
		dialer := net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("telemetry destination unavailable")
	}
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 2
	transport.MaxConnsPerHost = 2
	transport.ResponseHeaderTimeout = 4 * time.Second
	return transport
}
