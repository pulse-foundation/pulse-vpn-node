package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func userRoutes(mux *http.ServeMux, manager *node.UserManager) {
	unavailable := func(w http.ResponseWriter) bool {
		if manager == nil {
			http.Error(w, "user management not configured", http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	mux.HandleFunc("GET /api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		if unavailable(w) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		view, err := manager.View(ctx)
		if err != nil {
			userError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("PUT /api/v1/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		if unavailable(w) {
			return
		}
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			userError(w, node.ErrInvalid)
			return
		}
		body := struct {
			UUID             string `json:"uuid"`
			Enabled          *bool  `json:"enabled"`
			ExpectedRevision *int64 `json:"expectedRevision"`
		}{}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			userError(w, node.ErrInvalid)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || body.Enabled == nil || body.ExpectedRevision == nil {
			userError(w, node.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		operation, err := manager.Change(ctx, node.UserChange{Key: r.Header.Get("Idempotency-Key"), Name: r.PathValue("name"), UUID: body.UUID, Enabled: *body.Enabled, ExpectedRevision: *body.ExpectedRevision})
		if err != nil {
			userError(w, err)
			return
		}
		w.Header().Set("Location", "/api/v1/operations/"+strconv.FormatInt(operation.Revision, 10))
		writeJSON(w, http.StatusAccepted, operation)
	})
	mux.HandleFunc("GET /api/v1/operations/{revision}", func(w http.ResponseWriter, r *http.Request) {
		if unavailable(w) {
			return
		}
		revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		if err != nil || revision < 1 {
			userError(w, node.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		operation, err := manager.Operation(ctx, revision)
		if err != nil {
			userError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, operation)
	})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func userError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, node.ErrInvalid):
		http.Error(w, "invalid user change", http.StatusBadRequest)
	case errors.Is(err, node.ErrConflict):
		http.Error(w, "user revision or identity conflict", http.StatusConflict)
	case errors.Is(err, node.ErrNotFound):
		http.Error(w, "user or operation not found", http.StatusNotFound)
	default:
		http.Error(w, "user management unavailable", http.StatusServiceUnavailable)
	}
}
