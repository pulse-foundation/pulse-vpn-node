package mihomo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func TestRenderVLESSRealityAndIdle(t *testing.T) {
	vpn := &node.VLESS{Listen: "0.0.0.0", Port: 8443, Users: []node.User{{Name: "local", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}}, Reality: node.Reality{Destination: "target:8443", PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ShortIDs: []string{"abcd"}, ServerNames: []string{"target.test"}}}
	raw, err := render("127.0.0.1:19090", "controller-secret", vpn)
	if err != nil {
		t.Fatal(err)
	}
	second, err := render("127.0.0.1:19090", "controller-secret", vpn)
	if err != nil || !bytes.Equal(raw, second) {
		t.Fatal("render is not deterministic")
	}
	var got struct {
		Controller string `json:"external-controller"`
		Listeners  []struct {
			Type  string `json:"type"`
			Port  int    `json:"port"`
			Users []struct {
				Flow string `json:"flow"`
				UUID string `json:"uuid"`
			} `json:"users"`
			Reality struct {
				PrivateKey string   `json:"private-key"`
				Dest       string   `json:"dest"`
				ShortIDs   []string `json:"short-id"`
			} `json:"reality-config"`
		} `json:"listeners"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Controller != "127.0.0.1:19090" || len(got.Listeners) != 1 {
		t.Fatal("invalid controller/listener output")
	}
	l := got.Listeners[0]
	if l.Type != "vless" || l.Port != 8443 || len(l.Users) != 1 || l.Users[0].Flow != "xtls-rprx-vision" || l.Users[0].UUID != vpn.Users[0].UUID || l.Reality.PrivateKey != vpn.Reality.PrivateKey || l.Reality.Dest != "target:8443" || len(l.Reality.ShortIDs) != 1 {
		t.Fatal("missing VLESS/REALITY settings")
	}
	idle, err := render("127.0.0.1:19090", "controller-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(idle, &got); err != nil || len(got.Listeners) != 0 {
		t.Fatal("idle config has listener")
	}
	vpn.Reality.PrivateKey = ""
	if _, err := render("127.0.0.1:19090", "controller-secret", vpn); err == nil {
		t.Fatal("invalid REALITY rendered as plaintext")
	}
}
