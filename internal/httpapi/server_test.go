package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusAuthenticatesBeforeCheckingCore(t *testing.T) {
	calls := 0
	token := strings.Repeat("a", 64)
	handler := New(token, false, func(context.Context) (string, error) { calls++; return "v1.19.33", nil })
	for _, auth := range []string{"", "Bearer wrong", "Basic " + token, "Bearer " + token + "extra"} {
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		req.Header.Set("Authorization", auth)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", res.Code)
		}
	}
	if calls != 0 {
		t.Fatal("unauthenticated requests reached core")
	}
}

func TestStatusReadyAndDegraded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		code  int
		state string
	}{
		{"ready", nil, 200, "ready"}, {"degraded", errors.New("secret child diagnostics"), 503, "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New("test-token", false, func(context.Context) (string, error) { return "v1.19.33", tc.err })
			req := httptest.NewRequest("GET", "/api/v1/status", nil)
			req.Header.Set("Authorization", "Bearer test-token")
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.code {
				t.Fatalf("status=%d", res.Code)
			}
			var got struct {
				Status        string `json:"status"`
				VPNConfigured bool   `json:"vpnConfigured"`
				Core          struct {
					Version string `json:"version"`
				} `json:"core"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.state || got.VPNConfigured {
				t.Fatalf("unexpected status: %s", res.Body.String())
			}
			if tc.err == nil && got.Core.Version != "v1.19.33" {
				t.Fatal("missing core version")
			}
			if strings.Contains(res.Body.String(), "secret") {
				t.Fatal("exposed core error")
			}
			if res.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("status may be cached")
			}
		})
	}
}

func TestUnknownRouteAndWrongMethod(t *testing.T) {
	handler := New("test-token", false, func(context.Context) (string, error) { t.Fatal("unexpected core request"); return "", nil })
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/api/v1/missing", 404}, {"POST", "/api/v1/status", 405}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Fatalf("%s %s: %d", tc.method, tc.path, res.Code)
		}
	}
}

func TestStatusReportsConfiguredVPNWithoutExposingSettings(t *testing.T) {
	handler := New("test-token", true, func(context.Context) (string, error) { return "v1.19.33", nil })
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	var got Status
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || !got.VPNConfigured {
		t.Fatal("configured listener not reported")
	}
	if strings.Contains(res.Body.String(), "uuid") || strings.Contains(res.Body.String(), "privateKey") {
		t.Fatal("VPN settings exposed")
	}
}
