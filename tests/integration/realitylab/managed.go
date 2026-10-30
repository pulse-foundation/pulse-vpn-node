package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

type connectionView struct {
	SchemaVersion int              `json:"schemaVersion"`
	Connection    *node.Connection `json:"connection"`
}

func fixtureClient(name string) (map[string]any, map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name+".json"))
	if err != nil {
		return nil, nil, errors.New("client fixture unavailable")
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, nil, errors.New("client fixture invalid")
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok || len(proxies) != 1 {
		return nil, nil, errors.New("client fixture proxy missing")
	}
	proxy, ok := proxies[0].(map[string]any)
	if !ok {
		return nil, nil, errors.New("client fixture proxy invalid")
	}
	return cfg, proxy, nil
}
func fixtureUUID(name string) (string, error) {
	_, proxy, err := fixtureClient(name)
	if err != nil {
		return "", err
	}
	uuid, ok := proxy["uuid"].(string)
	if !ok {
		return "", errors.New("client fixture identity missing")
	}
	return uuid, nil
}
func managedConfigure() error {
	var status struct {
		Status     string `json:"status"`
		Configured bool   `json:"vpnConfigured"`
	}
	if err := userAPI("GET", "status", nil, 200, &status); err != nil {
		return err
	}
	if status.Status != "ready" || status.Configured {
		return errors.New("managed node did not start healthy idle")
	}
	var view connectionView
	if err := userAPI("GET", "connection", nil, 200, &view); err != nil {
		return err
	}
	if view.SchemaVersion != 1 || view.Connection != nil {
		return errors.New("idle descriptor was not null")
	}
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	if len(s.Users) != 0 {
		return errors.New("idle node imported unintended users")
	}
	if err = awaitRevision(s.Revision); err != nil {
		return err
	}
	body := map[string]any{"expectedRevision": s.Revision, "transport": map[string]string{"type": "raw"}, "listenPort": 8443, "reality": map[string]any{"destination": "target:8443", "serverNames": []string{"target.test"}}}
	// Reserved ports must be rejected before changing durable intent/revision.
	for _, port := range []int{8080, 19090} {
		body["listenPort"] = port
		if err = userAPI("PUT", "connection", body, 400, nil); err != nil {
			return err
		}
		after, err := userSnapshot()
		if err != nil || after.Revision != s.Revision {
			return errors.New("rejected port changed desired revision")
		}
	}
	body["listenPort"] = 8443
	var op userOperation
	if err = userAPI("PUT", "connection", body, 202, &op); err != nil {
		return err
	}
	if err = awaitRevision(op.Revision); err != nil {
		return err
	}
	if err = userAPI("GET", "connection", nil, 200, &view); err != nil {
		return err
	}
	if view.Connection == nil || view.Connection.ListenPort != 8443 {
		return errors.New("applied connection descriptor missing")
	}
	body["expectedRevision"] = op.Revision
	body["listenPort"] = 9443
	if err = userAPI("PUT", "connection", body, 409, nil); err != nil {
		return err
	}
	if err = writeJSON("managed-connection.json", view); err != nil {
		return err
	}
	for _, name := range []string{"client", "second", "bad-uuid", "replacement"} {
		cfg, proxy, err := fixtureClient(name)
		if err != nil {
			return err
		}
		proxy["reality-opts"] = map[string]any{"public-key": view.Connection.Reality.PublicKey, "short-id": view.Connection.Reality.ShortIDs[0], "support-x25519mlkem768": false}
		if err = writeJSON(name+".json", cfg); err != nil {
			return err
		}
	}
	for _, u := range []struct{ name, client string }{{"local-test", "client"}, {"second-test", "second"}} {
		uuid, err := fixtureUUID(u.client)
		if err != nil {
			return err
		}
		if err = mutateUser(u.name, true, uuid); err != nil {
			return err
		}
	}
	fmt.Println("PASS: idle readiness/null descriptor, live singleton creation, conflicting second create and two users")
	return nil
}
func lifecycleMutation(method, path, uuid string) error {
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	body := map[string]any{"expectedRevision": s.Revision}
	if uuid != "" {
		body["uuid"] = uuid
	}
	var op userOperation
	if err = userAPI(method, path, body, 202, &op); err != nil {
		return err
	}
	return awaitRevision(op.Revision)
}
func waitAccounted(name string, minimum trafficBytes) (trafficBytes, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s, err := trafficRead()
		if err != nil {
			return trafficBytes{}, err
		}
		v := trafficTotals(s)[name]
		if v.Upload > minimum.Upload && v.Download > minimum.Download {
			return v, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return trafficBytes{}, errors.New("credential generation traffic was not attributed")
}
func managedLifecycle() error {
	if err := probe(); err != nil {
		return err
	}
	original, err := openSessions("client:7890")
	if err != nil {
		return err
	}
	defer original.close()
	second, err := openSessions("second:7890")
	if err != nil {
		return err
	}
	defer second.close()
	before, err := waitAccounted("second-test", trafficBytes{})
	if err != nil {
		return err
	}
	rotated, err := fixtureUUID("bad-uuid")
	if err != nil {
		return err
	}
	if err = lifecycleMutation("POST", "users/second-test/rotate", rotated); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	if err = second.revoked(); err != nil {
		return err
	}
	if err = expectRejected("second:7890"); err != nil {
		return err
	}
	replacement, err := openSessions("bad-uuid:7890")
	if err != nil {
		return err
	}
	defer replacement.close()
	after, err := waitAccounted("second-test", before)
	if err != nil {
		return err
	}
	if err = lifecycleMutation("DELETE", "users/second-test", ""); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	if err = replacement.revoked(); err != nil {
		return err
	}
	if err = expectRejected("bad-uuid:7890"); err != nil {
		return err
	}
	s, err := userSnapshot()
	if err != nil {
		return err
	}
	if len(s.Users) != 1 || s.Users[0].Name != "local-test" {
		return errors.New("deleted user still visible")
	}
	uuid, err := fixtureUUID("replacement")
	if err != nil {
		return err
	}
	if err = mutateUser("second-test", true, uuid); err != nil {
		return err
	}
	recreated, err := openSessions("replacement:7890")
	if err != nil {
		return err
	}
	defer recreated.close()
	if _, err = waitAccounted("second-test", after); err != nil {
		return err
	}
	if err = original.echo(); err != nil {
		return err
	}
	if err = expectRejected("second:7890"); err != nil {
		return err
	}
	if err = expectRejected("bad-uuid:7890"); err != nil {
		return err
	}
	fmt.Println("PASS: rotate/delete close old TCP+UDP sessions; fresh credentials work; unrelated sessions survive; totals persist across credential generations")
	return nil
}
func managedPersist() error {
	snapshot, err := userSnapshot()
	if err != nil {
		return err
	}
	if err = awaitRevision(snapshot.Revision); err != nil {
		return err
	}
	var view, saved connectionView
	if err := userAPI("GET", "connection", nil, 200, &view); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "managed-connection.json"))
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		return errors.New("saved public descriptor unavailable")
	}
	if !reflect.DeepEqual(view, saved) {
		return errors.New("restart changed connection key or descriptor")
	}
	if err = probe(); err != nil {
		return err
	}
	if err = expectRejected("second:7890"); err != nil {
		return err
	}
	if err = expectRejected("bad-uuid:7890"); err != nil {
		return err
	}
	s, err := openSessions("replacement:7890")
	if err != nil {
		return err
	}
	defer s.close()
	if _, err = waitAccounted("second-test", trafficBytes{}); err != nil {
		return err
	}
	fmt.Println("PASS: SQL connection/keys, recreated identity, totals and all retired credentials survive idle-file restart")
	return nil
}
