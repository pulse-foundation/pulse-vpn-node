package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type exportRoundTrip func(*http.Request) (*http.Response, error)

func (f exportRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const exportTestSession = "8a80bd53-dc79-46a8-ad11-a8fa066148e2"

func exportResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}
func exportTestConfig() ExportConfig {
	return ExportConfig{SchemaVersion: 1, Endpoint: "https://receiver.example/api/v1/node-ingest", EnrollmentGeneration: "4c9bc671-b3e3-40f9-89ab-cf8682ce7f8e", Credential: "synthetic-sender-credential"}
}
func exportSample() Sample {
	v := float64(1)
	return Sample{Kind: "system", ObservedAt: time.Now().UTC(), Metrics: []Metric{{Name: "memory.used", Scope: "host", Unit: "bytes", Available: true, Value: &v, ObservedAt: time.Now().UTC()}}}
}
func exportACKFor(body map[string]json.RawMessage, cursor, heartbeat string) exportACK {
	var run string
	_ = json.Unmarshal(body["runId"], &run)
	return exportACK{SchemaVersion: 1, SessionID: exportTestSession, RunID: run, CommittedSequence: cursor, HeartbeatSequence: heartbeat, LeaseExpiresAt: time.Now().Add(90 * time.Second).UTC()}
}
func startTestExporter(t *testing.T, buffer *Buffer, transport http.RoundTripper) (*Exporter, func()) {
	t.Helper()
	exporter, err := NewExporter(exportTestConfig(), buffer, ExporterOptions{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); exporter.Run(ctx) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return exporter, stop
}
func TestExporterBacklogHasFreshIndependentHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBuffer()
		_ = b.Publish(exportSample())
		var mu sync.Mutex
		heartbeats := []string{}
		historyAttempts := 0
		exporter, _ := startTestExporter(t, b, exportRoundTrip(func(r *http.Request) (*http.Response, error) {
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			defer mu.Unlock()
			hb := "0"
			if len(heartbeats) > 0 {
				hb = heartbeats[len(heartbeats)-1]
			}
			switch r.URL.Path {
			case "/api/v1/node-ingest/heartbeat":
				_ = json.Unmarshal(body["heartbeatSequence"], &hb)
				heartbeats = append(heartbeats, hb)
			case "/api/v1/node-ingest/samples":
				historyAttempts++
				response := exportResponse(503, map[string]string{"code": "backpressure"})
				response.Header.Set("Retry-After", "60")
				return response, nil
			}
			return exportResponse(200, exportACKFor(body, "0", hb)), nil
		}))
		time.Sleep(100 * time.Second)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if len(heartbeats) < 4 || historyAttempts > 2 {
			t.Fatalf("heartbeat blocked by history: heartbeats=%d history=%d", len(heartbeats), historyAttempts)
		}
		for i := 1; i < len(heartbeats); i++ {
			a, _ := decimalSequence(heartbeats[i-1])
			z, _ := decimalSequence(heartbeats[i])
			if z <= a {
				t.Fatal("heartbeat sequence did not advance")
			}
		}
		status := exporter.Status()
		if status.HistoryError != "backpressure" || status.HeartbeatError != "" || status.LastHeartbeatAt == nil {
			t.Fatalf("incorrect independent delivery status: %+v", status)
		}
	})
}
func TestExporterRejectsACKRegression(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBuffer()
		_ = b.Publish(exportSample())
		exporter, _ := startTestExporter(t, b, exportRoundTrip(func(r *http.Request) (*http.Response, error) {
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			cursor, hb := "0", "0"
			if strings.HasSuffix(r.URL.Path, "heartbeat") {
				_ = json.Unmarshal(body["heartbeatSequence"], &hb)
			}
			if strings.HasSuffix(r.URL.Path, "samples") {
				cursor = "1"
			}
			return exportResponse(200, exportACKFor(body, cursor, hb)), nil
		}))
		time.Sleep(time.Second)
		synctest.Wait()
		status := exporter.Status()
		if status.HistoryError != "invalid_response" || status.CommittedSequence != "0" || status.HeartbeatSequence != "1" {
			t.Fatalf("regressed ACK was committed: %+v", status)
		}
	})
}
func TestExporterBufferGapReportedAndBatchBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBuffer()
		for i := 0; i < MaxRecords+1; i++ {
			_ = b.Publish(exportSample())
		}
		var mu sync.Mutex
		var gap *exportGap
		pages := 0
		hb := "0"
		_, _ = startTestExporter(t, b, exportRoundTrip(func(r *http.Request) (*http.Response, error) {
			data, _ := io.ReadAll(r.Body)
			if len(data) > exportBodyLimit {
				t.Error("oversized wire batch")
			}
			var body map[string]json.RawMessage
			_ = json.Unmarshal(data, &body)
			mu.Lock()
			defer mu.Unlock()
			cursor := "0"
			if strings.HasSuffix(r.URL.Path, "heartbeat") {
				_ = json.Unmarshal(body["heartbeatSequence"], &hb)
			}
			if strings.HasSuffix(r.URL.Path, "samples") {
				var batch exportBatch
				_ = json.Unmarshal(data, &batch)
				pages++
				if pages == 1 {
					gap = batch.Gap
				}
				if len(batch.Samples) > 64 {
					t.Error("too many records")
				}
				if len(batch.Samples) > 0 {
					cursor = jsonSequence(batch.Samples[len(batch.Samples)-1].Sequence)
				}
			}
			return exportResponse(200, exportACKFor(body, cursor, hb)), nil
		}))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if gap == nil || gap.From != 1 || gap.Through != 1 || gap.Reason != "buffer_evicted" {
			t.Fatalf("missing explicit retention gap: %+v", gap)
		}
		if pages < 2 {
			t.Fatal("did not continue bounded backlog")
		}
	})
}
func jsonSequence(value uint64) string { data, _ := json.Marshal(value); return string(data) }
func TestExporterCloneRunConflictVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBuffer()
		var mu sync.Mutex
		attempts := 0
		var expected *string
		exporter, _ := startTestExporter(t, b, exportRoundTrip(func(r *http.Request) (*http.Response, error) {
			var body runRegistration
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			defer mu.Unlock()
			attempts++
			expected = body.ExpectedSession
			return exportResponse(409, map[string]any{"code": "run_conflict", "currentSession": exportTestSession}), nil
		}))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if attempts < 2 || expected == nil || *expected != exportTestSession || exporter.Status().RegistrationError != "run_conflict" {
			t.Fatal("clone conflict not retained for scoped CAS retry")
		}
	})
}
func TestExporterCancelJoinsLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBuffer()
		entered := make(chan struct{})
		_, stop := startTestExporter(t, b, exportRoundTrip(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}))
		<-entered
		stop()
		synctest.Wait()
	})
}
func TestExportConfigRejectsUnsafeDestinations(t *testing.T) {
	for _, endpoint := range []string{"http://receiver.example/api/v1/node-ingest", "https://127.0.0.1/api/v1/node-ingest", "https://[::1]/api/v1/node-ingest", "https://169.254.169.254/api/v1/node-ingest", "https://user:password@receiver.example/api/v1/node-ingest", "https://receiver.example/api/v1/node-ingest?token=x", "https://receiver.example/api/v1/node-ingest#fragment"} {
		t.Run(endpoint, func(t *testing.T) {
			cfg := exportTestConfig()
			cfg.Endpoint = endpoint
			if cfg.Validate() == nil {
				t.Fatal("unsafe destination accepted")
			}
		})
	}
}
