package postgres

import (
	"context"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
)

func TestCommittedTrafficObservationResolvesAliasesAndExcludesReplays(t *testing.T) {
	s, _ := trafficStore(t)
	ctx := context.Background()
	if _, err := s.Change(ctx, node.UserChange{Kind: "rotate", Key: "rotate-for-live", Name: "alice", UUID: "b47ac10b-58cc-4372-a567-0e02b2c3d479", Enabled: true, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alias := snapshot.Active()[0].Name
	if alias == "alice" {
		t.Fatal("rotation did not create runtime alias")
	}
	value := sample(1, 100, 200)
	value.Connections = append(value.Connections, traffic.Connection{ID: "rotated", User: alias, StartedAt: trafficTime, Upload: 300}, traffic.Connection{ID: "unknown", User: "unknown", StartedAt: trafficTime, Upload: 999})
	result, err := s.ApplyTraffic(ctx, value)
	if err != nil || result == nil {
		t.Fatalf("committed result missing: %v", err)
	}
	if result.Connections[0].User != "alice" || result.Connections[1].User != "alice" || result.Connections[2].User != "" {
		t.Fatal("runtime identities not stably attributed")
	}
	if value.Connections[1].User != alias || value.Connections[2].User != "unknown" {
		t.Fatal("caller observation mutated")
	}
	totals := assertTraffic(t, s, 400, 200, 0, 1)
	if result.AccountingEpoch != totals.AccountingEpoch || result.AccountingSequence != totals.AccountingSequence || !result.ObservedAt.Equal(*totals.ObservedAt) {
		t.Fatal("observation not bound to committed totals")
	}
	if totals.Users[0].Live != nil {
		t.Fatal("ephemeral measurements persisted")
	}
	result, err = s.ApplyTraffic(ctx, value)
	if err != nil || result != nil {
		t.Fatal("replay returned a new live observation")
	}
	value.Sequence = 2
	if _, err = s.pool.Exec(ctx, `CREATE FUNCTION fail_live_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test failure'; END $$; CREATE TRIGGER fail_live_write BEFORE UPDATE ON node_traffic_totals FOR EACH ROW EXECUTE FUNCTION fail_live_write()`); err != nil {
		t.Fatal(err)
	}
	result, err = s.ApplyTraffic(ctx, value)
	if err == nil || result != nil {
		t.Fatal("failed transaction returned live observation")
	}
}
