package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// ErrCursor identifies invalid cursor syntax, future positions, or page limits.
var ErrCursor = errors.New("invalid telemetry cursor")

// Cursor is a non-destructive read position within one agent run.
type Cursor struct {
	RunID string `json:"runId"`
	After uint64 `json:"after,string"`
}

// Page describes a coherent retained window. Bounds are zero when no records
// remain; HeadSequence still identifies the last publication in this run.
type Page struct {
	RunID         string   `json:"runId"`
	FirstSequence uint64   `json:"firstSequence,string"`
	LastSequence  uint64   `json:"lastSequence,string"`
	HeadSequence  uint64   `json:"headSequence,string"`
	Records       []Sample `json:"records"`
	NextCursor    Cursor   `json:"nextCursor"`
	Gap           bool     `json:"gap"`
	RunChanged    bool     `json:"runChanged"`
	HasMore       bool     `json:"hasMore"`
}

// Latest keeps the most recent observation even after catch-up retention expires.
// AgeSeconds measures elapsed time since publication, independently of wall time
// reported by collectors. A missing observation has null sample and age.
type Latest struct {
	RunID      string   `json:"runId"`
	Sample     *Sample  `json:"sample"`
	AgeSeconds *float64 `json:"ageSeconds"`
	Stale      bool     `json:"stale"`
}

type bufferedSample struct {
	sample      Sample
	publishedAt time.Time
	size        int
}

// Buffer owns immutable samples and a bounded ring. It must not be copied after
// first use. The latest record for each of the three kinds is retained separately.
type Buffer struct {
	mu      sync.Mutex
	runID   string
	head    uint64
	records [MaxRecords]bufferedSample
	start   int
	count   int
	bytes   int
	latest  map[string]bufferedSample
	now     func() time.Time
}

func NewBuffer() (*Buffer, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("create telemetry run ID: %w", err)
	}
	return &Buffer{runID: hex.EncodeToString(id[:]), latest: make(map[string]bufferedSample, 3), now: time.Now}, nil
}

// Publish atomically assigns identity, appends to the ring, and replaces the
// corresponding latest snapshot. The caller retains ownership of its sample.
func (b *Buffer) Publish(sample Sample) error {
	if err := validateSampleSize(sample); err != nil {
		return err
	}
	sample = cloneSample(sample)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.head == math.MaxUint64 {
		return errors.New("telemetry sequence exhausted")
	}
	sample.RunID = b.runID
	sample.Sequence = b.head + 1
	encoded, err := json.Marshal(sample)
	if err != nil {
		return errors.New("telemetry sample contains invalid values")
	}
	if len(encoded) > MaxRecordBytes {
		return errors.New("telemetry sample exceeds byte limit")
	}
	now := b.now()
	b.trimExpired(now)
	for b.count > 0 && (b.count == MaxRecords || b.bytes+len(encoded) > MaxBufferBytes) {
		b.evictOldest()
	}
	record := bufferedSample{sample: sample, publishedAt: now, size: len(encoded)}
	b.records[(b.start+b.count)%MaxRecords] = record
	b.count++
	b.bytes += record.size
	b.head = sample.Sequence
	b.latest[sample.Kind] = record
	return nil
}

func (b *Buffer) Latest(kind string, maxAge time.Duration) Latest {
	b.mu.Lock()
	now := b.now()
	b.trimExpired(now)
	record, ok := b.latest[kind]
	result := Latest{RunID: b.runID, Stale: true}
	b.mu.Unlock()
	if !ok {
		return result
	}
	sample := cloneSample(record.sample)
	age := max(0, now.Sub(record.publishedAt).Seconds())
	result.Sample = &sample
	result.AgeSeconds = &age
	result.Stale = now.Sub(record.publishedAt) > maxAge
	return result
}

