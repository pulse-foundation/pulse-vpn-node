package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Status struct {
	Status        string     `json:"status"`
	VPNConfigured bool       `json:"vpnConfigured"`
	Core          CoreStatus `json:"core"`
}

type CoreStatus struct {
	Version string `json:"version,omitempty"`
}

// New exposes process readiness only. It does not claim VPN connectivity.
func New(token string, check func(context.Context) (string, error)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		version, err := check(ctx)
		status := Status{Status: "ready", Core: CoreStatus{Version: version}}
		code := http.StatusOK
		if err != nil {
			status = Status{Status: "degraded"}
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(status)
	})
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		auth := r.Header.Get("Authorization")
		provided := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
		if token == "" || len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
