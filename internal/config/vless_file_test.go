package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const syntheticVLESS = `{"listen":"0.0.0.0","port":8443,"users":[{"name":"local","uuid":"f47ac10b-58cc-4372-a567-0e02b2c3d479"}],"reality":{"destination":"target:8443","privateKey":"a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s","shortIds":["abcd"],"serverNames":["target.test"]}}`

func TestVLESSFileStrictParsing(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"valid", syntheticVLESS, true},
		{"unknown field", strings.Replace(syntheticVLESS, `"port":8443`, `"port":8443,"allowInsecure":true`, 1), false},
		{"extra document", syntheticVLESS + `{}`, false},
		{"null", `null`, false},
		{"invalid", strings.Replace(syntheticVLESS, `"abcd"`, `"secret-invalid"`, 1), false},
		{"oversized", syntheticVLESS + strings.Repeat(" ", 65537), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "vpn.json")
			if err := os.WriteFile(p, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			v, err := loadVLESS(p)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.ok && (v == nil || v.Port != 8443) {
				t.Fatal("settings not loaded")
			}
			if err != nil && strings.Contains(err.Error(), "secret-invalid") {
				t.Fatal("error leaks credentials")
			}
		})
	}
}
