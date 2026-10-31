package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"
)

type ExportStatus struct {
	ConfigurationError string     `json:"configurationError,omitempty"`
	Configured         bool       `json:"configured"`
	RegistrationError  string     `json:"registrationError,omitempty"`
	HeartbeatError     string     `json:"heartbeatError,omitempty"`
	HistoryError       string     `json:"historyError,omitempty"`
	LastHeartbeatAt    *time.Time `json:"lastHeartbeatAt,omitempty"`
	CommittedSequence  string     `json:"committedSequence"`
	HeartbeatSequence  string     `json:"heartbeatSequence"`
}
type ExporterOptions struct {
	Transport    http.RoundTripper
	Now          func() time.Time
	RandomUint64 func() uint64 // Optional deterministic jitter source for tests.
	SampleMaxAge time.Duration
	ProbeMaxAge  time.Duration
}

// Exporter has one Run owner. Collection never waits for its network work.
// Status is safe for concurrent cached reads and never contains credentials or destinations.
type Exporter struct {
	config                               ExportConfig
	buffer                               *Buffer
	client                               *http.Client
	now                                  func() time.Time
	random                               func() uint64
	sampleMaxAge, probeMaxAge            time.Duration
	runID, session, expectedSession      string
	cursor, heartbeat, acceptedHeartbeat uint64
	mu                                   sync.Mutex
	status                               ExportStatus
}

