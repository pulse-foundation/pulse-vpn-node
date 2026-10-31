package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func userRoutes(mux *http.ServeMux, manager *node.UserManager) {
	mux.HandleFunc("GET /api/v1/access-policy/events", func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			userError(w, node.ErrUnavailable)
			return
		}
		after := int64(0)
		var err error
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseInt(raw, 10, 64)
		}
		if err != nil || after < 0 {
			userError(w, node.ErrInvalid)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		events, err := manager.AccessPolicyEvents(ctx, after)
		if err != nil {
			userError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Events []node.AccessPolicyEvent `json:"events"`
		}{events})
	})
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
			AccessPolicy     json.RawMessage `json:"accessPolicy"`
			UUID             string          `json:"uuid"`
			Enabled          *bool           `json:"enabled"`
			ExpectedRevision *int64          `json:"expectedRevision"`
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
		policy, err := decodeAccessPolicy(body.AccessPolicy)
		if err != nil {
			userError(w, err)
			return
		}
		operation, err := manager.Change(ctx, node.UserChange{AccessPolicy: policy, Key: r.Header.Get("Idempotency-Key"), Name: r.PathValue("name"), UUID: body.UUID, Enabled: *body.Enabled, ExpectedRevision: *body.ExpectedRevision})
		if err != nil {
			userError(w, err)
			return
		}
		w.Header().Set("Location", "/api/v1/operations/"+strconv.FormatInt(operation.Revision, 10))
		writeJSON(w, http.StatusAccepted, operation)
	})
	for _, route := range []struct{ pattern, kind string }{{"DELETE /api/v1/users/{name}", "delete"}, {"POST /api/v1/users/{name}/rotate", "rotate"}} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			if unavailable(w) {
				return
			}
			if len(r.Header.Values("Idempotency-Key")) != 1 {
				userError(w, node.ErrInvalid)
				return
			}
			var body struct {
				AccessPolicy     json.RawMessage `json:"accessPolicy"`
				ExpectedRevision *int64          `json:"expectedRevision"`
				UUID             *string         `json:"uuid"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil {
				userError(w, node.ErrInvalid)
				return
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF || body.ExpectedRevision == nil || (route.kind == "delete" && (body.UUID != nil || len(body.AccessPolicy) > 0)) || (route.kind == "rotate" && (body.UUID == nil || *body.UUID == "")) {
				userError(w, node.ErrInvalid)
				return
			}
			uuid := ""
			if body.UUID != nil {
				uuid = *body.UUID
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			policy, err := decodeAccessPolicy(body.AccessPolicy)
			if err != nil || policy != nil && policy.Renew {
				userError(w, node.ErrInvalid)
				return
			}
			operation, err := manager.Change(ctx, node.UserChange{AccessPolicy: policy, Kind: route.kind, Key: r.Header.Get("Idempotency-Key"), Name: r.PathValue("name"), UUID: uuid, ExpectedRevision: *body.ExpectedRevision})
			if err != nil {
				userError(w, err)
				return
			}
			w.Header().Set("Location", "/api/v1/operations/"+strconv.FormatInt(operation.Revision, 10))
			writeJSON(w, http.StatusAccepted, operation)
		})
	}
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

func decodeAccessPolicy(raw json.RawMessage) (*node.AccessPolicyChange, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var body struct {
		Revision  *int64          `json:"revision"`
		ExpiresAt json.RawMessage `json:"expiresAt"`
		Renew     *bool           `json:"renew"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.Revision == nil || *body.Revision < 1 || len(body.ExpiresAt) == 0 || body.Renew == nil {
		return nil, node.ErrInvalid
	}
	var deadline *time.Time
	if string(body.ExpiresAt) != "null" {
		var value string
		if json.Unmarshal(body.ExpiresAt, &value) != nil || !strings.HasSuffix(value, "Z") {
			return nil, node.ErrInvalid
		}
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil, node.ErrInvalid
		}
		deadline = &t
	}
	return &node.AccessPolicyChange{Revision: *body.Revision, ExpiresAt: deadline, Renew: *body.Renew}, nil
}
