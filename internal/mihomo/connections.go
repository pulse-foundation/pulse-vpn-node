package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
)

const maxSnapshotBytes = 8 << 20
const maxSnapshotConnections = 16384
const maxSweepDeletes = 128

var connectionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var inboundUserPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type trackedConnection struct {
	ID       string          `json:"id"`
	Upload   json.RawMessage `json:"upload"`
	Download json.RawMessage `json:"download"`
	Start    json.RawMessage `json:"start"`
	Metadata *struct {
		InboundUser *string `json:"inboundUser"`
	} `json:"metadata"`
}

func (c *Controller) SweepRevoked(ctx context.Context, names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	revoked := make(map[string]bool, len(names))
	for _, name := range names {
		if !inboundUserPattern.MatchString(name) {
			return 0, errors.New("invalid revoked identity")
		}
		revoked[name] = true
	}
	connections, err := c.connections(ctx)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, conn := range connections {
		if !revoked[*conn.Metadata.InboundUser] {
			continue
		}
		if closed == maxSweepDeletes {
			return closed, errors.New("core connection cleanup pending: sweep limit reached")
		}
		res, err := c.request(ctx, http.MethodDelete, "/connections/"+conn.ID, nil)
		if err != nil {
			return closed, err
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			return closed, errors.New("core connection cleanup incomplete")
		}
		closed++
	}
	return closed, nil
}

// connections validates the whole snapshot before callers act on any entry.
// Traffic counters remain raw here: revocation only requires connection identity.
func (c *Controller) connections(ctx context.Context) ([]trackedConnection, error) {
	res, err := c.request(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("core connection snapshot unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSnapshotBytes+1))
	if err != nil || len(data) > maxSnapshotBytes {
		return nil, errors.New("core connection snapshot incomplete or oversized")
	}
	var snapshot struct {
		Connections json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Connections == nil {
		return nil, errors.New("invalid core connection snapshot")
	}
	// Decode incrementally so an oversized list cannot allocate an unbounded
	// slice of entries before the connection limit is checked.
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Connections))
	token, err := decoder.Token()
	if err != nil {
		return nil, errors.New("invalid core connection snapshot")
	}
	if token == nil { // Mihomo may encode an empty tracker list as null.
		return nil, nil
	}
	if token != json.Delim('[') {
		return nil, errors.New("invalid core connection snapshot")
	}
	var connections []trackedConnection
	for decoder.More() {
		if len(connections) == maxSnapshotConnections {
			return nil, errors.New("invalid core connection snapshot")
		}
		var conn trackedConnection
		if err := decoder.Decode(&conn); err != nil {
			return nil, errors.New("invalid core connection snapshot")
		}
		connections = append(connections, conn)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return nil, errors.New("invalid core connection snapshot")
	}
	// Validate the entire bounded snapshot before making any destructive call.
	seen := make(map[string]bool, len(connections))
	for _, conn := range connections {
		if !connectionIDPattern.MatchString(conn.ID) || seen[conn.ID] || conn.Metadata == nil || conn.Metadata.InboundUser == nil {
			return nil, errors.New("invalid core connection identity")
		}
		seen[conn.ID] = true
	}
	return connections, nil
}
