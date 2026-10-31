package telemetry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only the transport maps the synthetic public receiver to a local TLS fixture.
// Production URL validation and redirect handling remain enabled.
type localReceiverTransport struct {
	transport http.RoundTripper
	origin    string
}

func (t localReceiverTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	u := *r.URL
	c.URL = &u
	c.URL.Host = strings.TrimPrefix(t.origin, "https://")
	return t.transport.RoundTrip(c)
}
func exporterFixture(t *testing.T, handler http.HandlerFunc) (*Exporter, *Buffer) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	buffer, err := NewBuffer()
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com", MinVersion: tls.VersionTLS12}
	t.Cleanup(transport.CloseIdleConnections)
	exporter, err := NewExporter(ExportConfig{SchemaVersion: 1, Endpoint: "https://receiver.example/api/v1/node-ingest", EnrollmentGeneration: "4c9bc671-b3e3-40f9-89ab-cf8682ce7f8e", Credential: "synthetic-sender-credential"}, buffer, ExporterOptions{Transport: localReceiverTransport{transport, server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	return exporter, buffer
}
func TestExporterLostACKRetriesSameSample(t *testing.T) {
	var mu sync.Mutex
	var first string
	var requests int
	storedHeartbeat := "0"
	accepted := make(chan struct{})
	session := "8a80bd53-dc79-46a8-ad11-a8fa066148e2"
	exporter, buffer := exporterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-sender-credential" {
			t.Error("missing dedicated credential")
			w.WriteHeader(401)
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var run string
		_ = json.Unmarshal(body["runId"], &run)
		cursor := "0"
		mu.Lock()
		heartbeat := storedHeartbeat
		mu.Unlock()
		if r.URL.Path == "/api/v1/node-ingest/heartbeat" {
			_ = json.Unmarshal(body["heartbeatSequence"], &heartbeat)
			mu.Lock()
			storedHeartbeat = heartbeat
			mu.Unlock()
		}
		if r.URL.Path == "/api/v1/node-ingest/samples" {
			mu.Lock()
			requests++
			samples := string(body["samples"])
			if requests == 1 {
				first = samples
				mu.Unlock()
				w.WriteHeader(503)
				return
			}
			if first != samples {
				t.Error("retry changed retained sample identities")
			}
			mu.Unlock()
			cursor = "1"
			select {
			case <-accepted:
			default:
				close(accepted)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "sessionId": session, "runId": run, "committedSequence": cursor, "heartbeatSequence": heartbeat, "leaseExpiresAt": time.Now().Add(90 * time.Second).UTC()})
	})
	if err := buffer.Publish(Sample{Kind: "system", ObservedAt: time.Now().UTC(), Metrics: []Metric{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); exporter.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("exporter failed to join")
		}
	})
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("sample retry not delivered")
	}
}
func TestExporterRedirectDoesNotLeakKey(t *testing.T) {
	var leaked bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true; w.WriteHeader(200) }))
	defer target.Close()
	attempted := make(chan struct{})
	exporter, _ := exporterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-attempted:
		default:
			close(attempted)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); exporter.Run(ctx) }()
	select {
	case <-attempted:
	case <-time.After(2 * time.Second):
		t.Fatal("registration not attempted")
	}
	cancel()
	<-done
	if leaked {
		t.Fatal("redirect received telemetry credential")
	}
}