func (b *Buffer) Page(runID string, after uint64, limit int) (Page, error) {
	if limit < 1 || limit > MaxPageSize || (runID == "" && after != 0) {
		return Page{}, ErrCursor
	}
	if runID != "" {
		if len(runID) != 32 {
			return Page{}, ErrCursor
		}
		if _, err := hex.DecodeString(runID); err != nil {
			return Page{}, ErrCursor
		}
	}
	b.mu.Lock()
	b.trimExpired(b.now())
	changed := runID != "" && runID != b.runID
	if changed {
		after = 0
	}
	if after > b.head {
		b.mu.Unlock()
		return Page{}, ErrCursor
	}
	result := Page{RunID: b.runID, HeadSequence: b.head, Records: make([]Sample, 0, limit), NextCursor: Cursor{RunID: b.runID, After: after}, RunChanged: changed}
	if b.count == 0 {
		result.Gap = after < b.head
		result.NextCursor.After = b.head
	} else {
		result.FirstSequence = b.records[b.start].sample.Sequence
		result.LastSequence = b.records[(b.start+b.count-1)%MaxRecords].sample.Sequence
		result.Gap = after < result.FirstSequence-1
		// Reserve more than the fixed JSON envelope needs, including decimal uint64
		// positions and both run IDs; each record also needs a comma separator.
		size := 1024
		for i := 0; i < b.count; i++ {
			record := b.records[(b.start+i)%MaxRecords]
			if record.sample.Sequence <= after {
				continue
			}
			if len(result.Records) == limit || size+record.size+1 > MaxPageBytes {
				result.HasMore = true
				break
			}
			result.Records = append(result.Records, record.sample)
			result.NextCursor.After = record.sample.Sequence
			size += record.size + 1
		}
	}
	b.mu.Unlock()
	// Internal values never mutate. Clone the captured immutable references after
	// unlocking, so response encoding and caller mutation cannot hold up collectors.
	for i := range result.Records {
		result.Records[i] = cloneSample(result.Records[i])
	}
	return result, nil
}

func (b *Buffer) trimExpired(now time.Time) {
	for b.count > 0 && now.Sub(b.records[b.start].publishedAt) >= Retention {
		b.evictOldest()
	}
}

func (b *Buffer) evictOldest() {
	b.bytes -= b.records[b.start].size
	b.records[b.start] = bufferedSample{}
	b.start = (b.start + 1) % MaxRecords
	b.count--
}

// Bound allocation work before cloning or JSON encoding, including oversized
// collector labels. JSON marshaling separately rejects nonfinite floats and
// accounts for escaping and the assigned run/sequence fields.
func validateSampleSize(sample Sample) error {
	switch sample.Kind {
	case "system", "connectivity", "runtime":
	default:
		return errors.New("unknown telemetry kind")
	}
	if len(sample.Metrics) > 256 || len(sample.Checks) > 32 {
		return errors.New("telemetry sample exceeds item limit")
	}
	if sample.Runtime != nil && (len(sample.Runtime.CoreState) > 128 || len(sample.Runtime.CoreVersion) > 128 || len(sample.Runtime.UserState) > 128) {
		return errors.New("telemetry runtime metadata exceeds limit")
	}
	bytes := 0
	add := func(values ...string) bool {
		for _, s := range values {
			if len(s) > MaxRecordBytes-bytes {
				return false
			}
			bytes += len(s)
		}
		return true
	}
	for _, m := range sample.Metrics {
		if !add(m.Name, m.Scope, m.Resource, m.Unit, m.Reason) {
			return errors.New("telemetry sample exceeds byte limit")
		}
	}
	for _, c := range sample.Checks {
		if !add(c.Name, c.Type, c.Scope, c.Vantage, c.Status, c.Reason) {
			return errors.New("telemetry sample exceeds byte limit")
		}
	}
	return nil
}

func cloneSample(sample Sample) Sample {
	sample.Kind = strings.Clone(sample.Kind)
	if sample.Runtime != nil {
		info := *sample.Runtime
		info.CoreState = strings.Clone(info.CoreState)
		info.CoreVersion = strings.Clone(info.CoreVersion)
		info.UserState = strings.Clone(info.UserState)
		sample.Runtime = &info
	}
	if sample.Metrics != nil {
		metrics := make([]Metric, len(sample.Metrics))
		copy(metrics, sample.Metrics)
		for i := range metrics {
			m := &metrics[i]
			m.Name = strings.Clone(m.Name)
			m.Scope = strings.Clone(m.Scope)
			m.Resource = strings.Clone(m.Resource)
			m.Unit = strings.Clone(m.Unit)
			m.Reason = strings.Clone(m.Reason)
			if m.Value != nil {
				v := *m.Value
				m.Value = &v
			}
			if m.Counter != nil {
				v := *m.Counter
				m.Counter = &v
			}
		}
		sample.Metrics = metrics
	}
	if sample.Checks != nil {
		checks := make([]Check, len(sample.Checks))
		copy(checks, sample.Checks)
		for i := range checks {
			c := &checks[i]
			c.Name = strings.Clone(c.Name)
			c.Type = strings.Clone(c.Type)
			c.Scope = strings.Clone(c.Scope)
			c.Vantage = strings.Clone(c.Vantage)
			c.Status = strings.Clone(c.Status)
			c.Reason = strings.Clone(c.Reason)
			if c.RTTMilliseconds != nil {
				v := *c.RTTMilliseconds
				c.RTTMilliseconds = &v
			}
			if c.LossPercent != nil {
				v := *c.LossPercent
				c.LossPercent = &v
			}
		}
		sample.Checks = checks
	}
	return sample
}
