package node

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
)

// Connection is the public, credential-free description of the startup listener.
// User reconciliation never changes its transport settings.
type Connection struct {
	Protocol   string              `json:"protocol"`
	Transport  ConnectionTransport `json:"transport"`
	ListenPort int                 `json:"listenPort"`
	Reality    ConnectionReality   `json:"reality"`
}
type ConnectionTransport struct {
	Type string `json:"type"`
	Flow string `json:"flow"`
}
type ConnectionReality struct {
	PublicKey   string   `json:"publicKey"`
	ShortIDs    []string `json:"shortIds"`
	ServerNames []string `json:"serverNames"`
}

// DescribeConnection validates actual startup settings and copies only public fields.
func DescribeConnection(v *VLESS) (*Connection, error) {
	if v == nil {
		return nil, nil
	}
	if err := v.Validate(); err != nil {
		return nil, errors.New("invalid connection settings")
	}
	raw, err := base64.RawURLEncoding.DecodeString(v.Reality.PrivateKey)
	if err != nil {
		return nil, errors.New("invalid connection key")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil, errors.New("invalid connection key")
	}
	return &Connection{
		Protocol: "vless", Transport: ConnectionTransport{Type: "raw", Flow: VisionFlow}, ListenPort: v.Port,
		Reality: ConnectionReality{PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortIDs: append([]string(nil), v.Reality.ShortIDs...), ServerNames: append([]string(nil), v.Reality.ServerNames...)},
	}, nil
}
