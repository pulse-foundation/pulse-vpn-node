package telemetry

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testBuffer(t *testing.T) *Buffer {
	t.Helper()
	b, err := NewBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func publishSample(t *testing.T, b *Buffer, sample Sample) {
	t.Helper()
	if err := b.Publish(sample); err != nil {
		t.Fatal(err)
	}
}

func bufferPage(t *testing.T, b *Buffer, run string, after uint64, limit int) Page {
	t.Helper()
	p, err := b.Page(run, after, limit)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Catches identity assignment outside the publication lock and destructive reads.
func TestBufferOrderedNonDestructivePages(t *testing.T) {
	b := testBuffer(t)
	for _, kind := range []string{"system", "connectivity", "runtime"} {
		publishSample(t, b, Sample{RunID: "caller-controlled", Sequence: 99, Kind: kind})
	}
	p := bufferPage(t, b, "", 0, 2)
	if len(p.RunID) != 32 || p.FirstSequence != 1 || p.LastSequence != 3 || p.HeadSequence != 3 || !p.HasMore || p.Gap || p.RunChanged {
		t.Fatalf("invalid page metadata: %+v", p)
	}
	if len(p.Records) != 2 || p.Records[0].Sequence != 1 || p.Records[1].Sequence != 2 || p.NextCursor.After != 2 || p.NextCursor.RunID != p.RunID {
		t.Fatalf("invalid ordered records: %+v", p)
	}
	if retry := bufferPage(t, b, "", 0, 2); !reflect.DeepEqual(p, retry) {
		t.Fatal("retry changed page")
	}
	next := bufferPage(t, b, p.RunID, 2, 2)
	if len(next.Records) != 1 || next.Records[0].Sequence != 3 || next.HasMore || next.NextCursor.After != 3 {
		t.Fatalf("invalid continuation: %+v", next)
	}
	for _, sample := range append(p.Records, next.Records...) {
		latest := b.Latest(sample.Kind, time.Minute)
		if latest.Sample == nil || latest.Sample.Sequence != sample.Sequence || latest.RunID != sample.RunID || sample.RunID != p.RunID {
			t.Fatalf("latest and page identities disagree: %+v", latest)
		}
	}
	raw, err := json.Marshal(p)
	if err != nil || !strings.Contains(string(raw), `"headSequence":"3"`) || !strings.Contains(string(raw), `"after":"2"`) {
		t.Fatalf("sequence JSON: %s, %v", raw, err)
	}
}

// Catches aliases of collector-owned or response-owned slices and numeric pointers.
func TestBufferOwnsPublishedAndReturnedSamples(t *testing.T) {
	b := testBuffer(t)
	value, counter, rtt, loss := 1.25, uint64(100), 2.5, 0.0
	original := Sample{Kind: "system", Metrics: []Metric{{Name: "cpu", Value: &value, Counter: &counter}}, Checks: []Check{{Name: "probe", RTTMilliseconds: &rtt, LossPercent: &loss}}}
	publishSample(t, b, original)
	value, counter, rtt, loss = 99, 99, 99, 99
	original.Metrics[0].Name = "mutated"
	original.Checks[0].Name = "mutated"
	first := bufferPage(t, b, "", 0, 1)
	if *first.Records[0].Metrics[0].Value != 1.25 || *first.Records[0].Metrics[0].Counter != 100 || first.Records[0].Metrics[0].Name != "cpu" || *first.Records[0].Checks[0].RTTMilliseconds != 2.5 || *first.Records[0].Checks[0].LossPercent != 0 || first.Records[0].Checks[0].Name != "probe" {
		t.Fatalf("publication alias: %+v", first)
	}
	*first.Records[0].Metrics[0].Value = 42
	first.Records[0].Checks[0].Name = "changed"
	latest := b.Latest("system", time.Minute)
	if *latest.Sample.Metrics[0].Value != 1.25 || latest.Sample.Checks[0].Name != "probe" {
		t.Fatal("page mutated latest")
	}
	*latest.Sample.Metrics[0].Counter = 42
	*latest.Sample.Checks[0].RTTMilliseconds = 42
	again := bufferPage(t, b, "", 0, 1)
	if *again.Records[0].Metrics[0].Counter != 100 || *again.Records[0].Checks[0].RTTMilliseconds != 2.5 {
		t.Fatal("latest mutated buffer")
	}
}

// Catches using collector wall timestamps for retention or latest freshness.
func TestBufferElapsedRetentionAndLatestAge(t *testing.T) {
	b := testBuffer(t)
	now := time.Now()
	b.now = func() time.Time { return now }
	absent := b.Latest("system", time.Minute)
	if absent.Sample != nil || absent.AgeSeconds != nil || !absent.Stale || absent.RunID == "" {
		t.Fatalf("missing latest: %+v", absent)
	}
	publishSample(t, b, Sample{Kind: "system", ObservedAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)})
	now = now.Add(2 * time.Minute)
	publishSample(t, b, Sample{Kind: "connectivity", ObservedAt: time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)})
	if p := bufferPage(t, b, "", 0, 128); len(p.Records) != 2 {
		t.Fatal("sample wall clock changed retention")
	}
	latest := b.Latest("system", time.Minute)
	if latest.AgeSeconds == nil || *latest.AgeSeconds != 120 || !latest.Stale {
		t.Fatalf("wrong age: %+v", latest)
	}
	now = now.Add(13 * time.Minute)
	p := bufferPage(t, b, "", 0, 128)
	if len(p.Records) != 1 || p.Records[0].Sequence != 2 || !p.Gap || p.FirstSequence != 2 {
		t.Fatalf("retention at boundary: %+v", p)
	}
	now = now.Add(2 * time.Minute)
	p = bufferPage(t, b, p.RunID, 1, 128)
	if len(p.Records) != 0 || p.Records == nil || !p.Gap || p.FirstSequence != 0 || p.LastSequence != 0 || p.HeadSequence != 2 || p.NextCursor.After != 2 || p.HasMore {
		t.Fatalf("all expired: %+v", p)
	}
	latest = b.Latest("system", time.Minute)
	if latest.Sample == nil || *latest.AgeSeconds != 1020 || !latest.Stale {
		t.Fatalf("latest should outlive retention: %+v", latest)
	}
	if caughtUp := bufferPage(t, b, p.RunID, 2, 128); caughtUp.Gap {
		t.Fatal("caught-up cursor has false gap")
	}
}

