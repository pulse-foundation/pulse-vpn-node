package postgres

import (
	"context"
	"testing"
)

func TestTrafficAccountingIdentitySurvivesCoreRestart(t *testing.T) {
	s, _ := trafficStore(t)
	ctx := context.Background()
	before, err := s.TrafficTotals(ctx)
	if err != nil || before.AccountingEpoch == "" || before.AccountingSequence != 0 {
		t.Fatalf("missing initial identity: %+v %v", before, err)
	}
	applySample(t, s, sample(1, 9007199254740993, 42))
	applySample(t, s, sample(1, 9007199254740993, 42))
	first, err := s.TrafficTotals(ctx)
	if err != nil || first.AccountingSequence != 1 || first.AccountingEpoch != before.AccountingEpoch {
		t.Fatalf("replay changed accounting: %+v %v", first, err)
	}
	if _, err := s.StartTraffic(ctx, "core-2"); err != nil {
		t.Fatal(err)
	}
	second, err := s.TrafficTotals(ctx)
	if err != nil || second.AccountingEpoch != first.AccountingEpoch || second.AccountingSequence != first.AccountingSequence || second.Users[0].UploadBytes != 9007199254740993 {
		t.Fatalf("core restart lost accounting: %+v %v", second, err)
	}
}
