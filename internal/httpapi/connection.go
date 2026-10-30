package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func connectionRoutes(mux *http.ServeMux, startup *node.VLESS, manager *node.UserManager) {
	connection, err := node.DescribeConnection(startup)
	mux.HandleFunc("GET /api/v1/connection", func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(w, "connection settings unavailable", http.StatusServiceUnavailable)
			return
		}
		current := connection
		if manager != nil && manager.Managed() {
			current = manager.Connection()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			SchemaVersion int              `json:"schemaVersion"`
			Connection    *node.Connection `json:"connection"`
		}{SchemaVersion: 1, Connection: current})
	})
	mux.HandleFunc("PUT /api/v1/connection", func(w http.ResponseWriter, r *http.Request) {
		if manager == nil || !manager.Managed() {
			http.Error(w, "managed connection creation unavailable", http.StatusServiceUnavailable)
			return
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			userError(w, node.ErrInvalid)
			return
		}
		var body node.ConnectionChange
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil {
			userError(w, node.ErrInvalid)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || body.ExpectedRevision < 1 {
			userError(w, node.ErrInvalid)
			return
		}
		body.Key = r.Header.Get("Idempotency-Key")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		operation, err := manager.CreateConnection(ctx, body)
		if err != nil {
			userError(w, err)
			return
		}
		w.Header().Set("Location", "/api/v1/operations/"+strconv.FormatInt(operation.Revision, 10))
		writeJSON(w, http.StatusAccepted, operation)
	})
}