// Catches missing capacity eviction and failure to communicate missed measurements.
func TestBufferCountLimitAndRunChanges(t *testing.T) {
	b := testBuffer(t)
	for i := 0; i < 2050; i++ {
		publishSample(t, b, Sample{Kind: "system"})
	}
	p := bufferPage(t, b, "", 0, 128)
	if p.FirstSequence != 3 || p.LastSequence != 2050 || !p.Gap || len(p.Records) != 128 || p.Records[0].Sequence != 3 {
		t.Fatalf("count bound: %+v", p)
	}
	exact := bufferPage(t, b, p.RunID, 2, 1)
	if exact.Gap {
		t.Fatal("cursor immediately before oldest record is not a gap")
	}
	other := testBuffer(t)
	otherRun := bufferPage(t, other, "", 0, 1).RunID
	if otherRun == p.RunID {
		t.Fatal("run IDs reused")
	}
	changed := bufferPage(t, b, otherRun, math.MaxUint64, 1)
	if !changed.RunChanged || !changed.Gap || changed.Records[0].Sequence != 3 {
		t.Fatalf("run reset did not start earliest: %+v", changed)
	}
	fresh := bufferPage(t, other, p.RunID, 2050, 1)
	if !fresh.RunChanged || fresh.Gap || len(fresh.Records) != 0 || fresh.NextCursor.After != 0 {
		t.Fatalf("empty new run: %+v", fresh)
	}
}

