// Package traffic collects approximate VPN payload counters. It does not enforce quotas.
package traffic

import (
	"context"
	"time"
)

const PollInterval = 2 * time.Second
const MaxConnections = 16384
const MaxCheckpoints = 131072
const CheckpointRetention = 24 * time.Hour

// User is the immutable node user key, not a mutable display name or VPN credential.
// Upload is client-to-destination; download is destination-to-client, in bytes.
type Connection struct {
	ID        string
	User      string
	StartedAt time.Time
	Upload    int64
	Download  int64
}

type Snapshot struct {
	Generation  string
	Sequence    int64
	ObservedAt  time.Time
	Connections []Connection
}

type Source interface {
	Traffic(context.Context) ([]Connection, error)
}

// Store commits observations and checkpoints together. StartTraffic is called only
// for an owned, confirmed core instance; retrying the same generation is idempotent.
type Store interface {
	StartTraffic(context.Context, string) (int64, error)
	ApplyTraffic(context.Context, Snapshot) error
	TrafficTotals(context.Context) (Totals, error)
}

type UserTotal struct {
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	UploadBytes   int64  `json:"uploadBytes,string"`
	DownloadBytes int64  `json:"downloadBytes,string"`
}

type Totals struct {
	ObservedAt         *time.Time  `json:"observedAt"`
	Users              []UserTotal `json:"users"`
	ResetCount         int64       `json:"resetCount,string"`
	SkippedConnections int64       `json:"skippedConnections"`
}
