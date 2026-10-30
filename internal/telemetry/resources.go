package telemetry

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const resourceFileLimit = 1 << 20

// Resources is owned by one sampling loop. It starts no goroutines. CPU busy
// is aggregate host utilization normalized to 0..100; cgroup CPU is used cores.
type Resources struct {
	opts     ResourceOptions
	now      func() time.Time
	previous map[string]resourceBaseline
	cpu      []uint64
	cpuAt    time.Time
}
type resourceBaseline struct {
	value uint64
	at    time.Time
}

func NewResources(opts ResourceOptions) *Resources {
	if opts.Cgroup == "" {
		opts.Cgroup = "/sys/fs/cgroup"
	}
	opts.HostInterfaces = boundedNames(opts.HostInterfaces)
	opts.ContainerInterfaces = boundedNames(opts.ContainerInterfaces)
	opts.Disks = boundedNames(opts.Disks)
	opts.Filesystems = append([]Filesystem(nil), opts.Filesystems[:min(len(opts.Filesystems), 8)]...)
	return &Resources{opts: opts, now: time.Now, previous: make(map[string]resourceBaseline)}
}
func boundedNames(names []string) []string {
	return append([]string(nil), names[:min(len(names), 8)]...)
}

func (r *Resources) Sample(ctx context.Context) Sample {
	s := Sample{Kind: "system", ObservedAt: r.now()}
	r.host(ctx, &s)
	r.cgroup(ctx, &s)
	r.network(ctx, &s, "container", "/proc/net/dev", r.opts.ContainerInterfaces)
	for _, fs := range r.opts.Filesystems {
		r.filesystem(ctx, &s, fs)
	}
	r.process(ctx, &s)
	return s
}
func addMetric(s *Sample, name, scope, resource, unit, reason string, value *float64, counter *uint64, window float64) {
	if len(s.Metrics) >= 200 {
		return
	}
	s.Metrics = append(s.Metrics, Metric{Name: name, Scope: scope, Resource: resource, Unit: unit, Available: reason == "", Reason: reason, Value: value, Counter: counter, ObservedAt: s.ObservedAt, WindowSeconds: window})
}
func addGauge(s *Sample, name, scope, resource, unit string, value float64, reason string) {
	var v *float64
	if reason == "" {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			reason = "malformed"
		} else {
			v = &value
		}
	}
	addMetric(s, name, scope, resource, unit, reason, v, nil, 0)
}
func addCounter(s *Sample, name, scope, resource, unit string, value uint64, reason string) {
	var v *uint64
	if reason == "" {
		v = &value
	}
	addMetric(s, name, scope, resource, unit, reason, nil, v, 0)
}
func sourceReason(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	return "read_failed"
}
func resourceRead(ctx context.Context, path string) (string, string) {
	if ctx.Err() != nil {
		return "", "cancelled"
	}
	if path == "" {
		return "", "not_configured"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", sourceReason(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, resourceFileLimit+1))
	if err != nil {
		return "", sourceReason(err)
	}
	if ctx.Err() != nil {
		return "", "cancelled"
	}
	if len(data) > resourceFileLimit || strings.Count(string(data), "\n") > 16384 {
		return "", "source_too_large"
	}
	return string(data), ""
}
func parseUint(s string) (uint64, string) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, "malformed"
	}
	return n, ""
}
func parseFloat(s string) (float64, string) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, "malformed"
	}
	return n, ""
}
func (r *Resources) hostPath(name string) string {
	if r.opts.HostProc == "" {
		return ""
	}
	return filepath.Join(r.opts.HostProc, name)
}
func (r *Resources) rate(s *Sample, name, scope, resource, unit string, value uint64, scale float64, reason string) {
	key := scope + "/" + resource + "/" + name
	prior, ok := r.previous[key]
	if reason != "" {
		delete(r.previous, key)
		addGauge(s, name, scope, resource, unit, 0, reason)
		return
	}
	r.previous[key] = resourceBaseline{value: value, at: s.ObservedAt}
	elapsed := s.ObservedAt.Sub(prior.at).Seconds()
	switch {
	case !ok:
		reason = "baseline_required"
	case value < prior.value:
		reason = "counter_reset"
	case elapsed <= 0:
		reason = "invalid_window"
	}
	var v *float64
	if reason == "" {
		n := float64(value-prior.value) * scale / elapsed
		v = &n
	}
	if !ok || elapsed <= 0 {
		elapsed = 0
	}
	addMetric(s, name, scope, resource, unit, reason, v, nil, elapsed)
}