func NewExporter(cfg ExportConfig, buffer *Buffer, options ExporterOptions) (*Exporter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if buffer == nil {
		return nil, errors.New("telemetry buffer required")
	}
	page, err := buffer.Page("", 0, 1)
	if err != nil {
		return nil, err
	}
	transport := options.Transport
	if transport == nil {
		transport = telemetryTransport()
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	sampleAge := options.SampleMaxAge
	if sampleAge <= 0 {
		sampleAge = 15 * time.Second
	}
	probeAge := options.ProbeMaxAge
	if probeAge <= 0 {
		probeAge = 90 * time.Second
	}
	random := options.RandomUint64
	if random == nil {
		random = deliveryRandom
	}
	return &Exporter{random: random, config: cfg, buffer: buffer, runID: page.RunID, now: now, sampleMaxAge: sampleAge, probeMaxAge: probeAge, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, status: ExportStatus{Configured: true, CommittedSequence: "0", HeartbeatSequence: "0"}}, nil
}
func (e *Exporter) Status() ExportStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.status
	if s.LastHeartbeatAt != nil {
		at := *s.LastHeartbeatAt
		s.LastHeartbeatAt = &at
	}
	return s
}
func (e *Exporter) record(channel string, failure *deliveryError) {
	e.mu.Lock()
	defer e.mu.Unlock()
	code := ""
	if failure != nil {
		code = failure.code
	}
	switch channel {
	case "registration":
		e.status.RegistrationError = code
	case "heartbeat":
		e.status.HeartbeatError = code
		if failure == nil {
			at := e.now().UTC()
			e.status.LastHeartbeatAt = &at
		}
	case "history":
		e.status.HistoryError = code
	}
}
func (e *Exporter) envelope() exportEnvelope {
	return exportEnvelope{1, e.config.EnrollmentGeneration, e.runID, e.now().UTC()}
}
func (e *Exporter) acceptACK(ack exportACK, minimumCursor, minimumHeartbeat uint64) *deliveryError {
	cursor, ok := decimalSequence(ack.CommittedSequence)
	heartbeat, valid := decimalSequence(ack.HeartbeatSequence)
	page, err := e.buffer.Page(e.runID, 0, 1)
	if !ok || !valid || err != nil || cursor < minimumCursor || cursor > page.HeadSequence || heartbeat < minimumHeartbeat || heartbeat < e.acceptedHeartbeat || (e.session != "" && ack.SessionID != e.session) {
		return &deliveryError{code: "invalid_response"}
	}
	e.session = ack.SessionID
	e.expectedSession = ack.SessionID
	e.cursor = cursor
	e.heartbeat = max(e.heartbeat, heartbeat)
	e.acceptedHeartbeat = heartbeat
	e.mu.Lock()
	e.status.CommittedSequence = ack.CommittedSequence
	e.status.HeartbeatSequence = ack.HeartbeatSequence
	e.mu.Unlock()
	return nil
}
func (e *Exporter) register(ctx context.Context) *deliveryError {
	var expected *string
	if e.expectedSession != "" {
		value := e.expectedSession
		expected = &value
	}
	ack, failure := e.post(ctx, "/runs", runRegistration{e.envelope(), expected})
	if failure != nil {
		if failure.code == "run_conflict" && failure.session != "" {
			e.expectedSession = failure.session
		}
		return failure
	}
	return e.acceptACK(ack, e.cursor, 0)
}
func (e *Exporter) heartbeatState() heartbeatState {
	state := heartbeatState{Runtime: RuntimeInfo{CoreState: "unavailable", UserState: "not_configured"}}
	runtime := e.buffer.Latest("runtime", e.sampleMaxAge)
	if runtime.Sample != nil {
		state.Metrics = append(state.Metrics, runtime.Sample.Metrics...)
		if !runtime.Stale && runtime.Sample.Runtime != nil {
			state.Runtime = *runtime.Sample.Runtime
		}
	}
	system := e.buffer.Latest("system", e.sampleMaxAge)
	if system.Sample != nil {
		state.Metrics = append(state.Metrics, system.Sample.Metrics...)
	}
	if len(state.Metrics) > 200 {
		state.Metrics = state.Metrics[:200]
	}
	checks := e.buffer.Latest("connectivity", e.probeMaxAge)
	if checks.Sample != nil {
		state.Checks = checks.Sample.Checks
	}
	normalizeExportObservations(state.Metrics, state.Checks)
	return state
}
func (e *Exporter) sendHeartbeat(ctx context.Context) *deliveryError {
	if e.heartbeat == math.MaxUint64 {
		return &deliveryError{code: "sequence_exhausted", retry: time.Minute}
	}
	e.heartbeat++
	ack, failure := e.post(ctx, "/heartbeat", exportHeartbeat{e.envelope(), e.session, e.heartbeat, e.heartbeatState()})
	if failure != nil {
		return failure
	}
	return e.acceptACK(ack, e.cursor, e.heartbeat)
}
func (e *Exporter) sendHistory(ctx context.Context) (bool, *deliveryError) {
	page, err := e.buffer.Page(e.runID, e.cursor, exportSampleLimit)
	if err != nil || page.RunChanged {
		return false, &deliveryError{code: "invalid_cursor", retry: time.Minute}
	}
	if len(page.Records) == 0 && !page.Gap {
		return false, nil
	}
	// Page returns owned copies; canonical wire timestamps must not mutate the buffer.
	for i := range page.Records {
		page.Records[i].ObservedAt = page.Records[i].ObservedAt.UTC()
		normalizeExportObservations(page.Records[i].Metrics, page.Records[i].Checks)
	}
	body := exportBatch{exportEnvelope: e.envelope(), SessionID: e.session, Samples: page.Records}
	minimum := e.cursor
	if page.Gap {
		through := page.HeadSequence
		if page.FirstSequence > 0 {
			through = page.FirstSequence - 1
		}
		body.Gap = &exportGap{e.cursor + 1, through, "buffer_evicted"}
		minimum = through
	}
	if len(body.Samples) > 0 {
		minimum = body.Samples[len(body.Samples)-1].Sequence
	}
	ack, failure := e.post(ctx, "/samples", body)
	if failure != nil {
		return false, failure
	}
	return page.HasMore, e.acceptACK(ack, minimum, 0)
}
func deliveryRandom() uint64 {
	var bytes [8]byte
	_, _ = rand.Read(bytes[:])
	return binary.LittleEndian.Uint64(bytes[:])
}
func (e *Exporter) deliveryInterval() time.Duration {
	return 27*time.Second + time.Duration(e.random()%uint64(6*time.Second+1))
}
func exportWait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(max(0, delay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func nextFailureDelay(failure *deliveryError, backoff *time.Duration, random func() uint64) time.Duration {
	ceiling := max(time.Second, min(time.Minute, *backoff))
	floor := max(time.Second, ceiling/2)
	delay := floor + time.Duration(random()%uint64(ceiling-floor+1))
	*backoff = min(time.Minute, *backoff*2)
	if failure.retry > 0 {
		delay = max(delay, failure.retry)
	}
	return max(time.Second, min(time.Minute, delay))
}
func (e *Exporter) Run(ctx context.Context) {
	defer e.client.CloseIdleConnections()
	nextHeartbeat, nextHistory := e.now(), e.now()
	registrationBackoff, heartbeatBackoff, historyBackoff := time.Second, time.Second, time.Second
	for ctx.Err() == nil {
		if e.session == "" {
			failure := e.register(ctx)
			e.record("registration", failure)
			if failure != nil {
				if !exportWait(ctx, nextFailureDelay(failure, &registrationBackoff, e.random)) {
					return
				}
				continue
			}
			registrationBackoff = time.Second
			nextHeartbeat = e.now()
		}
		now := e.now()
		channel := ""
		var failure *deliveryError
		if !now.Before(nextHeartbeat) {
			channel = "heartbeat"
			failure = e.sendHeartbeat(ctx)
			if failure == nil {
				heartbeatBackoff = time.Second
				nextHeartbeat = e.now().Add(e.deliveryInterval())
			} else {
				nextHeartbeat = e.now().Add(nextFailureDelay(failure, &heartbeatBackoff, e.random))
			}
		}
		if channel == "" && !now.Before(nextHistory) {
			channel = "history"
			var more bool
			more, failure = e.sendHistory(ctx)
			if failure == nil {
				historyBackoff = time.Second
				delay := e.deliveryInterval()
				if more {
					delay = time.Second
				}
				nextHistory = e.now().Add(delay)
			} else {
				nextHistory = e.now().Add(nextFailureDelay(failure, &historyBackoff, e.random))
			}
		}
		if channel != "" {
			e.record(channel, failure)
			if failure != nil && (failure.code == "run_conflict" || failure.code == "retired_run" || failure.code == "sequence_conflict") {
				e.session = ""
			}
			continue
		}
		if !exportWait(ctx, min(nextHeartbeat.Sub(now), nextHistory.Sub(now))) {
			return
		}
	}
}
