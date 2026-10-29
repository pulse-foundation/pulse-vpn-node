package mihomo

import (
	"encoding/json"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

// render is deterministic and has no I/O. JSON is accepted by Mihomo's YAML reader.
func render(address, secret string, vpn *node.VLESS) ([]byte, error) {
	listeners := []any{}
	if vpn != nil {
		if err := vpn.Validate(); err != nil {
			return nil, err
		}
		users := make([]map[string]string, 0, len(vpn.Users))
		for _, u := range vpn.Users {
			users = append(users, map[string]string{"username": u.Name, "uuid": u.UUID, "flow": node.VisionFlow})
		}
		listeners = append(listeners, map[string]any{
			"name": "pulse-vless", "type": "vless", "listen": vpn.Listen,
			"port": vpn.Port, "users": users,
			"reality-config": map[string]any{
				"dest": vpn.Reality.Destination, "private-key": vpn.Reality.PrivateKey,
				"short-id": vpn.Reality.ShortIDs, "server-names": vpn.Reality.ServerNames,
			},
		})
	}
	return json.Marshal(map[string]any{
		"mode": "direct", "log-level": "silent", "ipv6": false,
		"external-controller": address, "secret": secret,
		"allow-lan": false, "mixed-port": 0, "port": 0, "socks-port": 0,
		"redir-port": 0, "tproxy-port": 0,
		"dns":             map[string]any{"enable": false},
		"profile":         map[string]any{"store-selected": false, "store-fake-ip": false},
		"geo-auto-update": false, "find-process-mode": "off",
		"listeners": listeners, "rules": []any{},
	})
}
