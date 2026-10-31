package telemetry

import (
	"context"
	"encoding/json"
	"github.com/pulse-foundation/pulse-vpn-node/internal/traffic"
	"net/http"
	"testing"
	"time"
)

func TestHeartbeatCarriesDurableTrafficAndOmitsUnavailable(t *testing.T) {
	const session = "8a80bd53-dc79-46a8-ad11-a8fa066148e2"
	var snapshots []json.RawMessage
	exporter, _ := exporterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RunID    string          `json:"runId"`
			Sequence string          `json:"heartbeatSequence"`
			Snapshot json.RawMessage `json:"trafficSnapshot"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		snapshots = append(snapshots, body.Snapshot)
		_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "sessionId": session, "runId": body.RunID, "committedSequence": "0", "heartbeatSequence": body.Sequence, "leaseExpiresAt": time.Now().Add(time.Minute)})
	})
	exporter.session = session
	observed := time.Now().UTC()
	exporter.trafficSnapshot = func() *traffic.View {
		return &traffic.View{Totals: traffic.Totals{AccountingEpoch: "11111111-1111-4111-8111-111111111111", AccountingSequence: 9007199254740993, ObservedAt: &observed, Users: []traffic.UserTotal{{Name: "stable-user", UploadBytes: 9007199254740993, DownloadBytes: 42}}}, Approximate: true, PollIntervalSeconds: 2, State: "collecting"}
	}
	if err := exporter.sendHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	exporter.trafficSnapshot = func() *traffic.View { return nil }
	if err := exporter.sendHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Sequence string `json:"accountingSequence"`
		Users    []struct {
			Upload string `json:"uploadBytes"`
		} `json:"users"`
	}
	if err := json.Unmarshal(snapshots[0], &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Sequence != "9007199254740993" || wire.Users[0].Upload != "9007199254740993" || len(snapshots[1]) != 0 {
		t.Fatal("traffic precision or independent heartbeat lost")
	}
}
