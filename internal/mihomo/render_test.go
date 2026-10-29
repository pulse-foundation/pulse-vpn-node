package mihomo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
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

// Removing a credential must also reject flows created by an already authenticated
// UDP/mux parent after its outbound tracker was closed by the controller.
func TestRenderAuthorizesOnlyCurrentInboundUsers(t *testing.T) {
	vpn := &node.VLESS{Listen: "0.0.0.0", Port: 8443, Users: []node.User{{Name: "alice", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}, {Name: "bob", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d480"}}, Reality: node.Reality{Destination: "target:8443", PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ShortIDs: []string{"abcd"}, ServerNames: []string{"target.test"}}}
	for _, names := range [][]string{{"alice", "bob"}, {"bob"}, {}} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			snapshot := *vpn
			snapshot.Users = nil
			for _, name := range names {
				for _, u := range vpn.Users {
					if u.Name == name {
						snapshot.Users = append(snapshot.Users, u)
					}
				}
			}
			raw, err := render("127.0.0.1:19090", "controller-secret", &snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Mode      string   `json:"mode"`
				Rules     []string `json:"rules"`
				Listeners []struct {
					Reality struct {
						Proxy string `json:"proxy"`
					} `json:"reality-config"`
				} `json:"listeners"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Mode != "rule" {
				t.Fatal("identity authorization rules are bypassed")
			}
			expected := make([]string, 0, len(names)+1)
			for _, name := range names {
				expected = append(expected, "IN-USER,"+name+",DIRECT")
			}
			expected = append(expected, "MATCH,REJECT")
			if !slices.Equal(got.Rules, expected) {
				t.Fatal("egress is not limited to current identities")
			}
			if len(got.Listeners) != 1 || got.Listeners[0].Reality.Proxy != "DIRECT" {
				t.Fatal("REALITY target cannot dial independently of user authorization")
			}
		})
	}
}
