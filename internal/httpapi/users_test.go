package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

type apiUsers struct{ changes int }

func (s *apiUsers) Initialize(context.Context, []node.User) (node.UserSnapshot, error) {
	return s.Snapshot(context.Background())
}
func (s *apiUsers) Snapshot(context.Context) (node.UserSnapshot, error) {
	return node.UserSnapshot{Revision: 1, Users: []node.ManagedUser{{Name: "user", UUID: "private-uuid", Enabled: true}}}, nil
}
func (s *apiUsers) Change(context.Context, node.UserChange) (node.Operation, error) {
	s.changes++
	return node.Operation{Revision: 2, State: "queued"}, nil
}
func (s *apiUsers) Operation(context.Context, int64) (node.Operation, error) {
	return node.Operation{Revision: 2, State: "queued"}, nil
}
func (s *apiUsers) RecordApply(context.Context, int64, string) error { return nil }

func TestUserRoutesAuthenticateBoundInputsAndOmitCredentials(t *testing.T) {
	store := &apiUsers{}
	manager := node.NewUserManager(store, nil)
	handler := NewWithUsers("token", true, func(context.Context) (string, error) { return "version", nil }, manager)
	for _, tc := range []struct {
		method, path, body, auth string
		code                     int
	}{
		{"GET", "/api/v1/users", "", "", 401},
		{"PUT", "/api/v1/users/user", `{"enabled":true,"expectedRevision":1}`, "", 401},
		{"GET", "/api/v1/users", "", "token", 200},
		{"PUT", "/api/v1/users/user", `{"enabled":false,"expectedRevision":1}`, "token", 202},
		{"PUT", "/api/v1/users/user", `{"enabled":false}`, "token", 400},
		{"PUT", "/api/v1/users/user", `{"expectedRevision":1}`, "token", 400},
		{"PUT", "/api/v1/users/user", `{"enabled":false,"expectedRevision":1,"privateKey":"secret"}`, "token", 400},
		{"PUT", "/api/v1/users/user", `{"enabled":false,"expectedRevision":1} {}`, "token", 400},
		{"PUT", "/api/v1/users/user", strings.Repeat("x", 5000), "token", 400},
		{"GET", "/api/v1/operations/2", "", "token", 200},
		{"GET", "/api/v1/operations/not-a-revision", "", "token", 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Idempotency-Key", "test")
		if tc.auth != "" {
			req.Header.Set("Authorization", "Bearer "+tc.auth)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != tc.code {
			t.Fatalf("%s %s: %d != %d", tc.method, tc.path, out.Code, tc.code)
		}
		if strings.Contains(out.Body.String(), "private-uuid") || strings.Contains(out.Body.String(), "secret") {
			t.Fatal("credential echoed")
		}
	}
	if store.changes != 1 {
		t.Fatal("invalid or unauthorized requests reached store")
	}
}

func TestUserLifecycleRoutes(t *testing.T) {
	manager := node.NewUserManager(&apiUsers{}, nil)
	handler := NewWithUsers("token", true, func(context.Context) (string, error) { return "version", nil }, manager)
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"DELETE", "/api/v1/users/user", `{"expectedRevision":1}`, 202},
		{"POST", "/api/v1/users/user/rotate", `{"expectedRevision":1,"uuid":"f47ac10b-58cc-4372-a567-0e02b2c3d479"}`, 202},
		{"POST", "/api/v1/users/user/rotate", `{"expectedRevision":1}`, 400},
		{"DELETE", "/api/v1/users/user", `{"expectedRevision":1,"uuid":"secret"}`, 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Idempotency-Key", "lifecycle")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != tc.code {
			t.Errorf("%s: got %d, want %d", tc.path, out.Code, tc.code)
		}
	}
}