func (r *Resources) host(ctx context.Context, s *Sample) {
	r.hostCPU(ctx, s)
	r.hostMemory(ctx, s)
	data, reason := resourceRead(ctx, r.hostPath("loadavg"))
	fields := strings.Fields(data)
	for i, name := range []string{"load.1m", "load.5m", "load.15m"} {
		v, why := 0.0, reason
		if why == "" {
			if len(fields) < 3 {
				why = "malformed"
			} else {
				v, why = parseFloat(fields[i])
			}
		}
		addGauge(s, name, "host", "", "load", v, why)
	}
	data, reason = resourceRead(ctx, r.hostPath("uptime"))
	fields = strings.Fields(data)
	v := 0.0
	if reason == "" {
		if len(fields) < 2 {
			reason = "malformed"
		} else {
			v, reason = parseFloat(fields[0])
		}
	}
	addGauge(s, "uptime", "host", "", "seconds", v, reason)
	r.network(ctx, s, "host", r.hostPath("1/net/dev"), r.opts.HostInterfaces)
	r.disks(ctx, s)
}
func (r *Resources) hostCPU(ctx context.Context, s *Sample) {
	data, reason := resourceRead(ctx, r.hostPath("stat"))
	var ticks []uint64
	cores := 0
	if reason == "" {
		for _, line := range strings.Split(data, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			if f[0] == "cpu" {
				if len(f) < 5 {
					reason = "malformed"
					break
				}
				for _, field := range f[1:min(len(f), 9)] {
					n, why := parseUint(field)
					if why != "" {
						reason = why
						break
					}
					ticks = append(ticks, n)
				}
			} else if strings.HasPrefix(f[0], "cpu") {
				if _, err := strconv.ParseUint(strings.TrimPrefix(f[0], "cpu"), 10, 32); err == nil {
					cores++
				}
			}
		}
		if len(ticks) < 4 || cores == 0 {
			reason = "malformed"
		}
	}
	addGauge(s, "cpu.cores", "host", "", "cores", float64(cores), reason)
	if reason != "" {
		r.cpu = nil
		addGauge(s, "cpu.busy", "host", "", "percent", 0, reason)
		return
	}
	prior, at := r.cpu, r.cpuAt
	r.cpu, r.cpuAt = ticks, s.ObservedAt
	window := s.ObservedAt.Sub(at).Seconds()
	var total, busy float64
	if len(prior) != len(ticks) {
		reason = "baseline_required"
	} else if window <= 0 {
		reason = "invalid_window"
	} else {
		for i, tick := range ticks {
			if tick < prior[i] {
				reason = "counter_reset"
				break
			}
			delta := float64(tick - prior[i])
			total += delta
			if i != 3 && i != 4 {
				busy += delta
			}
		}
		if reason == "" && total == 0 {
			reason = "insufficient_samples"
		}
	}
	var value *float64
	if reason == "" {
		v := busy / total * 100
		value = &v
	}
	if len(prior) == 0 || window <= 0 {
		window = 0
	}
	addMetric(s, "cpu.busy", "host", "", "percent", reason, value, nil, window)
}
func (r *Resources) hostMemory(ctx context.Context, s *Sample) {
	data, reason := resourceRead(ctx, r.hostPath("meminfo"))
	values := map[string]uint64{}
	bad := map[string]bool{}
	if reason == "" {
		for _, line := range strings.Split(data, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			key := strings.TrimSuffix(f[0], ":")
			if key != "MemTotal" && key != "MemAvailable" && key != "SwapTotal" && key != "SwapFree" {
				continue
			}
			if len(f) != 3 || f[2] != "kB" {
				bad[key] = true
				continue
			}
			v, why := parseUint(f[1])
			if why != "" || v > math.MaxUint64/1024 {
				bad[key] = true
				continue
			}
			values[key] = v * 1024
		}
	}
	get := func(key string) (uint64, string) {
		if reason != "" {
			return 0, reason
		}
		if bad[key] {
			return 0, "malformed"
		}
		v, ok := values[key]
		if !ok {
			return 0, "missing_field"
		}
		return v, ""
	}
	for _, def := range []struct{ name, key string }{{"memory.total", "MemTotal"}, {"memory.available", "MemAvailable"}, {"swap.total", "SwapTotal"}, {"swap.free", "SwapFree"}} {
		v, why := get(def.key)
		addGauge(s, def.name, "host", "", "bytes", float64(v), why)
	}
	for _, def := range []struct{ name, total, free string }{{"memory.used", "MemTotal", "MemAvailable"}, {"swap.used", "SwapTotal", "SwapFree"}} {
		total, why := get(def.total)
		free, other := get(def.free)
		if why == "" {
			why = other
		}
		if why == "" && free > total {
			why = "malformed"
		}
		v := float64(0)
		if why == "" {
			v = float64(total - free)
		}
		addGauge(s, def.name, "host", "", "bytes", v, why)
	}
}
func (r *Resources) cgroup(ctx context.Context, s *Sample) {
	for _, def := range []struct{ name, file string }{{"memory.current", "memory.current"}, {"memory.limit", "memory.max"}} {
		data, reason := resourceRead(ctx, filepath.Join(r.opts.Cgroup, def.file))
		var v uint64
		if reason == "" {
			if def.file == "memory.max" && strings.TrimSpace(data) == "max" {
				reason = "unbounded"
			} else {
				v, reason = parseUint(strings.TrimSpace(data))
			}
		}
		addGauge(s, def.name, "container", "", "bytes", float64(v), reason)
	}
	data, reason := resourceRead(ctx, filepath.Join(r.opts.Cgroup, "cpu.stat"))
	var v uint64
	if reason == "" {
		reason = "missing_field"
		for _, line := range strings.Split(data, "\n") {
			f := strings.Fields(line)
			if len(f) > 0 && f[0] == "usage_usec" {
				if len(f) != 2 {
					reason = "malformed"
				} else {
					v, reason = parseUint(f[1])
				}
				break
			}
		}
	}
	addCounter(s, "cpu.usage", "container", "", "microseconds", v, reason)
	r.rate(s, "cpu.usage.cores", "container", "", "cores", v, 1e-6, reason)
}
func (r *Resources) network(ctx context.Context, s *Sample, scope, path string, names []string) {
	if len(names) == 0 {
		return
	}
	data, reason := resourceRead(ctx, path)
	rows := map[string][]string{}
	if reason == "" {
		for _, line := range strings.Split(data, "\n") {
			name, rest, ok := strings.Cut(line, ":")
			if ok {
				rows[strings.TrimSpace(name)] = strings.Fields(rest)
			}
		}
	}
	for _, name := range names {
		f, ok := rows[name]
		for _, def := range []struct {
			direction string
			index     int
		}{{"receive", 0}, {"transmit", 8}} {
			why := reason
			var v uint64
			if why == "" {
				if !ok {
					why = "not_found"
				} else if len(f) != 16 {
					why = "malformed"
				} else {
					v, why = parseUint(f[def.index])
				}
			}
			addCounter(s, "network."+def.direction+".bytes", scope, name, "bytes", v, why)
			r.rate(s, "network."+def.direction+".rate", scope, name, "bytes_per_second", v, 1, why)
		}
	}
}
func (r *Resources) disks(ctx context.Context, s *Sample) {
	if len(r.opts.Disks) == 0 {
		return
	}
	data, reason := resourceRead(ctx, r.hostPath("diskstats"))
	rows := map[string][]string{}
	if reason == "" {
		for _, line := range strings.Split(data, "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 {
				rows[f[2]] = f
			}
		}
	}
	for _, name := range r.opts.Disks {
		f, ok := rows[name]
		for _, def := range []struct {
			direction string
			index     int
		}{{"read", 5}, {"write", 9}} {
			why := reason
			var v uint64
			if why == "" {
				if !ok {
					why = "not_found"
				} else if len(f) < 14 {
					why = "malformed"
				} else {
					v, why = parseUint(f[def.index])
					if v > math.MaxUint64/512 {
						why = "malformed"
					} else {
						v *= 512
					}
				}
			}
			addCounter(s, "disk."+def.direction+".bytes", "host", name, "bytes", v, why)
			r.rate(s, "disk."+def.direction+".rate", "host", name, "bytes_per_second", v, 1, why)
		}
	}
}
func (r *Resources) filesystem(ctx context.Context, s *Sample, fs Filesystem) {
	var values [6]uint64
	reason := ""
	if ctx.Err() != nil {
		reason = "cancelled"
	} else {
		values, reason = filesystemValues(ctx, fs.Path)
	}
	for i, name := range []string{"filesystem.total", "filesystem.free", "filesystem.available", "filesystem.inodes.total", "filesystem.inodes.free", "filesystem.inodes.used"} {
		unit := "bytes"
		if i >= 3 {
			unit = "inodes"
		}
		addGauge(s, name, fs.Scope, fs.Name, unit, float64(values[i]), reason)
	}
}
func (r *Resources) process(ctx context.Context, s *Sample) {
	var mem runtime.MemStats
	reason := ""
	if ctx.Err() != nil {
		reason = "cancelled"
	} else {
		runtime.ReadMemStats(&mem)
	}
	for _, def := range []struct {
		name  string
		value uint64
	}{{"go.memory.heap_alloc", mem.HeapAlloc}, {"go.memory.heap_sys", mem.HeapSys}, {"go.memory.sys", mem.Sys}} {
		addGauge(s, def.name, "process", "agent", "bytes", float64(def.value), reason)
	}
}
