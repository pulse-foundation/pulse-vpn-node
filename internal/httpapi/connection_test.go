package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func connectionSettings() *node.VLESS {
	// RFC 7748 Alice private/public key pair: an independent derivation oracle.
	raw, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	return &node.VLESS{Listen: "0.0.0.0", Port: 8443, Users: []node.User{{Name: "private-user", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}}, Reality: node.Reality{Destination: "private-target.test:443", PrivateKey: base64.RawURLEncoding.EncodeToString(raw), ShortIDs: []string{"abcdef"}, ServerNames: []string{"target.test"}}}
}

func TestConnectionDescriptor(t *testing.T) {
	cfg := connectionSettings()
	h := NewWithServices("token", true, func(context.Context) (string, error) { return "v", nil }, Services{StartupVLESS: cfg})
	// The descriptor is an immutable startup snapshot, independent of live-user changes.
	cfg.Reality.ShortIDs[0] = "1234"
	cfg.Users = nil
	for _, auth := range []string{"", "Bearer wrong", "Bearer token"} {
		r := httptest.NewRequest("GET", "/api/v1/connection", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if auth != "Bearer token" {
			if w.Code != 401 {
				t.Fatal("missing authentication")
			}
			continue
		}
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected response %d", w.Code)
		}
		var got struct {
			SchemaVersion int              `json:"schemaVersion"`
			Connection    *node.Connection `json:"connection"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		public, _ := hex.DecodeString("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")
		c := got.Connection
		if got.SchemaVersion != 1 || c == nil || c.Protocol != "vless" || c.Transport.Type != "raw" || c.Transport.Flow != node.VisionFlow || c.ListenPort != 8443 || c.Reality.PublicKey != base64.RawURLEncoding.EncodeToString(public) || strings.Join(c.Reality.ShortIDs, ",") != "abcdef" || strings.Join(c.Reality.ServerNames, ",") != "target.test" {
			t.Fatal("incorrect public descriptor")
		}
		for _, secret := range []string{cfg.Reality.PrivateKey, "privateKey", "users", "uuid", "private-user", "f47ac10b-58cc-4372-a567-0e02b2c3d479", "private-target", "destination", "listen\""} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private material exposed")
			}
		}
	}
}

func TestConnectionAbsentOrInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*node.VLESS)
		absent bool
		code   int
	}{
		{name: "absent", absent: true, code: 200},
		{name: "bad encoding", change: func(v *node.VLESS) { v.Reality.PrivateKey = "secret-invalid-key" }, code: 503},
		{name: "bad length", change: func(v *node.VLESS) { v.Reality.PrivateKey = "YWJj" }, code: 503},
		{name: "bad port", change: func(v *node.VLESS) { v.Port = 0 }, code: 503},
		{name: "bad short ID", change: func(v *node.VLESS) { v.Reality.ShortIDs = []string{"zz"} }, code: 503},
		{name: "bad name", change: func(v *node.VLESS) { v.Reality.ServerNames = nil }, code: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := connectionSettings()
			if tc.absent {
				cfg = nil
			} else {
				tc.change(cfg)
			}
			h := NewWithServices("token", cfg != nil, nil, Services{StartupVLESS: cfg})
			r := httptest.NewRequest("GET", "/api/v1/connection", nil)
			r.Header.Set("Authorization", "Bearer token")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("got %d", w.Code)
			}
			if tc.absent && strings.TrimSpace(w.Body.String()) != `{"schemaVersion":1,"connection":null}` {
				t.Fatal("missing explicit null")
			}
			if tc.code == 503 && strings.TrimSpace(w.Body.String()) != "connection settings unavailable" {
				t.Fatal("unsafe error")
			}
		})
	}
}
