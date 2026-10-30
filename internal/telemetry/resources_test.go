package telemetry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func metric(t *testing.T, s Sample, name, scope, resource string) Metric {
	t.Helper()
	for _, m := range s.Metrics {
		if m.Name == name && m.Scope == scope && m.Resource == resource {
			return m
		}
	}
	t.Fatalf("missing metric %s/%s/%s", name, scope, resource)
	return Metric{}
}
func gaugeIs(t *testing.T, m Metric, want float64) {
	t.Helper()
	if !m.Available || m.Value == nil || *m.Value != want {
		t.Fatalf("metric %s = %+v, want %v", m.Name, m, want)
	}
}
func TestResourcesHostFixturesAndRates(t *testing.T) {
	p, c := t.TempDir(), t.TempDir()
	fixture(t, p, "stat", "cpu 100 0 100 700 100 0 0 0 0 0\ncpu0 0\ncpu1 0\n")
	fixture(t, p, "meminfo", "MemTotal: 1000 kB\nMemAvailable: 400 kB\nSwapTotal: 200 kB\nSwapFree: 50 kB\n")
	fixture(t, p, "loadavg", "1.5 2.5 3.5 1/100 123\n")
	fixture(t, p, "uptime", "123.5 44\n")
	fixture(t, p, "diskstats", "8 0 sda 1 0 10 0 1 0 20 0 0 0 0\n")
	fixture(t, p, "1/net/dev", "eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n")
	fixture(t, p, "net/dev", "eth0: 9999 0 0 0 0 0 0 0 9999 0 0 0 0 0 0 0\n")
	fixture(t, c, "memory.current", "100\n")
	fixture(t, c, "memory.max", "max\n")
	fixture(t, c, "cpu.stat", "usage_usec 1000000\n")
	r := NewResources(ResourceOptions{HostProc: p, Cgroup: c, HostInterfaces: []string{"eth0"}, Disks: []string{"sda"}})
	now := time.Now()
	r.now = func() time.Time { return now }
	s := r.Sample(context.Background())
	gaugeIs(t, metric(t, s, "cpu.cores", "host", ""), 2)
	gaugeIs(t, metric(t, s, "memory.used", "host", ""), 600*1024)
	gaugeIs(t, metric(t, s, "load.5m", "host", ""), 2.5)
	gaugeIs(t, metric(t, s, "uptime", "host", ""), 123.5)
	m := metric(t, s, "network.receive.bytes", "host", "eth0")
	if m.Counter == nil || *m.Counter != 100 {
		t.Fatalf("wrong network namespace: %+v", m)
	}
	if m := metric(t, s, "cpu.busy", "host", ""); m.Available || m.Reason != "baseline_required" {
		t.Fatalf("first CPU sample: %+v", m)
	}
	if m := metric(t, s, "memory.limit", "container", ""); m.Available || m.Reason != "unbounded" {
		t.Fatalf("memory max: %+v", m)
	}
	now = now.Add(2 * time.Second)
	fixture(t, p, "stat", "cpu 200 0 100 800 100 0 0 0 0 0\ncpu0 0\ncpu1 0\n")
	fixture(t, p, "1/net/dev", "eth0: 300 0 0 0 0 0 0 0 600 0 0 0 0 0 0 0\n")
	fixture(t, c, "cpu.stat", "usage_usec 2000000\n")
	s = r.Sample(context.Background())
	gaugeIs(t, metric(t, s, "cpu.busy", "host", ""), 50)
	gaugeIs(t, metric(t, s, "network.receive.rate", "host", "eth0"), 100)
	gaugeIs(t, metric(t, s, "cpu.usage.cores", "container", ""), 0.5)
	now = now.Add(time.Second)
	fixture(t, p, "1/net/dev", "eth0: 1 0 0 0 0 0 0 0 2 0 0 0 0 0 0 0\n")
	if m := metric(t, r.Sample(context.Background()), "network.receive.rate", "host", "eth0"); m.Available || m.Reason != "counter_reset" {
		t.Fatalf("reset: %+v", m)
	}
	now = now.Add(time.Second)
	fixture(t, p, "1/net/dev", "eth0: 11 0 0 0 0 0 0 0 12 0 0 0 0 0 0 0\n")
	gaugeIs(t, metric(t, r.Sample(context.Background()), "network.receive.rate", "host", "eth0"), 10)
}
func TestResourcesUnavailableDoesNotFabricateHost(t *testing.T) {
	r := NewResources(ResourceOptions{Cgroup: t.TempDir(), HostInterfaces: []string{"eth0"}, Disks: []string{"sda"}})
	s := r.Sample(context.Background())
	for _, m := range s.Metrics {
		if m.Scope == "host" && (m.Available || m.Value != nil || m.Counter != nil || m.Reason != "not_configured") {
			t.Fatalf("fabricated host: %+v", m)
		}
	}
}
func TestResourcesMalformedAndRecovery(t *testing.T) {
	p := t.TempDir()
	r := NewResources(ResourceOptions{HostProc: p, Cgroup: t.TempDir(), HostInterfaces: []string{"eth0"}})
	fixture(t, p, "meminfo", "MemTotal: 100 kB\nMemAvailable: bad kB\nSwapTotal: 50 kB\nSwapFree: 20 kB\n")
	s := r.Sample(context.Background())
	gaugeIs(t, metric(t, s, "memory.total", "host", ""), 102400)
	if m := metric(t, s, "memory.available", "host", ""); m.Available || m.Reason != "malformed" {
		t.Fatalf("bad memory: %+v", m)
	}
	fixture(t, p, "1/net/dev", "eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n")
	r.Sample(context.Background())
	fixture(t, p, "1/net/dev", "eth0: malformed\n")
	r.Sample(context.Background())
	fixture(t, p, "1/net/dev", "eth0: 200 0 0 0 0 0 0 0 300 0 0 0 0 0 0 0\n")
	if m := metric(t, r.Sample(context.Background()), "network.receive.rate", "host", "eth0"); m.Available || m.Reason != "baseline_required" {
		t.Fatalf("recovery: %+v", m)
	}
}
func TestResourcesBoundsCancellationAndFilesystem(t *testing.T) {
	p := t.TempDir()
	fixture(t, p, "stat", string(make([]byte, (1<<20)+1)))
	r := NewResources(ResourceOptions{HostProc: p, Cgroup: t.TempDir(), Filesystems: []Filesystem{{Name: "missing", Path: filepath.Join(t.TempDir(), "absent"), Scope: "host"}}})
	s := r.Sample(context.Background())
	if len(s.Metrics) > 200 {
		t.Fatal("unbounded metrics")
	}
	if m := metric(t, s, "cpu.busy", "host", ""); m.Available || m.Reason != "source_too_large" {
		t.Fatalf("large source: %+v", m)
	}
	if m := metric(t, s, "filesystem.total", "host", "missing"); m.Available || (runtime.GOOS == "linux" && m.Reason != "not_found") || (runtime.GOOS != "linux" && m.Reason != "unsupported") {
		t.Fatalf("filesystem: %+v", m)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, m := range r.Sample(ctx).Metrics {
		if m.Available {
			t.Fatalf("cancelled sample: %+v", m)
		}
	}
}

func TestResourcesCPUResetAndIdleWait(t *testing.T) {
	p := t.TempDir()
	r := NewResources(ResourceOptions{HostProc: p, Cgroup: t.TempDir()})
	now := time.Now()
	r.now = func() time.Time { return now }
	states := []struct {
		stat   string
		value  float64
		reason string
	}{
		{"100 0 100 700 100 0 0 0 200 50", 0, "baseline_required"},
		{"100 0 100 700 200 0 0 0 300 100", 0, ""},
		{"50 0 100 800 200 0 0 0 300 100", 0, "counter_reset"},
		{"150 0 100 800 200 0 0 0 300 100", 100, ""},
	}
	for _, state := range states {
		fixture(t, p, "stat", "cpu "+state.stat+"\ncpu0 0\n")
		now = now.Add(time.Second)
		m := metric(t, r.Sample(context.Background()), "cpu.busy", "host", "")
		if state.reason != "" {
			if m.Available || m.Reason != state.reason {
				t.Fatalf("CPU state: %+v", m)
			}
		} else {
			gaugeIs(t, m, state.value)
		}
	}
}
func TestResourcesDiskOverflowAndPartialNetwork(t *testing.T) {
	p := t.TempDir()
	r := NewResources(ResourceOptions{HostProc: p, Cgroup: t.TempDir(), Disks: []string{"sda"}, HostInterfaces: []string{"eth0", "eth1"}})
	now := time.Now()
	r.now = func() time.Time { return now }
	fixture(t, p, "diskstats", "8 0 sda 1 0 18446744073709551615 0 1 0 20 0 0 0 0\n")
	fixture(t, p, "1/net/dev", "eth0: 123 0 0 0 0 0 0 0 321 0 0 0 0 0 0 0\neth1: malformed\n")
	s := r.Sample(context.Background())
	if m := metric(t, s, "disk.read.bytes", "host", "sda"); m.Available || m.Reason != "malformed" {
		t.Fatalf("overflow: %+v", m)
	}
	if m := metric(t, s, "disk.write.bytes", "host", "sda"); !m.Available || m.Counter == nil || *m.Counter != 10240 {
		t.Fatalf("sectors: %+v", m)
	}
	if m := metric(t, s, "network.receive.bytes", "host", "eth0"); !m.Available {
		t.Fatalf("partial network: %+v", m)
	}
	if m := metric(t, s, "network.receive.bytes", "host", "eth1"); m.Available || m.Reason != "malformed" {
		t.Fatalf("malformed network: %+v", m)
	}
	now = now.Add(2 * time.Second)
	fixture(t, p, "diskstats", "8 0 sda 1 0 10 0 1 0 22 0 0 0 0\n")
	gaugeIs(t, metric(t, r.Sample(context.Background()), "disk.write.rate", "host", "sda"), 512)
}
func TestResourcesMaximumSelectionBound(t *testing.T) {
	var names []string
	var filesystems []Filesystem
	root := t.TempDir()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("resource%d", i)
		names = append(names, name)
		filesystems = append(filesystems, Filesystem{Name: name, Path: root, Scope: "container"})
	}
	r := NewResources(ResourceOptions{HostProc: root, Cgroup: root, Disks: names, HostInterfaces: names, ContainerInterfaces: names, Filesystems: filesystems})
	// Cancelled sampling exercises the full configured metric shape without reading real sources.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := r.Sample(ctx)
	if len(s.Metrics) > 200 {
		t.Fatalf("metric bound: %d", len(s.Metrics))
	}
	for _, m := range s.Metrics {
		if m.Resource == "resource8" {
			t.Fatal("selection exceeded bound")
		}
	}
}
func TestResourcesFilesystemNamedScope(t *testing.T) {
	root := t.TempDir()
	s := NewResources(ResourceOptions{Cgroup: t.TempDir(), Filesystems: []Filesystem{{Name: "data", Path: root, Scope: "container"}}}).Sample(context.Background())
	m := metric(t, s, "filesystem.total", "container", "data")
	if runtime.GOOS == "linux" {
		if !m.Available || m.Value == nil || *m.Value <= 0 {
			t.Fatalf("filesystem capacity: %+v", m)
		}
	} else if m.Reason != "unsupported" {
		t.Fatalf("unsupported filesystem: %+v", m)
	}
	for _, m := range s.Metrics {
		if strings.Contains(m.Reason, root) || strings.Contains(m.Resource, root) {
			t.Fatal("filesystem path leaked")
		}
	}
}
func TestResourcesPermissionReason(t *testing.T) {
	// Wrapped filesystem errors are classified without returning the sensitive path.
	err := &os.PathError{Op: "open", Path: "/private-sensitive-mount", Err: os.ErrPermission}
	if got := sourceReason(err); got != "permission_denied" {
		t.Fatalf("permission classification: %s", got)
	}
}
