package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerVersionAuthenticatesAndValidates(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		ok         bool
	}{
		{"pinned", `{"version":"v1.19.33"}`, 200, true},
		{"other version", `{"version":"v1.0.0"}`, 200, false},
		{"bad JSON", `garbage`, 200, false},
		{"denied", `secret diagnostics`, 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/version" || r.Header.Get("Authorization") != "Bearer controller-token" {
					t.Error("bad controller request")
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewController(srv.URL, "controller-token")
			v, err := c.Version(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected result: %q %v", v, err)
			}
			if tc.ok && v != "v1.19.33" {
				t.Fatalf("version=%q", v)
			}
		})
	}
}

func TestControllerDoesNotForwardSecretOnRedirect(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	_, err := NewController(source.URL, "private-token").Version(context.Background())
	if err == nil || called {
		t.Fatal("followed controller redirect")
	}
}
