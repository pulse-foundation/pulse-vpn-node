package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
	"io"
	"net/http"
	"strconv"
	"time"
)

const exportBodyLimit = 512 * 1024
const exportSampleLimit = 64

type exportEnvelope struct {
	SchemaVersion        int       `json:"schemaVersion"`
	EnrollmentGeneration string    `json:"enrollmentGeneration"`
	RunID                string    `json:"runId"`
	SentAt               time.Time `json:"sentAt"`
}
type runRegistration struct {
	exportEnvelope
	ExpectedSession *string `json:"expectedSession"`
}
type heartbeatState struct {
	Runtime RuntimeInfo `json:"runtime"`
	Metrics []Metric    `json:"metrics,omitempty"`
	Checks  []Check     `json:"checks,omitempty"`
}
type exportHeartbeat struct {
	exportEnvelope
	SessionID         string         `json:"sessionId"`
	HeartbeatSequence uint64         `json:"heartbeatSequence,string"`
	State             heartbeatState `json:"state"`
	TrafficSnapshot   *traffic.View  `json:"trafficSnapshot,omitempty"`
}
type exportGap struct {
	From    uint64 `json:"fromSequence,string"`
	Through uint64 `json:"throughSequence,string"`
	Reason  string `json:"reason"`
}
type exportBatch struct {
	exportEnvelope
	SessionID string     `json:"sessionId"`
	Samples   []Sample   `json:"samples"`
	Gap       *exportGap `json:"gap,omitempty"`
}
type exportACK struct {
	SchemaVersion     int       `json:"schemaVersion"`
	SessionID         string    `json:"sessionId"`
	RunID             string    `json:"runId"`
	CommittedSequence string    `json:"committedSequence"`
	HeartbeatSequence string    `json:"heartbeatSequence"`
	LeaseExpiresAt    time.Time `json:"leaseExpiresAt"`
}
type deliveryError struct {
	code    string
	retry   time.Duration
	session string
}

func (e *deliveryError) Error() string { return e.code }
func decimalSequence(value string) (uint64, bool) {
	if value == "" || len(value) > 20 || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return n, err == nil
}
func retryDelay(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(max(1, min(60, seconds))) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Second, min(time.Minute, at.Sub(now)))
	}
	return 0
}
func (e *Exporter) post(ctx context.Context, path string, body any) (exportACK, *deliveryError) {
	data, err := json.Marshal(body)
	if err != nil || len(data) > exportBodyLimit {
		return exportACK{}, &deliveryError{code: "invalid_payload", retry: time.Minute}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.config.Endpoint+path, bytes.NewReader(data))
	if err != nil {
		return exportACK{}, &deliveryError{code: "invalid_enrollment", retry: time.Minute}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+e.config.Credential)
	response, err := e.client.Do(request)
	if err != nil {
		return exportACK{}, &deliveryError{code: "unavailable"}
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(data) > 16384 {
		return exportACK{}, &deliveryError{code: "invalid_response"}
	}
	if response.StatusCode != http.StatusOK {
		failure := &deliveryError{code: "unavailable", retry: retryDelay(response.Header.Get("Retry-After"), e.now())}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			failure.code = "redirect_rejected"
			failure.retry = time.Minute
			return exportACK{}, failure
		}
		var remote struct {
			Code           string  `json:"code"`
			CurrentSession *string `json:"currentSession"`
		}
		_ = json.Unmarshal(data, &remote)
		switch remote.Code {
		case "unauthorized", "invalid_request", "run_conflict", "retired_run", "sequence_conflict", "clock_skew", "backpressure", "rate_limited", "unavailable", "payload_too_large", "unsupported_encoding", "unsupported_media_type":
			failure.code = remote.Code
		}
		if remote.CurrentSession != nil && enrollmentUUID.MatchString(*remote.CurrentSession) {
			failure.session = *remote.CurrentSession
		}
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 409 && response.StatusCode != 429 {
			failure.retry = time.Minute
		}
		return exportACK{}, failure
	}
	var ack exportACK
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ack) != nil || decoder.Decode(new(any)) != io.EOF || ack.SchemaVersion != 1 || ack.RunID != e.runID || !enrollmentUUID.MatchString(ack.SessionID) || ack.LeaseExpiresAt.IsZero() {
		return exportACK{}, &deliveryError{code: "invalid_response"}
	}
	return ack, nil
}

func normalizeExportObservations(metrics []Metric, checks []Check) {
	for i := range metrics {
		metrics[i].ObservedAt = metrics[i].ObservedAt.UTC()
	}
	for i := range checks {
		checks[i].ObservedAt = checks[i].ObservedAt.UTC()
	}
}
