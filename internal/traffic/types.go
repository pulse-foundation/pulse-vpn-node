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

// User is a source runtime identity, resolved by Store to the immutable node user
// key before returning a CommittedObservation. It is never a display name.
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
	ApplyTraffic(context.Context, Snapshot) (*CommittedObservation, error)
	TrafficTotals(context.Context) (Totals, error)
}

// CommittedObservation is returned only for a newly committed sample, never for
// a replay or an uncertain commit. Connections contain resolved stable user keys;
// unknown identities are empty. UnratedUsers had observations excluded from totals.
// Users contains the visible stable identities present in that commit.
type CommittedObservation struct {
	Snapshot
	AccountingEpoch    string
	AccountingSequence int64
	UnratedUsers       []string
	Users              []string
}

type Live struct {
	ObservedAt             time.Time `json:"observedAt"`
	UploadBytesPerSecond   *float64  `json:"uploadBytesPerSecond"`
	DownloadBytesPerSecond *float64  `json:"downloadBytesPerSecond"`
	ActiveConnections      int       `json:"activeConnections"`
}

type UserTotal struct {
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	UploadBytes   int64  `json:"uploadBytes,string"`
	DownloadBytes int64  `json:"downloadBytes,string"`
	// LastActivityAt is the last committed observation of a positive byte delta,
	// not a packet timestamp or proof that the client is currently connected.
	LastActivityAt *time.Time `json:"lastActivityAt"`
	Live           *Live      `json:"live"`
}

type Totals struct {
	AccountingEpoch    string      `json:"accountingEpoch"`
	AccountingSequence int64       `json:"accountingSequence,string"`
	ObservedAt         *time.Time  `json:"observedAt"`
	Users              []UserTotal `json:"users"`
	ResetCount         int64       `json:"resetCount,string"`
	SkippedConnections int64       `json:"skippedConnections"`
}
