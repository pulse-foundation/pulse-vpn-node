package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExporterNormalizesAllObservationTimestampsToUTC(t *testing.T) {
	heartbeat := "0"
	observed := 0
	exporter, buffer := exporterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad request")
			return
		}
		var check func(any)
		check = func(value any) {
			switch v := value.(type) {
			case map[string]any:
				for key, item := range v {
					if key == "observedAt" || key == "sentAt" {
						at, ok := item.(string)
						if !ok || !strings.HasSuffix(at, "Z") {
							t.Errorf("non-UTC observation in %s", r.URL.Path)
						}
						observed++
					}
					check(item)
				}
			case []any:
				for _, item := range v {
					check(item)
				}
			}
		}
		check(body)
		cursor := "0"
		if strings.HasSuffix(r.URL.Path, "heartbeat") {
			heartbeat = body["heartbeatSequence"].(string)
		}
		if strings.HasSuffix(r.URL.Path, "samples") {
			cursor = "2"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "sessionId": "8a80bd53-dc79-46a8-ad11-a8fa066148e2", "runId": body["runId"], "committedSequence": cursor, "heartbeatSequence": heartbeat, "leaseExpiresAt": time.Now().Add(90 * time.Second).UTC()})
	})
	at := time.Now().In(time.FixedZone("non-UTC", 3*60*60))
	metric := Metric{Name: "memory.total", Scope: "host", Unit: "bytes", Available: false, Reason: "not_found", ObservedAt: at}
	check := Check{Name: "dns", Type: "dns", Scope: "container", Vantage: "node_direct", Status: "unavailable", Reason: "unsupported", ObservedAt: at}
	for _, sample := range []Sample{{Kind: "system", ObservedAt: at, Metrics: []Metric{metric}}, {Kind: "connectivity", ObservedAt: at, Checks: []Check{check}}} {
		if err := buffer.Publish(sample); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := exporter.register(ctx); err != nil {
		t.Fatal(err)
	}
	if err := exporter.sendHeartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.sendHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if observed < 9 {
		t.Fatal("missing nested timestamp coverage")
	}
	latest := buffer.Latest("system", time.Minute)
	if latest.Sample.ObservedAt.Location() != at.Location() || latest.Sample.Metrics[0].ObservedAt.Location() != at.Location() {
		t.Fatal("export mutated retained observations")
	}
}

func TestFailureJitterVariesPlateauAndHonorsRetryAfter(t *testing.T) {
	low, high := time.Minute, time.Minute
	first := nextFailureDelay(&deliveryError{code: "unavailable"}, &low, func() uint64 { return 0 })
	second := nextFailureDelay(&deliveryError{code: "unavailable"}, &high, func() uint64 { return uint64(30 * time.Second) })
	if first != 30*time.Second || second != time.Minute {
		t.Fatalf("missing bounded retry jitter: %v %v", first, second)
	}
	backoff := time.Second
	if delay := nextFailureDelay(&deliveryError{retry: time.Minute}, &backoff, func() uint64 { return 0 }); delay != time.Minute {
		t.Fatalf("Retry-After ignored: %v", delay)
	}
}

func TestHeartbeatBuildIdentityIsNotInSamples(t *testing.T) {
	exporter, _ := exporterFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	data, err := json.Marshal(exporter.heartbeatState())
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	build, ok := state["build"].(map[string]any)
	if !ok || build["version"] != "dev" || build["commit"] != "unknown" || build["architecture"] == "" {
		t.Fatalf("missing build identity: %s", data)
	}
	data, err = json.Marshal(Sample{Kind: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"build"`) {
		t.Fatal("build identity in raw samples")
	}
}