// Catches page-size and byte bounds being enforced only on record count.
func TestBufferByteAndPageLimits(t *testing.T) {
	b := testBuffer(t)
	sample := Sample{Kind: "system", Metrics: []Metric{{Name: strings.Repeat("a", 60000)}}}
	for i := 0; i < 100; i++ {
		publishSample(t, b, sample)
	}
	p := bufferPage(t, b, "", 0, 128)
	if !p.Gap || !p.HasMore || p.FirstSequence < 30 || len(p.Records) > 8 {
		t.Fatalf("byte limits absent: %+v", p.NextCursor)
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 512<<10 {
		t.Fatalf("page bytes %d: %v", len(raw), err)
	}
	var total int
	after := p.FirstSequence - 1
	for {
		next := bufferPage(t, b, p.RunID, after, 128)
		if len(next.Records) == 0 {
			t.Fatal("byte paging made no progress")
		}
		for _, s := range next.Records {
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			total += len(raw)
		}
		after = next.NextCursor.After
		if !next.HasMore {
			break
		}
	}
	if total > 4<<20 {
		t.Fatalf("retained %d bytes", total)
	}
}

// Catches invalid samples consuming IDs or replacing the valid latest snapshot.
func TestBufferRejectsInvalidSamplesAtomically(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	cases := []Sample{
		{Kind: "unknown"},
		{Kind: "system", Metrics: make([]Metric, 257)},
		{Kind: "connectivity", Checks: make([]Check, 33)},
		{Kind: "system", Metrics: []Metric{{Value: &nan}}},
		{Kind: "system", Metrics: []Metric{{WindowSeconds: inf}}},
		{Kind: "connectivity", Checks: []Check{{RTTMilliseconds: &nan}}},
		{Kind: "connectivity", Checks: []Check{{LossPercent: &inf}}},
		{Kind: "system", Metrics: []Metric{{Name: strings.Repeat("x", 65536)}}},
	}
	b := testBuffer(t)
	publishSample(t, b, Sample{Kind: "system"})
	for i, s := range cases {
		if err := b.Publish(s); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	publishSample(t, b, Sample{Kind: "system", Metrics: make([]Metric, 256), Checks: make([]Check, 32)})
	p := bufferPage(t, b, "", 0, 128)
	if p.HeadSequence != 2 || len(p.Records) != 2 || b.Latest("system", time.Minute).Sample.Sequence != 2 {
		t.Fatalf("rejected samples changed state: %+v", p.NextCursor)
	}
}

func TestBufferRejectsInvalidCursors(t *testing.T) {
	b := testBuffer(t)
	run := bufferPage(t, b, "", 0, 1).RunID
	for _, c := range []struct {
		run   string
		after uint64
		limit int
	}{
		{"", 1, 1}, {"bad", 0, 1}, {run, 1, 1}, {run, 0, 0}, {run, 0, 129},
	} {
		if _, err := b.Page(c.run, c.after, c.limit); !errors.Is(err, ErrCursor) {
			t.Errorf("invalid cursor accepted: %+v, %v", c, err)
		}
	}
	publishSample(t, b, Sample{Kind: "system"})
	if _, err := b.Page(run, 2, 1); !errors.Is(err, ErrCursor) {
		t.Fatalf("future cursor accepted: %v", err)
	}
}

// Run with -race: readers must see coherent sequences during concurrent publication.
func TestBufferConcurrentPublishAndRead(t *testing.T) {
	b := testBuffer(t)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := b.Publish(Sample{Kind: "system"}); err != nil {
					t.Error(err)
					return
				}
				p, err := b.Page("", 0, 128)
				if err != nil {
					t.Error(err)
					return
				}
				for j, s := range p.Records {
					if s.Sequence != uint64(j+1) || s.RunID != p.RunID {
						t.Error("incoherent page")
						return
					}
				}
				latest := b.Latest("system", time.Minute)
				if latest.Sample == nil || latest.Sample.RunID != p.RunID {
					t.Error("incoherent latest")
					return
				}
			}
		}()
	}
	wg.Wait()
	p := bufferPage(t, b, "", 0, 128)
	if p.HeadSequence != 800 || p.LastSequence != 800 {
		t.Fatalf("lost publications: %+v", p.NextCursor)
	}
}

func TestBufferOwnsRuntimeMetadata(t *testing.T) {
	b := testBuffer(t)
	info := &RuntimeInfo{CoreState: "running", CoreVersion: "1.19.33", UserState: "applied"}
	publishSample(t, b, Sample{Kind: "runtime", Runtime: info})
	info.CoreState = "failed"
	latest := b.Latest("runtime", time.Minute)
	if latest.Sample.Runtime.CoreState != "running" {
		t.Fatal("caller modified runtime metadata")
	}
	latest.Sample.Runtime.CoreVersion = "changed"
	if p := bufferPage(t, b, "", 0, 1); p.Records[0].Runtime.CoreVersion != "1.19.33" {
		t.Fatal("reader modified runtime metadata")
	}
	if err := b.Publish(Sample{Kind: "runtime", Runtime: &RuntimeInfo{CoreVersion: strings.Repeat("v", 129)}}); err == nil {
		t.Fatal("oversized runtime metadata accepted")
	}
}
