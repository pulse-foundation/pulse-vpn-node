package mihomo

import (
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
	ID       string `json:"id"`
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
	res, err := c.request(ctx, http.MethodGet, "/connections", nil)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, errors.New("core connection snapshot unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSnapshotBytes+1))
	if err != nil || len(data) > maxSnapshotBytes {
		return 0, errors.New("core connection snapshot incomplete or oversized")
	}
	var snapshot struct {
		Connections json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Connections == nil {
		return 0, errors.New("invalid core connection snapshot")
	}
	var connections []trackedConnection
	if err := json.Unmarshal(snapshot.Connections, &connections); err != nil || len(connections) > maxSnapshotConnections {
		return 0, errors.New("invalid core connection snapshot")
	}
	// Validate the entire bounded snapshot before making any destructive call.
	seen := make(map[string]bool, len(connections))
	for _, conn := range connections {
		if !connectionIDPattern.MatchString(conn.ID) || seen[conn.ID] || conn.Metadata == nil || conn.Metadata.InboundUser == nil {
			return 0, errors.New("invalid core connection identity")
		}
		seen[conn.ID] = true
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
