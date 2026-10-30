package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func connectionRoutes(mux *http.ServeMux, startup *node.VLESS) {
	connection, err := node.DescribeConnection(startup)
	mux.HandleFunc("GET /api/v1/connection", func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(w, "connection settings unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			SchemaVersion int              `json:"schemaVersion"`
			Connection    *node.Connection `json:"connection"`
		}{SchemaVersion: 1, Connection: connection})
	})
}
