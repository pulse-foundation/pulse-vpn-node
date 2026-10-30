package postgres

import (
	"context"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

// Ping observes dependency reachability without reading users or coupling resource
// collection to SQL. The application supplies a short probe deadline.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return node.ErrUnavailable
	}
	return nil
}
