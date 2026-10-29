package node

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validVLESS() VLESS {
	return VLESS{Listen: "0.0.0.0", Port: 8443, Users: []User{{Name: "local", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}}, Reality: Reality{Destination: "target:8443", PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ShortIDs: []string{"abcdef0123456789"}, ServerNames: []string{"target.test"}}}
}

func TestVLESSValidation(t *testing.T) {
	v := validVLESS()
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*VLESS)
	}{
		{"no users", func(v *VLESS) { v.Users = nil }},
		{"bad uuid", func(v *VLESS) { v.Users[0].UUID = "secret-bad-uuid" }},
		{"duplicate uuid", func(v *VLESS) { v.Users = append(v.Users, User{Name: "other", UUID: strings.ToUpper(v.Users[0].UUID)}) }},
		{"duplicate name", func(v *VLESS) {
			v.Users = append(v.Users, User{Name: "local", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d480"})
		}},
		{"bad listen", func(v *VLESS) { v.Listen = "target" }},
		{"bad port", func(v *VLESS) { v.Port = 0 }},
		{"bad destination", func(v *VLESS) { v.Reality.Destination = "target" }},
		{"missing private key", func(v *VLESS) { v.Reality.PrivateKey = "" }},
		{"bad private key", func(v *VLESS) { v.Reality.PrivateKey = "secret-bad-key" }},
		{"empty short ids", func(v *VLESS) { v.Reality.ShortIDs = nil }},
		{"empty short id", func(v *VLESS) { v.Reality.ShortIDs = []string{""} }},
		{"bad short id", func(v *VLESS) { v.Reality.ShortIDs = []string{"xyz"} }},
		{"long short id", func(v *VLESS) { v.Reality.ShortIDs = []string{"abcdef012345678900"} }},
		{"empty names", func(v *VLESS) { v.Reality.ServerNames = nil }},
		{"bad name", func(v *VLESS) { v.Reality.ServerNames = []string{"https://target.test"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := validVLESS()
			tc.change(&v)
			err := v.Validate()
			if err == nil {
				t.Fatal("accepted invalid config")
			}
			if strings.Contains(err.Error(), "secret-bad") {
				t.Fatal("validation exposed secret")
			}
		})
	}
}
