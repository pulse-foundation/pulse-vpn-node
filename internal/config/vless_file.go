package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func loadVLESS(path string) (*node.VLESS, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open VLESS settings file")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("cannot read VLESS settings within 64 KiB limit")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var vpn *node.VLESS
	if err := dec.Decode(&vpn); err != nil || vpn == nil {
		return nil, errors.New("invalid VLESS settings JSON")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("unexpected data after VLESS settings")
	}
	if err := vpn.Validate(); err != nil {
		return nil, err
	}
	return vpn, nil
}
