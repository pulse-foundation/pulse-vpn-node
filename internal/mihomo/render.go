package mihomo

import "encoding/json"

// renderIdle uses JSON, which Mihomo's YAML reader accepts. No listener is configured.
func renderIdle(address, secret string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"mode": "direct", "log-level": "silent", "ipv6": false,
		"external-controller": address, "secret": secret,
		"allow-lan": false, "mixed-port": 0, "port": 0, "socks-port": 0,
		"redir-port": 0, "tproxy-port": 0,
		"dns":             map[string]any{"enable": false},
		"profile":         map[string]any{"store-selected": false, "store-fake-ip": false},
		"geo-auto-update": false, "find-process-mode": "off",
		"listeners": []any{}, "rules": []any{},
	})
}
