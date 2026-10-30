package node

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// Connection is the public, credential-free description of a verified listener.
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

// DescribeConnection validates applied settings and copies only public fields.
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

// ConnectionChange creates the singleton listener; transport conversion is not implicit.
type ConnectionChange struct {
	Key              string `json:"-"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Transport        struct {
		Type string `json:"type"`
	} `json:"transport"`
	ListenPort int `json:"listenPort"`
	Reality    struct {
		Destination string   `json:"destination"`
		ServerNames []string `json:"serverNames"`
	} `json:"reality"`
}

func (c ConnectionChange) NewSettings() (*VLESS, error) {
	if c.Transport.Type != "raw" {
		return nil, ErrInvalid
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, ErrUnavailable
	}
	var short [8]byte
	if _, err = rand.Read(short[:]); err != nil {
		return nil, ErrUnavailable
	}
	v := &VLESS{Listen: "0.0.0.0", Port: c.ListenPort, Users: []User{}, Reality: Reality{Destination: c.Reality.Destination, ServerNames: append([]string(nil), c.Reality.ServerNames...), PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortIDs: []string{hex.EncodeToString(short[:])}}}
	if v.Validate() != nil {
		return nil, ErrInvalid
	}
	return v, nil
}
