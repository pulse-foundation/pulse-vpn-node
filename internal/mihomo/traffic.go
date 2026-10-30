package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

// Traffic reads cumulative payload counters for live TCP and UDP trackers.
// Upload is client-to-destination; download is destination-to-client. Only the
// attribution key and counters leave this adapter, never routing metadata.
func (c *Controller) Traffic(ctx context.Context) ([]traffic.Connection, error) {
	connections, err := c.connections(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]traffic.Connection, 0, len(connections))
	for _, conn := range connections {
		var upload, download *int64
		var start *time.Time
		if json.Unmarshal(conn.Upload, &upload) != nil || upload == nil || *upload < 0 ||
			json.Unmarshal(conn.Download, &download) != nil || download == nil || *download < 0 ||
			json.Unmarshal(conn.Start, &start) != nil || start == nil || start.IsZero() {
			return nil, errors.New("invalid core traffic counters")
		}
		result = append(result, traffic.Connection{
			ID: conn.ID, User: *conn.Metadata.InboundUser, StartedAt: *start,
			Upload: *upload, Download: *download,
		})
	}
	return result, nil
}

// Traffic does not acquire the mutation lock: sampling is read-only and must not
// wait behind a slow user reload. Both request and child lifetime cancel the read.
func (p *Process) Traffic(ctx context.Context) ([]traffic.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	stop := context.AfterFunc(p.lifetime, cancel)
	defer func() { stop(); cancel() }()
	if ctx.Err() != nil || p.lifetime.Err() != nil {
		return nil, errors.New("core traffic read cancelled")
	}
	connections, err := p.controller.Traffic(ctx)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || p.lifetime.Err() != nil {
		return nil, errors.New("core traffic read cancelled")
	}
	return connections, nil
}

var _ traffic.Source = (*Process)(nil)
