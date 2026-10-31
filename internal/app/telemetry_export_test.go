package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

type deliveryRoundTrip func(*http.Request) (*http.Response, error)

func (f deliveryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTelemetryRotationReloadsWithoutRestartAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "enrollment.json")
		buffer, _ := telemetry.NewBuffer()
		var mu sync.Mutex
		heartbeat := uint64(0)
		seen := map[string]int{}
		transport := deliveryRoundTrip(func(r *http.Request) (*http.Response, error) {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			var run string
			_ = json.Unmarshal(body["runId"], &run)
			mu.Lock()
			defer mu.Unlock()
			if strings.HasSuffix(r.URL.Path, "heartbeat") {
				var seq string
				_ = json.Unmarshal(body["heartbeatSequence"], &seq)
				heartbeat, _ = strconv.ParseUint(seq, 10, 64)
				seen[r.Header.Get("Authorization")]++
			}
			data, _ := json.Marshal(map[string]any{"schemaVersion": 1, "sessionId": "8a80bd53-dc79-46a8-ad11-a8fa066148e2", "runId": run, "committedSequence": "0", "heartbeatSequence": strconv.FormatUint(heartbeat, 10), "leaseExpiresAt": time.Now().Add(90 * time.Second).UTC()})
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		})
		install := func(token string) {
			t.Helper()
			cfg := telemetry.ExportConfig{SchemaVersion: 1, Endpoint: "https://receiver.example/api/v1/node-ingest", EnrollmentGeneration: "4c9bc671-b3e3-40f9-89ab-cf8682ce7f8e", Credential: token}
			data, _ := json.Marshal(cfg)
			temp := path + ".next"
			if err := os.WriteFile(temp, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(temp, path); err != nil {
				t.Fatal(err)
			}
		}
		install("synthetic-old-credential")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		delivery, stop := startTelemetryDelivery(ctx, path, buffer, telemetry.ExporterOptions{Transport: transport})
		defer stop()
		time.Sleep(time.Second)
		synctest.Wait()
		mu.Lock()
		if seen["Bearer synthetic-old-credential"] != 1 {
			t.Error("old enrollment did not send fresh heartbeat")
		}
		mu.Unlock()
		install("synthetic-new-credential")
		time.Sleep(6 * time.Second)
		synctest.Wait()
		mu.Lock()
		if seen["Bearer synthetic-new-credential"] != 1 || heartbeat != 2 {
			t.Error("rotation did not continue run sequence using new credential")
		}
		old := seen["Bearer synthetic-old-credential"]
		mu.Unlock()
		time.Sleep(40 * time.Second)
		synctest.Wait()
		mu.Lock()
		if seen["Bearer synthetic-old-credential"] != old {
			t.Error("retired sender kept running")
		}
		mu.Unlock()
		if status := delivery.Status(); !status.Configured || status.LastHeartbeatAt == nil {
			t.Fatal("missing safe cached delivery status")
		}
		stop()
		synctest.Wait()
	})
}

func TestTelemetryInvalidFileRemovalClearsDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "enrollment.json")
		if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
		buffer, _ := telemetry.NewBuffer()
		delivery, stop := startTelemetryDelivery(context.Background(), path, buffer, telemetry.ExporterOptions{})
		defer stop()
		time.Sleep(time.Second)
		synctest.Wait()
		if delivery.Status().ConfigurationError == "" {
			t.Fatal("missing invalid-file diagnostic")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if delivery.Status().ConfigurationError != "" {
			t.Fatal("removed enrollment remains invalid")
		}
	})
}
