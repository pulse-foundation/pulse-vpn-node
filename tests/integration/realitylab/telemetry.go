package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"
)

// Deliberately decode the public wire contract, independently of implementation types.
type telemetryMetric struct {
	Name, Scope, Resource, Unit, Reason string
	Available                           bool
	Value                               *float64
	Counter                             *string
	ObservedAt                          time.Time
}
type telemetryCheck struct {
	Name, Type, Scope, Vantage, Status, Reason string
	RTTMilliseconds, LossPercent               *float64
	ObservedAt                                 time.Time
}
type telemetrySample struct {
	RunID      string
	Sequence   uint64 `json:"sequence,string"`
	Kind       string
	ObservedAt time.Time
	Metrics    []telemetryMetric
	Checks     []telemetryCheck
	Runtime    *struct{ CoreState, CoreVersion, UserState string }
}
type telemetryLatest struct {
	RunID      string
	Sample     *telemetrySample
	AgeSeconds *float64
	Stale      bool
}
type telemetryStats struct{ System, Runtime telemetryLatest }
type telemetryCursor struct {
	RunID string `json:"runId"`
	After uint64 `json:"after,string"`
}
type telemetryPage struct {
	RunID                    string
	FirstSequence            uint64 `json:"firstSequence,string"`
	LastSequence             uint64 `json:"lastSequence,string"`
	HeadSequence             uint64 `json:"headSequence,string"`
	Records                  []telemetrySample
	NextCursor               telemetryCursor
	Gap, RunChanged, HasMore bool
}

