package mihomo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/netip"
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
			var authorization []string
			for _, rule := range got.Rules {
				if strings.HasPrefix(rule, "IN-USER,") || strings.HasPrefix(rule, "MATCH,") {
					authorization = append(authorization, rule)
				} else if len(authorization) != 0 {
					t.Fatal("destination restrictions must precede user authorization")
				}
			}
			if !slices.Equal(authorization, expected) {
				t.Fatal("egress is not limited to current identities")
			}
			if len(got.Listeners) != 1 || got.Listeners[0].Reality.Proxy != "DIRECT" {
				t.Fatal("REALITY target cannot dial independently of user authorization")
			}
		})
	}
}

// A missing prefix, no-resolve, or placing a deny after IN-USER would expose
// internal services even though the user's credentials are valid.
func TestRenderPrivateDestinations(t *testing.T) {
	vpn := &node.VLESS{Listen: "0.0.0.0", Port: 8443, Users: []node.User{{Name: "alice", UUID: "f47ac10b-58cc-4372-a567-0e02b2c3d479"}}, Reality: node.Reality{Destination: "target:8443", PrivateKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ShortIDs: []string{"abcd"}, ServerNames: []string{"target.test"}}}
	for _, snapshot := range []*node.VLESS{nil, vpn} {
		raw, err := render("127.0.0.1:19090", "controller-secret", snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Rules []string `json:"rules"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		var denied []netip.Prefix
		for _, rule := range got.Rules {
			if strings.HasPrefix(rule, "IN-USER,") || strings.HasPrefix(rule, "MATCH,") {
				break
			}
			parts := strings.Split(rule, ",")
			if len(parts) != 3 || (parts[0] != "IP-CIDR" && parts[0] != "IP-CIDR6") || parts[2] != "REJECT" {
				t.Fatalf("destination denial must resolve DNS: %q", rule)
			}
			prefix, err := netip.ParsePrefix(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			denied = append(denied, prefix)
		}
		for _, tc := range []struct {
			ip      string
			blocked bool
		}{
			{"0.1.2.3", true}, {"10.255.255.255", true}, {"100.64.0.1", true}, {"100.127.255.255", true},
			{"127.0.0.1", true}, {"169.254.169.254", true}, {"172.16.0.1", true}, {"172.31.255.255", true},
			{"192.168.1.1", true}, {"198.18.0.1", true}, {"224.0.0.1", true}, {"255.255.255.255", true},
			{"::", true}, {"::1", true}, {"fc00::1", true}, {"fdff::1", true}, {"fe80::1", true}, {"ff02::1", true},
			{"64:ff9b::a00:1", true}, {"2001:db8::1", true},
			{"1.1.1.1", false}, {"8.8.8.8", false}, {"100.63.255.255", false}, {"100.128.0.0", false},
			{"172.15.255.255", false}, {"172.32.0.0", false}, {"192.169.0.1", false}, {"2606:4700:4700::1111", false},
		} {
			addr := netip.MustParseAddr(tc.ip)
			blocked := slices.ContainsFunc(denied, func(p netip.Prefix) bool { return p.Contains(addr) })
			if blocked != tc.blocked {
				t.Errorf("destination %s: blocked=%v, want %v", tc.ip, blocked, tc.blocked)
			}
		}
	}
}
