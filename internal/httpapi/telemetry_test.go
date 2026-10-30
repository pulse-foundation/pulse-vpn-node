package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

func TestTelemetryCachedAuthenticatedReadsAndCursors(t *testing.T) {
	b, err := telemetry.NewBuffer()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"system", "runtime", "connectivity"} {
		if err := b.Publish(telemetry.Sample{Kind: kind, ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewWithServices("test-token", true, func(context.Context) (string, error) { t.Fatal("cached telemetry contacted core"); return "", nil }, Services{Telemetry: b, SampleInterval: time.Second, ProbeInterval: time.Second})
	get := func(path string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer test-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/system/stats", "/api/v1/connectivity", "/api/v1/telemetry/samples"} {
		if w := get(path, false); w.Code != http.StatusUnauthorized {
			t.Fatal("unprotected telemetry endpoint")
		}
		if w := get(path, true); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cached read failed")
		}
	}
	readPage := func(path string) telemetry.Page {
		var p telemetry.Page
		w := get(path, true)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatal("invalid page response")
		}
		return p
	}
	p := readPage("/api/v1/telemetry/samples?limit=1")
	if len(p.Records) != 1 || !p.HasMore || p.NextCursor.After != 1 {
		t.Fatal("pagination failed")
	}
	a := get("/api/v1/telemetry/samples?limit=1", true)
	c := get("/api/v1/telemetry/samples?limit=1", true)
	if a.Body.String() != c.Body.String() {
		t.Fatal("repeated page consumed or changed identity")
	}
	if next := readPage("/api/v1/telemetry/samples?runId=" + p.RunID + "&after=1&limit=1"); next.Records[0].Sequence != 2 {
		t.Fatal("wrong continuation")
	}
	for _, q := range []string{"limit=0", "limit=129", "limit=1&limit=2", "runId=" + p.RunID, "after=1", "runId=" + p.RunID + "&after=99", "runId=x&after=0", "runId=" + p.RunID + "&after=-1", "url=https://example.test/"} {
		if w := get("/api/v1/telemetry/samples?"+q, true); w.Code != 400 {
			t.Fatalf("accepted invalid cursor: %s", q)
		}
	}
	old := strings.Repeat("f", 32)
	if old == p.RunID {
		old = strings.Repeat("e", 32)
	}
	if changed := readPage("/api/v1/telemetry/samples?runId=" + old + "&after=999"); !changed.RunChanged || changed.Records[0].Sequence != 1 {
		t.Fatal("run discontinuity hidden")
	}
}
