package postgres

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

var _ traffic.Store = (*Store)(nil)

// StartTraffic is idempotent for a proven, still-owned core generation. A caller
// must never use a new generation merely to recover from a SQL failure.
func (s *Store) StartTraffic(ctx context.Context, generation string) (int64, error) {
	if !keyPattern.MatchString(generation) {
		return 0, node.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var current string
	var sequence int64
	if err = tx.QueryRow(ctx, "SELECT generation,sequence FROM node_traffic_state WHERE singleton FOR UPDATE").Scan(&current, &sequence); err != nil {
		return 0, node.ErrUnavailable
	}
	if current == generation {
		return sequence, nil
	}
	if _, err = tx.Exec(ctx, "DELETE FROM node_traffic_checkpoints"); err != nil {
		return 0, node.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE node_traffic_state SET generation=$1,sequence=0,observed_at=NULL,pruned_before=NULL,skipped_connections=0 WHERE singleton`, generation); err != nil {
		return 0, node.ErrUnavailable
	}
	return 0, sanitized(tx.Commit(ctx))
}

type trafficKey struct{ id, user string }
type trafficCheckpoint struct {
	upload, download int64
	started          time.Time
}
type trafficIncrement struct{ upload, download int64 }

func validTrafficTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 }

// ApplyTraffic serializes complete samples. The durable sequence resolves both
// out-of-order delivery and ambiguous commit retries before inspecting counters.
func (s *Store) ApplyTraffic(ctx context.Context, sample traffic.Snapshot) error {
	if !keyPattern.MatchString(sample.Generation) || sample.Sequence <= 0 || !validTrafficTime(sample.ObservedAt) || len(sample.Connections) > traffic.MaxConnections {
		return node.ErrInvalid
	}
	ids, users := make([]string, 0, len(sample.Connections)), make([]string, 0, len(sample.Connections))
	seen := make(map[string]bool, len(sample.Connections))
	for _, c := range sample.Connections {
		if !keyPattern.MatchString(c.ID) || (c.User != "" && !namePattern.MatchString(c.User)) || !validTrafficTime(c.StartedAt) || c.StartedAt.After(sample.ObservedAt) || c.Upload < 0 || c.Download < 0 || seen[c.ID] {
			return node.ErrInvalid
		}
		seen[c.ID] = true
		ids = append(ids, c.ID)
		users = append(users, c.User)
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var generation string
	var sequence, resets int64
	var watermark *time.Time
	if err = tx.QueryRow(ctx, `SELECT generation,sequence,reset_count,pruned_before FROM node_traffic_state WHERE singleton FOR UPDATE`).Scan(&generation, &sequence, &resets, &watermark); err != nil {
		return node.ErrUnavailable
	}
	if generation != sample.Generation {
		return node.ErrConflict
	}
	if sample.Sequence <= sequence {
		return nil
	}
	// Resolve every credential generation, including revoked/deleted sessions,
	// to its stable accounting identity before checkpoint lookup and pruning.
	aliases, err := tx.Query(ctx, "SELECT runtime_name,user_name FROM node_user_credentials WHERE runtime_name=ANY($1)", users)
	if err != nil {
		return node.ErrUnavailable
	}
	attribution := map[string]string{}
	for aliases.Next() {
		var runtimeName, name string
		if aliases.Scan(&runtimeName, &name) != nil {
			aliases.Close()
			return node.ErrUnavailable
		}
		attribution[runtimeName] = name
	}
	aliases.Close()
	if aliases.Err() != nil {
		return node.ErrUnavailable
	}
	connections := append([]traffic.Connection(nil), sample.Connections...)
	for i := range connections {
		if name, ok := attribution[connections[i].User]; ok {
			connections[i].User = name
			users[i] = name
		} else {
			connections[i].User = ""
			users[i] = ""
		}
	}
	sample.Connections = connections
	// Prune only absent connections. The monotonic watermark makes a forgotten
	// old connection ineligible for first-observation accounting if it reappears.
	cutoff := sample.ObservedAt.Add(-traffic.CheckpointRetention).Truncate(time.Microsecond)
	if watermark != nil && watermark.After(cutoff) {
		cutoff = *watermark
	}
	pruned, err := tx.Exec(ctx, `DELETE FROM node_traffic_checkpoints c WHERE last_seen < $1 AND NOT EXISTS (SELECT 1 FROM unnest($2::text[],$3::text[]) AS p(id,usr) WHERE p.id=c.connection_id AND p.usr=c.user_name)`, cutoff, ids, users)
	if err != nil {
		return node.ErrUnavailable
	}
	if pruned.RowsAffected() > 0 {
		watermark = &cutoff
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM node_traffic_checkpoints").Scan(&count); err != nil || count > traffic.MaxCheckpoints {
		return node.ErrUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT c.connection_id,c.user_name,c.upload_bytes,c.download_bytes,c.started_at FROM node_traffic_checkpoints c JOIN unnest($1::text[],$2::text[]) AS p(id,usr) ON p.id=c.connection_id AND p.usr=c.user_name`, ids, users)
	if err != nil {
		return node.ErrUnavailable
	}
	checkpoints := make(map[trafficKey]trafficCheckpoint, len(ids))
	for rows.Next() {
		var key trafficKey
		var cp trafficCheckpoint
		if err = rows.Scan(&key.id, &key.user, &cp.upload, &cp.download, &cp.started); err != nil {
			rows.Close()
			return node.ErrUnavailable
		}
		checkpoints[key] = cp
	}
	rows.Close()
	if rows.Err() != nil {
		return node.ErrUnavailable
	}
	rows, err = tx.Query(ctx, "SELECT name FROM node_users WHERE name=ANY($1)", users)
	if err != nil {
		return node.ErrUnavailable
	}
	known := make(map[string]bool)
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			rows.Close()
			return node.ErrUnavailable
		}
		known[user] = true
	}
	rows.Close()
	if rows.Err() != nil {
		return node.ErrUnavailable
	}
	increments := make(map[string]trafficIncrement)
	acceptedIDs, acceptedUsers := []string{}, []string{}
	starts, uploads, downloads := []time.Time{}, []int64{}, []int64{}
	var skipped int64
	for _, c := range sample.Connections {
		key := trafficKey{c.ID, c.User}
		cp, exists := checkpoints[key]
		started := c.StartedAt.Truncate(time.Microsecond)
		if !known[c.User] || (!exists && ((watermark != nil && !started.After(*watermark)) || count >= traffic.MaxCheckpoints)) {
			skipped++
			continue
		}
		up, down := c.Upload, c.Download
		if exists {
			reset := false
			if up < cp.upload {
				up = 0
				reset = true
			} else {
				up -= cp.upload
			}
			if down < cp.download {
				down = 0
				reset = true
			} else {
				down -= cp.download
			}
			if !started.Equal(cp.started) {
				up = 0
				down = 0
				reset = true
			}
			if reset {
				if resets == math.MaxInt64 {
					return node.ErrUnavailable
				}
				resets++
			}
		} else {
			count++
		}
		inc := increments[c.User]
		if up > math.MaxInt64-inc.upload || down > math.MaxInt64-inc.download {
			return node.ErrUnavailable
		}
		inc.upload += up
		inc.download += down
		increments[c.User] = inc
		acceptedIDs = append(acceptedIDs, c.ID)
		acceptedUsers = append(acceptedUsers, c.User)
		starts = append(starts, started)
		uploads = append(uploads, c.Upload)
		downloads = append(downloads, c.Download)
	}
	totalUsers, totalUploads, totalDownloads := []string{}, []int64{}, []int64{}
	for user, inc := range increments {
		totalUsers = append(totalUsers, user)
		totalUploads = append(totalUploads, inc.upload)
		totalDownloads = append(totalDownloads, inc.download)
	}
	// PostgreSQL bigint arithmetic rejects overflow, rolling back the entire
	// observation, including pruning, counters, checkpoints and sequence.
	if _, err = tx.Exec(ctx, `INSERT INTO node_traffic_totals(user_name,upload_bytes,download_bytes) SELECT * FROM unnest($1::text[],$2::bigint[],$3::bigint[]) ON CONFLICT(user_name) DO UPDATE SET upload_bytes=node_traffic_totals.upload_bytes+excluded.upload_bytes,download_bytes=node_traffic_totals.download_bytes+excluded.download_bytes`, totalUsers, totalUploads, totalDownloads); err != nil {
		return node.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO node_traffic_checkpoints(connection_id,user_name,started_at,upload_bytes,download_bytes,last_seen) SELECT id,usr,started,up,down,$6 FROM unnest($1::text[],$2::text[],$3::timestamptz[],$4::bigint[],$5::bigint[]) AS p(id,usr,started,up,down) ON CONFLICT(connection_id,user_name) DO UPDATE SET started_at=excluded.started_at,upload_bytes=excluded.upload_bytes,download_bytes=excluded.download_bytes,last_seen=GREATEST(node_traffic_checkpoints.last_seen,excluded.last_seen)`, acceptedIDs, acceptedUsers, starts, uploads, downloads, sample.ObservedAt); err != nil {
		return node.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `UPDATE node_traffic_state SET accounting_sequence=accounting_sequence+1,sequence=$1,observed_at=$2,pruned_before=$3,reset_count=$4,skipped_connections=$5 WHERE singleton`, sample.Sequence, sample.ObservedAt, watermark, resets, skipped); err != nil {
		return node.ErrUnavailable
	}
	return sanitized(tx.Commit(ctx))
}

func (s *Store) TrafficTotals(ctx context.Context) (traffic.Totals, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return traffic.Totals{}, node.ErrUnavailable
	}
	defer tx.Rollback(ctx)
	result := traffic.Totals{Users: []traffic.UserTotal{}}
	if err = tx.QueryRow(ctx, `SELECT accounting_epoch::text,accounting_sequence,observed_at,reset_count,skipped_connections FROM node_traffic_state WHERE singleton`).Scan(&result.AccountingEpoch, &result.AccountingSequence, &result.ObservedAt, &result.ResetCount, &result.SkippedConnections); err != nil {
		return traffic.Totals{}, node.ErrUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT u.name,u.enabled,COALESCE(t.upload_bytes,0),COALESCE(t.download_bytes,0) FROM node_users u LEFT JOIN node_traffic_totals t ON t.user_name=u.name WHERE NOT u.deleted ORDER BY u.name LIMIT $1`, maxUsers+1)
	if err != nil {
		return traffic.Totals{}, node.ErrUnavailable
	}
	for rows.Next() {
		var user traffic.UserTotal
		if err = rows.Scan(&user.Name, &user.Enabled, &user.UploadBytes, &user.DownloadBytes); err != nil {
			rows.Close()
			return traffic.Totals{}, node.ErrUnavailable
		}
		result.Users = append(result.Users, user)
	}
	rows.Close()
	if rows.Err() != nil || len(result.Users) > maxUsers {
		return traffic.Totals{}, node.ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return traffic.Totals{}, node.ErrUnavailable
	}
	return result, nil
}
