package main

import (
	"crypto/tls"

	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type deliveryFixture struct {
	mu                                           sync.Mutex
	mode                                         string
	run                                          string
	session                                      string
	committed, heartbeat                         uint64
	HistoryRequests, HeartbeatRequests, Rejected int
}
type deliveryRequest struct {
	SchemaVersion                          int
	EnrollmentGeneration, RunID, SessionID string
	HeartbeatSequence                      uint64 `json:"heartbeatSequence,string"`
	Samples                                []telemetrySample
	Gap                                    *struct {
		ThroughSequence uint64 `json:"throughSequence,string"`
	}
}

func deliveryEnrollment() (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "telemetry-enrollment.json"))
	if err != nil {
		return nil, err
	}
	var wire struct{ Credential, EnrollmentGeneration string }
	if err = json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	return map[string]string{"credential": wire.Credential, "generation": wire.EnrollmentGeneration}, nil
}
func serveTelemetryReceiver() error {
	enrollment, err := deliveryEnrollment()
	if err != nil {
		return err
	}
	fixture := &deliveryFixture{session: newUUID()}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+enrollment["credential"] {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/fixture" {
			if mode := r.URL.Query().Get("mode"); mode != "" {
				fixture.mode = mode
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"history": fixture.HistoryRequests, "heartbeat": fixture.HeartbeatRequests, "rejected": fixture.Rejected, "committed": fixture.committed})
			return
		}
		var body deliveryRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&body) != nil || body.SchemaVersion != 1 || body.EnrollmentGeneration != enrollment["generation"] {
			w.WriteHeader(400)
			return
		}
		if fixture.mode == "outage" {
			fixture.Rejected++
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":"unavailable"}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/node-ingest/runs":
			if fixture.run != body.RunID {
				fixture.run = body.RunID
				fixture.session = newUUID()
				fixture.committed = 0
				fixture.heartbeat = 0
			}
		case "/api/v1/node-ingest/heartbeat":
			if body.SessionID != fixture.session {
				w.WriteHeader(409)
				return
			}
			fixture.heartbeat = max(fixture.heartbeat, body.HeartbeatSequence)
			fixture.HeartbeatRequests++
		case "/api/v1/node-ingest/samples":
			fixture.HistoryRequests++
			if fixture.mode == "history" {
				fixture.Rejected++
				w.Header().Set("Retry-After", "10")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"code":"backpressure"}`))
				return
			}
			if body.SessionID != fixture.session || len(body.Samples) > 64 {
				w.WriteHeader(400)
				return
			}
			if body.Gap != nil {
				fixture.committed = max(fixture.committed, body.Gap.ThroughSequence)
			}
			for _, sample := range body.Samples {
				if telemetryValidSample(sample) != nil || sample.RunID != fixture.run {
					w.WriteHeader(400)
					return
				}
				fixture.committed = max(fixture.committed, sample.Sequence)
			}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "sessionId": fixture.session, "runId": fixture.run, "committedSequence": strconv.FormatUint(fixture.committed, 10), "heartbeatSequence": strconv.FormatUint(fixture.heartbeat, 10), "leaseExpiresAt": time.Now().Add(90 * time.Second).UTC()})
	})
	server := &http.Server{Addr: ":443", Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return server.ListenAndServeTLS(filepath.Join(fixtureDir, "target.crt"), filepath.Join(fixtureDir, "target.key"))
}
func deliveryState(mode string) (map[string]uint64, error) {
	enrollment, err := deliveryEnrollment()
	if err != nil {
		return nil, err
	}
	client, err := proxyClient("client:7890")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest("GET", "https://target/fixture?mode="+mode, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+enrollment["credential"])
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("delivery receiver unavailable")
	}
	defer response.Body.Close()
	var state map[string]uint64
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&state) != nil {
		return nil, errors.New("delivery state invalid")
	}
	return state, nil
}
func telemetryDeliveryCheck() error {
	await := func(predicate func(map[string]uint64) bool) error {
		deadline := time.Now().Add(75 * time.Second)
		for time.Now().Before(deadline) {
			state, err := deliveryState("")
			if err != nil {
				return err
			}
			if predicate(state) {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return errors.New("authenticated push did not reach expected state")
	}
	if err := await(func(s map[string]uint64) bool { return s["heartbeat"] > 0 && s["committed"] > 0 }); err != nil {
		return err
	}
	fmt.Println("PASS: authenticated initial heartbeat and history received over verified TLS")
	sessions, err := openSessions("client:7890")
	if err != nil {
		return err
	}
	defer sessions.close()
	before, err := deliveryState("history")
	if err != nil {
		return err
	}
	if err = await(func(s map[string]uint64) bool {
		return s["heartbeat"] > before["heartbeat"] && s["rejected"] > before["rejected"]
	}); err != nil {
		return err
	}
	if err = sessions.echo(); err != nil {
		return err
	}
	fmt.Println("PASS: history backpressure preserves fresh heartbeat and VPN sessions")
	before, err = deliveryState("outage")
	if err != nil {
		return err
	}
	if err = await(func(s map[string]uint64) bool { return s["rejected"] > before["rejected"] }); err != nil {
		return err
	}
	if err = sessions.echo(); err != nil {
		return err
	}
	if err = probe(); err != nil {
		return err
	}
	before, err = deliveryState("healthy")
	if err != nil {
		return err
	}
	if err = await(func(s map[string]uint64) bool {
		return s["committed"] > before["committed"] && s["heartbeat"] > before["heartbeat"]
	}); err != nil {
		return err
	}
	if err = sessions.echo(); err != nil {
		return err
	}
	fmt.Println("PASS: authenticated TLS push, independent fresh heartbeat during history backpressure, receiver outage/catch-up while existing and new VPN connections continue")
	return nil
}
