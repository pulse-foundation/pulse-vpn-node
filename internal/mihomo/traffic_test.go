package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const trafficConnectionFixture = `{"id":"connection-1","metadata":{"inboundUser":"alice","host":"private-host","destinationIP":"192.0.2.1","network":"tcp"},"upload":123,"download":456,"start":"2026-10-30T10:00:00Z"}`

func TestTrafficReadsAuthenticatedPayloadCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/connections" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("incorrect traffic request")
		}
		fmt.Fprintf(w, `{"connections":[%s,%s]}`, trafficConnectionFixture, strings.NewReplacer("connection-1", "connection-2", `"alice"`, `""`, `"tcp"`, `"udp"`).Replace(trafficConnectionFixture))
	}))
	defer srv.Close()
	got, err := NewController(srv.URL, "test-token").Traffic(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("traffic: %v, %v", got, err)
	}
	if got[0].ID != "connection-1" || got[0].User != "alice" || got[0].Upload != 123 || got[0].Download != 456 || !got[0].StartedAt.Equal(time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)) || got[1].User != "" {
		t.Fatalf("incorrect attribution/counters: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "private-host") || strings.Contains(string(encoded), "192.0.2.1") {
		t.Fatal("traffic contains private connection metadata")
	}
}

func TestTrafficRejectsWholeInvalidSnapshot(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing list", `{}`}, {"trailing JSON", `{"connections":[]} {}`}, {"object list", `{"connections":{}}`},
		{"missing upload", strings.Replace(trafficConnectionFixture, `"upload":123,`, "", 1)},
		{"null upload", strings.Replace(trafficConnectionFixture, `"upload":123`, `"upload":null`, 1)},
		{"negative upload", strings.Replace(trafficConnectionFixture, `"upload":123`, `"upload":-1`, 1)},
		{"overflow", strings.Replace(trafficConnectionFixture, `"download":456`, `"download":9223372036854775808`, 1)},
		{"missing download", strings.Replace(trafficConnectionFixture, `"download":456,`, "", 1)},
		{"null download", strings.Replace(trafficConnectionFixture, `"download":456`, `"download":null`, 1)},
		{"negative download", strings.Replace(trafficConnectionFixture, `"download":456`, `"download":-1`, 1)},
		{"string counter", strings.Replace(trafficConnectionFixture, `"upload":123`, `"upload":"123"`, 1)},
		{"fractional counter", strings.Replace(trafficConnectionFixture, `"upload":123`, `"upload":1.5`, 1)},
		{"zero start", strings.Replace(trafficConnectionFixture, "2026-10-30T10:00:00Z", "0001-01-01T00:00:00Z", 1)},
		{"bad start", strings.Replace(trafficConnectionFixture, "2026-10-30T10:00:00Z", "private-diagnostic", 1)},
		{"missing start", strings.Replace(trafficConnectionFixture, `,"start":"2026-10-30T10:00:00Z"`, "", 1)},
		{"null start", strings.Replace(trafficConnectionFixture, `"start":"2026-10-30T10:00:00Z"`, `"start":null`, 1)},
		{"missing identity", strings.Replace(trafficConnectionFixture, `"inboundUser":"alice"`, `"other":"alice"`, 1)},
		{"invalid ID", strings.Replace(trafficConnectionFixture, "connection-1", "bad/id", 1)},
		{"duplicate ID", fmt.Sprintf(`{"connections":[%s,%s]}`, trafficConnectionFixture, trafficConnectionFixture)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if strings.Contains(body, `"id"`) && !strings.Contains(body, `"connections"`) {
				body = fmt.Sprintf(`{"connections":[%s,%s]}`, strings.Replace(trafficConnectionFixture, "connection-1", "valid", 1), body)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			got, err := NewController(srv.URL, "test-token").Traffic(context.Background())
			if err == nil || got != nil {
				t.Fatal("accepted partial or invalid snapshot")
			}
			if strings.Contains(err.Error(), "private-diagnostic") || strings.Contains(err.Error(), "private-host") {
				t.Fatal("leaked response diagnostics")
			}
		})
	}
}

func TestTrafficEmptyAndBounds(t *testing.T) {
	var many strings.Builder
	many.WriteString(`{"connections":[`)
	for i := 0; i < 16385; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString(strings.Replace(trafficConnectionFixture, "connection-1", fmt.Sprintf("id-%d", i), 1))
	}
	many.WriteString(`]}`)
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"empty", `{"connections":[]}`, true}, {"null empty", `{"connections":null}`, true},
		{"too many", many.String(), false}, {"oversized", `{"connections":[],"padding":"` + strings.Repeat("x", 8<<20) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			got, err := NewController(srv.URL, "").Traffic(context.Background())
			if (err == nil) != tc.ok || len(got) != 0 {
				t.Fatalf("result: %d %v", len(got), err)
			}
		})
	}
}

func TestTrafficDoesNotUseProxyOrFollowRedirects(t *testing.T) {
	var called atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer target.Close()
	t.Setenv("HTTP_PROXY", target.URL)
	t.Setenv("HTTPS_PROXY", target.URL)
	t.Setenv("NO_PROXY", "")
	c := NewController("http://unresolvable.invalid", "secret")
	if _, err := c.Traffic(context.Background()); err == nil || called.Load() {
		t.Fatal("traffic used environment proxy")
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	if _, err := NewController(source.URL, "secret").Traffic(context.Background()); err == nil || called.Load() {
		t.Fatal("traffic followed redirect")
	}
}

func TestProcessTrafficCancelledWithoutWaitingForMutation(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer srv.Close()
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	p := &Process{controller: NewController(srv.URL, ""), lifetime: lifetime, done: make(chan struct{}), operations: make(chan struct{}, 1)}
	result := make(chan error, 1)
	go func() { _, err := p.Traffic(context.Background()); result <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("traffic waited for mutation lock")
	}
	stop()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("accepted cancelled traffic")
		}
	case <-time.After(time.Second):
		t.Fatal("child lifetime did not cancel traffic")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request not cancelled")
	}
	if got, err := p.Traffic(context.Background()); err == nil || got != nil {
		t.Fatal("read stopped process")
	}
}

func TestTrafficRequestContextCancelsRead(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := NewController(srv.URL, "").Traffic(ctx); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("accepted cancelled read")
		}
	case <-time.After(time.Second):
		t.Fatal("request ignored cancellation")
	}
}
