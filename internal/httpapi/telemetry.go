package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

func telemetryRoutes(mux *http.ServeMux, buffer *telemetry.Buffer, interval, probeInterval time.Duration) {
	available := func(w http.ResponseWriter) bool {
		if buffer == nil {
			http.Error(w, "telemetry unavailable", http.StatusServiceUnavailable)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v1/system/stats", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			System  telemetry.Latest `json:"system"`
			Runtime telemetry.Latest `json:"runtime"`
		}{buffer.Latest("system", 3*interval), buffer.Latest("runtime", 3*interval)})
	})
	mux.HandleFunc("GET /api/v1/connectivity", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		writeJSON(w, http.StatusOK, buffer.Latest("connectivity", 3*probeInterval))
	})
	mux.HandleFunc("GET /api/v1/telemetry/samples", func(w http.ResponseWriter, r *http.Request) {
		if !available(w) {
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		bad := err != nil
		for key, values := range query {
			if len(values) != 1 || (key != "runId" && key != "after" && key != "limit") {
				bad = true
			}
		}
		_, hasRun := query["runId"]
		_, hasAfter := query["after"]
		if hasRun != hasAfter {
			bad = true
		}
		after := uint64(0)
		if hasAfter {
			after, err = strconv.ParseUint(query.Get("after"), 10, 64)
			if err != nil || query.Get("runId") == "" {
				bad = true
			}
		}
		limit := 64
		if values, ok := query["limit"]; ok {
			limit, err = strconv.Atoi(values[0])
			if err != nil {
				bad = true
			}
		}
		if bad || limit < 1 || limit > telemetry.MaxPageSize {
			http.Error(w, "invalid telemetry cursor or limit", http.StatusBadRequest)
			return
		}
		page, err := buffer.Page(query.Get("runId"), after, limit)
		if err != nil {
			http.Error(w, "invalid telemetry cursor or limit", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
}
