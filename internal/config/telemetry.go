package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

type Telemetry struct {
	SampleIntervalSeconds    int                       `json:"sampleIntervalSeconds"`
	ProbeIntervalSeconds     int                       `json:"probeIntervalSeconds"`
	ProbeTimeoutMilliseconds int                       `json:"probeTimeoutMilliseconds"`
	Resources                telemetry.ResourceOptions `json:"resources"`
	Probes                   telemetry.ProbeOptions    `json:"probes"`
}

func DefaultTelemetry() Telemetry {
	return Telemetry{SampleIntervalSeconds: 5, ProbeIntervalSeconds: 30, ProbeTimeoutMilliseconds: 2000,
		Resources: telemetry.ResourceOptions{Cgroup: "/sys/fs/cgroup", ContainerInterfaces: []string{"eth0"}, Filesystems: []telemetry.Filesystem{{Name: "container-root", Path: "/", Scope: "container"}}},
		Probes:    telemetry.ProbeOptions{Targets: []telemetry.ProbeTarget{}, Concurrency: 4, Timeout: 2 * time.Second}}
}

var telemetryName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,64}$`)

func loadTelemetry(path string) (Telemetry, error) {
	cfg := DefaultTelemetry()
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return Telemetry{}, errors.New("cannot open telemetry settings")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16385))
		if err != nil || len(data) > 16384 || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
			return Telemetry{}, errors.New("invalid telemetry settings")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&cfg); err != nil {
			return Telemetry{}, errors.New("invalid telemetry settings")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return Telemetry{}, errors.New("invalid telemetry settings")
		}
	}
	if err := cfg.Validate(); err != nil {
		return Telemetry{}, err
	}
	cfg.Probes.Timeout = time.Duration(cfg.ProbeTimeoutMilliseconds) * time.Millisecond
	return cfg, nil
}

func (c Telemetry) Validate() error {
	invalid := errors.New("invalid telemetry settings")
	if c.SampleIntervalSeconds < 1 || c.SampleIntervalSeconds > 60 || c.ProbeIntervalSeconds < 5 || c.ProbeIntervalSeconds > 300 || c.ProbeTimeoutMilliseconds < 100 || c.ProbeTimeoutMilliseconds > 5000 || c.Probes.Concurrency < 1 || c.Probes.Concurrency > 4 {
		return invalid
	}
	validPath := func(p string) bool {
		return len(p) <= 256 && filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
	}
	if (c.Resources.HostProc != "" && !validPath(c.Resources.HostProc)) || !validPath(c.Resources.Cgroup) {
		return invalid
	}
	for _, names := range [][]string{c.Resources.HostInterfaces, c.Resources.ContainerInterfaces, c.Resources.Disks} {
		if len(names) > 8 {
			return invalid
		}
		seen := map[string]bool{}
		for _, name := range names {
			if !telemetryName.MatchString(name) || seen[name] {
				return invalid
			}
			seen[name] = true
		}
	}
	if len(c.Resources.Filesystems) > 8 || len(c.Probes.Targets) > 16 {
		return invalid
	}
	seen := map[string]bool{}
	for _, fs := range c.Resources.Filesystems {
		if !telemetryName.MatchString(fs.Name) || seen[fs.Name] || !validPath(fs.Path) || (fs.Scope != "host" && fs.Scope != "container") {
			return invalid
		}
		seen[fs.Name] = true
	}
	seen = map[string]bool{}
	for _, target := range c.Probes.Targets {
		if !telemetryName.MatchString(target.Name) || seen[target.Name] || len(target.Address) > 256 {
			return invalid
		}
		seen[target.Name] = true
		switch target.Type {
		case "dns":
			if !validTelemetryHost(target.Address) || net.ParseIP(target.Address) != nil {
				return invalid
			}
		case "tcp":
			host, port, err := net.SplitHostPort(target.Address)
			n, e := strconv.Atoi(port)
			if err != nil || e != nil || n < 1 || n > 65535 || !validTelemetryHost(host) {
				return invalid
			}
		case "https":
			u, err := url.Parse(target.Address)
			if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || !validTelemetryHost(u.Hostname()) {
				return invalid
			}
			if u.Port() != "" {
				n, e := strconv.Atoi(u.Port())
				if e != nil || n < 1 || n > 65535 {
					return invalid
				}
			}
		case "icmp":
			ip := net.ParseIP(target.Address)
			if ip == nil || ip.To4() == nil {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}

func validTelemetryHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}
