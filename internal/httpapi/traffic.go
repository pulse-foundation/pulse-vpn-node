package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

type trafficReader interface {
	View(context.Context) (traffic.View, error)
}

func trafficRoutes(mux *http.ServeMux, reader trafficReader) {
	mux.HandleFunc("GET /api/v1/traffic", func(w http.ResponseWriter, r *http.Request) {
		if reader == nil {
			http.Error(w, "traffic collection not configured", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		view, err := reader.View(ctx)
		if err != nil {
			http.Error(w, "traffic statistics unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})
}
