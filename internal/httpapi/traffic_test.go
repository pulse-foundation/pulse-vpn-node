package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

type trafficReaderFunc func(context.Context) (traffic.View, error)

func (f trafficReaderFunc) View(ctx context.Context) (traffic.View, error) { return f(ctx) }

func TestTrafficEndpoint(t *testing.T) {
	checks := 0
	reader := trafficReaderFunc(func(ctx context.Context) (traffic.View, error) {
		checks++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded database read")
		}
		return traffic.View{Approximate: true, State: "collecting", Totals: traffic.Totals{Users: []traffic.UserTotal{{Name: "alice", Enabled: false, UploadBytes: 9007199254740993, DownloadBytes: 8}}}}, nil
	})
	h := NewWithTraffic("test-token", true, func(context.Context) (string, error) { t.Fatal("traffic read probed core"); return "", nil }, nil, reader)
	request := func(auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/traffic", nil)
		if auth {
			r.Header.Set("Authorization", "Bearer test-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(false); w.Code != 401 || checks != 0 {
		t.Fatal("traffic route is unauthenticated")
	}
	w := request(true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"uploadBytes":"9007199254740993"`) || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal("traffic response lost integer precision, disabled user, or cache protection")
	}
	reader = func(context.Context) (traffic.View, error) { return traffic.View{}, errors.New("private DSN") }
	h = NewWithTraffic("test-token", true, nil, nil, reader)
	if w = request(true); w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("unsafe unavailable response")
	}
	h = New("test-token", false, nil)
	if w = request(true); w.Code != 503 {
		t.Fatal("idle node should report traffic unavailable")
	}
}