func telemetryAPI(path, auth string, status int, out any) error {
	req, err := http.NewRequest("GET", "http://node:8080/api/v1/"+path, nil)
	if err != nil {
		return errors.New("invalid telemetry test request")
	}
	if auth == "valid" {
		token, err := os.ReadFile(filepath.Join(fixtureDir, "api-token"))
		if err != nil {
			return errors.New("telemetry fixture token unavailable")
		}
		req.Header.Set("Authorization", "Bearer "+string(token))
	} else if auth != "" {
		req.Header.Set("Authorization", "Bearer invalid-fixture-token")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("telemetry API unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != status {
		return fmt.Errorf("telemetry API %s status: got %d, want %d", path, res.StatusCode, status)
	}
	if out != nil && json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out) != nil {
		return errors.New("invalid telemetry wire response")
	}
	return nil
}
func telemetryReadPage(cursor *telemetryCursor, limit int) (telemetryPage, error) {
	path := fmt.Sprintf("telemetry/samples?limit=%d", limit)
	if cursor != nil {
		path += fmt.Sprintf("&runId=%s&after=%d", cursor.RunID, cursor.After)
	}
	var page telemetryPage
	err := telemetryAPI(path, "valid", 200, &page)
	return page, err
}
func telemetryValidSample(s telemetrySample) error {
	id, err := hex.DecodeString(s.RunID)
	if err != nil || len(id) != 16 || s.Sequence == 0 || s.ObservedAt.IsZero() {
		return errors.New("invalid sample identity/time")
	}
	for _, m := range s.Metrics {
		if m.ObservedAt.IsZero() || m.Unit == "" {
			return errors.New("metric time/unit missing")
		}
		if !m.Available {
			if m.Value != nil || m.Counter != nil || m.Reason == "" {
				return errors.New("unavailable metric fabricated a value")
			}
		} else {
			if (m.Value == nil) == (m.Counter == nil) || m.Reason != "" {
				return errors.New("available metric has ambiguous value")
			}
			if m.Counter != nil {
				if _, err := strconv.ParseUint(*m.Counter, 10, 64); err != nil {
					return errors.New("counter is not a decimal uint64 string")
				}
			}
		}
	}
	return nil
}
func telemetryValidLatest(l telemetryLatest, kind string) error {
	if l.Sample == nil || l.AgeSeconds == nil || *l.AgeSeconds < 0 || l.Stale || l.RunID != l.Sample.RunID || l.Sample.Kind != kind {
		return errors.New("latest telemetry missing, stale, or mismatched")
	}
	return telemetryValidSample(*l.Sample)
}
func telemetryMetricFind(s telemetrySample, name, scope, resource string) (telemetryMetric, error) {
	for _, m := range s.Metrics {
		if m.Name == name && m.Scope == scope && m.Resource == resource {
			return m, nil
		}
	}
	return telemetryMetric{}, fmt.Errorf("missing telemetry metric %s/%s/%s", name, scope, resource)
}
func telemetryResources(s telemetrySample) error {
	for _, key := range []struct{ name, scope, resource string }{
		{"memory.current", "container", ""}, {"memory.limit", "container", ""}, {"cpu.usage", "container", ""}, {"cpu.usage.cores", "container", ""},
		{"network.receive.bytes", "container", "eth0"}, {"network.transmit.bytes", "container", "eth0"},
		{"filesystem.total", "container", "container-root"}, {"filesystem.available", "container", "container-root"},
		{"memory.total", "host", ""}, {"cpu.cores", "host", ""}, {"cpu.busy", "host", ""},
	} {
		m, err := telemetryMetricFind(s, key.name, key.scope, key.resource)
		if err != nil {
			return err
		}
		if !m.Available {
			return fmt.Errorf("required Linux metric unavailable: %s/%s (%s)", key.name, key.scope, m.Reason)
		}
	}
	m, err := telemetryMetricFind(s, "filesystem.total", "container", "missing-source")
	if err != nil {
		return err
	}
	if m.Available || m.Value != nil || m.Counter != nil || m.Reason != "not_found" {
		return errors.New("missing source did not remain explicitly unavailable")
	}
	return nil
}
func telemetryConnectivity(s telemetrySample) error {
	if len(s.Checks) != 4 {
		return errors.New("unexpected configured connectivity check count")
	}
	seen := map[string]bool{}
	for _, c := range s.Checks {
		if seen[c.Name] || c.Name != c.Type || c.Scope != "container" || c.Vantage != "node_direct" || c.ObservedAt.IsZero() {
			return errors.New("connectivity identity/scope/vantage mismatch")
		}
		seen[c.Name] = true
		switch c.Type {
		case "dns", "tcp", "https", "icmp":
		default:
			return errors.New("unexpected connectivity check")
		}
		if c.Type == "icmp" && c.Status == "unavailable" {
			if c.Reason != "socket_unavailable" && c.Reason != "unsupported" {
				return errors.New("unexpected ICMP unavailability reason")
			}
			if c.RTTMilliseconds != nil || c.LossPercent != nil {
				return errors.New("unavailable ICMP fabricated latency/loss")
			}
			continue
		}
		if c.Status != "success" || c.Reason != "" || c.RTTMilliseconds == nil || *c.RTTMilliseconds < 0 {
			return fmt.Errorf("local %s probe did not succeed (%s/%s)", c.Type, c.Status, c.Reason)
		}
		if c.Type == "icmp" && (c.LossPercent == nil || *c.LossPercent != 0) {
			return errors.New("local successful ICMP has invalid loss")
		}
	}
	return nil
}
func telemetryAwait() (telemetryStats, telemetryLatest, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var stats telemetryStats
		var connectivity telemetryLatest
		if err := telemetryAPI("system/stats", "valid", 200, &stats); err != nil {
			return stats, connectivity, err
		}
		if err := telemetryAPI("connectivity", "valid", 200, &connectivity); err != nil {
			return stats, connectivity, err
		}
		if telemetryValidLatest(stats.System, "system") == nil && telemetryValidLatest(stats.Runtime, "runtime") == nil && telemetryValidLatest(connectivity, "connectivity") == nil && telemetryResources(*stats.System.Sample) == nil {
			if err := telemetryConnectivity(*connectivity.Sample); err != nil {
				return stats, connectivity, err
			}
			return stats, connectivity, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return telemetryStats{}, telemetryLatest{}, errors.New("fresh Linux telemetry did not become available")
}
func telemetryCached() error {
	for attempt := 0; attempt < 20; attempt++ {
		a, c, err := telemetryAwait()
		if err != nil {
			return err
		}
		var b telemetryStats
		var d telemetryLatest
		if err = telemetryAPI("system/stats", "valid", 200, &b); err != nil {
			return err
		}
		if err = telemetryAPI("connectivity", "valid", 200, &d); err != nil {
			return err
		}
		if reflect.DeepEqual(a.System.Sample, b.System.Sample) && reflect.DeepEqual(a.Runtime.Sample, b.Runtime.Sample) && reflect.DeepEqual(c.Sample, d.Sample) {
			for _, latest := range []telemetryLatest{a.System, a.Runtime, c} {
				page, err := telemetryReadPage(&telemetryCursor{RunID: latest.RunID, After: latest.Sample.Sequence - 1}, 1)
				if err != nil {
					return err
				}
				if len(page.Records) != 1 || !reflect.DeepEqual(page.Records[0], *latest.Sample) {
					return errors.New("latest cache does not match retained sample identity/content")
				}
			}
			return nil
		}
	}
	return errors.New("latest reads did not preserve cached sample identities")
}
func telemetryPagination() error {
	first, err := telemetryReadPage(nil, 2)
	if err != nil {
		return err
	}
	if len(first.Records) != 2 || !first.HasMore || first.Gap || first.RunChanged || first.Records[0].Sequence != first.FirstSequence {
		return errors.New("initial telemetry page skipped retained history or limit")
	}
	retry, err := telemetryReadPage(nil, 2)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(first.Records, retry.Records) || first.NextCursor != retry.NextCursor {
		return errors.New("page retry changed record identities/content")
	}
	seen := map[string]bool{}
	for _, page := range []telemetryPage{first, retry} {
		for _, s := range page.Records {
			seen[fmt.Sprintf("%s/%d", s.RunID, s.Sequence)] = true
		}
	}
	if len(seen) != 2 {
		return errors.New("retry deduplication changed consumed record count")
	}
	previous := uint64(0)
	cursor := (*telemetryCursor)(nil)
	snapshotHead := first.HeadSequence
	latest := map[string]uint64{}
	for pages := 0; pages < 128; pages++ {
		page, err := telemetryReadPage(cursor, 2)
		if err != nil {
			return err
		}
		if page.RunID != first.RunID || page.RunChanged || page.Gap || len(page.Records) > 2 {
			return errors.New("unstable telemetry catch-up window")
		}
		for _, s := range page.Records {
			if err = telemetryValidSample(s); err != nil {
				return err
			}
			if s.RunID != page.RunID || s.Sequence <= previous || s.Sequence < page.FirstSequence || s.Sequence > page.LastSequence {
				return errors.New("telemetry page order/bounds mismatch")
			}
			previous = s.Sequence
			latest[s.Kind] = s.Sequence
		}
		if page.NextCursor.RunID != page.RunID || page.NextCursor.After != previous {
			return errors.New("telemetry next cursor mismatch")
		}
		cursor = &page.NextCursor
		if previous >= snapshotHead {
			break
		}
	}
	if previous < snapshotHead || len(latest) != 3 {
		return errors.New("pagination missed retained sample kinds")
	}
	path := fmt.Sprintf("telemetry/samples?runId=%s&after=18446744073709551615&limit=2", first.RunID)
	if err = telemetryAPI(path, "valid", 400, nil); err != nil {
		return err
	}
	for _, query := range []string{"limit=0", "limit=129", "runId=bad&after=0", "after=1"} {
		if err = telemetryAPI("telemetry/samples?"+query, "valid", 400, nil); err != nil {
			return err
		}
	}
	return nil
}

func telemetryDependency(value float64, after telemetryStats) (telemetryStats, error) {
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		var stats telemetryStats
		if err := telemetryAPI("system/stats", "valid", 200, &stats); err != nil {
			return stats, err
		}
		if telemetryValidLatest(stats.System, "system") == nil && telemetryValidLatest(stats.Runtime, "runtime") == nil && stats.System.Sample.Sequence > after.System.Sample.Sequence && stats.Runtime.Sample.Sequence > after.Runtime.Sample.Sequence {
			for _, m := range stats.Runtime.Sample.Metrics {
				if m.Name == "database.reachable" && m.Scope == "process" && m.Available && m.Value != nil && *m.Value == value {
					if err := telemetryResources(*stats.System.Sample); err != nil {
						return stats, err
					}
					return stats, nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return telemetryStats{}, errors.New("dependency telemetry did not reflect database transition with new resource samples")
}
func liveTelemetry() error {
	if err := telemetryDeliveryCheck(); err != nil {
		return err
	}
	for _, path := range []string{"system/stats", "connectivity", "telemetry/samples"} {
		for _, auth := range []string{"", "invalid"} {
			if err := telemetryAPI(path, auth, 401, nil); err != nil {
				return err
			}
		}
	}
	if err := probe(); err != nil {
		return err
	}
	stats, connectivity, err := telemetryAwait()
	if err != nil {
		return err
	}
	if err = telemetryCached(); err != nil {
		return err
	}
	if err = telemetryPagination(); err != nil {
		return err
	}
	fmt.Println("PASS: bearer auth, cached sample identities, bounded ordered pagination, retry deduplication and invalid cursors")
	fmt.Println("PASS: Linux container CPU/memory/network/filesystem and mounted host CPU/memory scopes; missing source is null with reason")
	fmt.Println("PASS: local direct DNS/TCP/verified HTTPS probes; ICMP success or explicit unavailable with null measurements")
	sessions, err := openSessions("client:7890")
	if err != nil {
		return err
	}
	defer sessions.close()
	if err = coordinate("database outage"); err != nil {
		return err
	}
	// Capture the post-stop cache, then require fresh publications while DB is down.
	if err = telemetryAPI("system/stats", "valid", 200, &stats); err != nil {
		return err
	}
	if err = telemetryAPI("connectivity", "valid", 200, &connectivity); err != nil {
		return err
	}
	outage, err := telemetryDependency(0, stats)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	continued := false
	for time.Now().Before(deadline) {
		var next telemetryLatest
		if err = telemetryAPI("connectivity", "valid", 200, &next); err != nil {
			return err
		}
		if telemetryValidLatest(next, "connectivity") == nil && next.Sample.Sequence > connectivity.Sample.Sequence {
			if err = telemetryConnectivity(*next.Sample); err != nil {
				return err
			}
			continued = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !continued {
		return errors.New("connectivity sampling stopped during database outage")
	}
	if err = sessions.echo(); err != nil {
		return err
	}
	if err = probe(); err != nil {
		return err
	}
	if err = coordinate("database recovery"); err != nil {
		return err
	}
	if _, err = telemetryDependency(1, outage); err != nil {
		return err
	}
	if err = sessions.echo(); err != nil {
		return err
	}
	fmt.Println("PASS: database outage/recovery changes dependency status while new resources, direct probes and existing VPN TCP/UDP continue")
	page, err := telemetryReadPage(nil, 2)
	if err != nil {
		return err
	}
	data, err := json.Marshal(telemetryCursor{RunID: page.RunID, After: page.HeadSequence})
	if err != nil {
		return errors.New("cannot encode restart cursor")
	}
	if err = os.WriteFile("/telemetry-baseline/cursor.json", data, 0600); err != nil {
		return errors.New("cannot save restart cursor")
	}
	return nil
}
func persistedTelemetry() error {
	data, err := os.ReadFile("/telemetry-baseline/cursor.json")
	if err != nil {
		return errors.New("restart cursor unavailable")
	}
	var cursor telemetryCursor
	if json.Unmarshal(data, &cursor) != nil || cursor.RunID == "" || cursor.After == 0 {
		return errors.New("restart cursor invalid")
	}
	if _, _, err = telemetryAwait(); err != nil {
		return err
	}
	page, err := telemetryReadPage(&cursor, 2)
	if err != nil {
		return err
	}
	if !page.RunChanged || page.RunID == cursor.RunID || len(page.Records) != 2 || page.Records[0].Sequence != page.FirstSequence || !page.HasMore || page.NextCursor.After != page.Records[1].Sequence {
		return errors.New("restart did not explicitly resume from earliest retained record of new run")
	}
	if err = probe(); err != nil {
		return err
	}
	fmt.Println("PASS: old cursor after node restart reports runChanged and earliest retained records, preserving VPN service")
	return nil
}
