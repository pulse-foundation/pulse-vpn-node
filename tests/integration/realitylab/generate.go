package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func generate() error {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	badKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	id := newUUID()
	secondID := newUUID()
	password := randomHex(32)
	for name, value := range map[string]string{"second-uuid": secondID, "postgres-password": password, "database-url": "postgres://pulse_test:" + password + "@database:5432/pulse_test?sslmode=disable"} {
		if err := writeFile(name, []byte(value)); err != nil {
			return err
		}
	}
	sid := randomHex(8)
	vpn := node.VLESS{Listen: "0.0.0.0", Port: 8443, Users: []node.User{{Name: "local-test", UUID: id}}, Reality: node.Reality{Destination: "target:8443", PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortIDs: []string{sid}, ServerNames: []string{"target.test"}}}
	if err := vpn.Validate(); err != nil {
		return err
	}
	if err := writeJSON("server.json", vpn); err != nil {
		return err
	}
	if err := writeFile("api-token", []byte(randomHex(32))); err != nil {
		return err
	}
	pub := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	for _, tc := range []struct{ file, uuid, key string }{
		{"client.json", id, pub}, {"second.json", secondID, pub}, {"bad-uuid.json", newUUID(), pub},
		{"bad-key.json", id, base64.RawURLEncoding.EncodeToString(badKey.PublicKey().Bytes())},
	} {
		cfg := map[string]any{"mixed-port": 7890, "allow-lan": true, "bind-address": "0.0.0.0", "mode": "rule", "log-level": "silent", "ipv6": false, "find-process-mode": "off", "geo-auto-update": false,
			"dns": map[string]any{"enable": false}, "profile": map[string]any{"store-selected": false, "store-fake-ip": false},
			"proxies": []any{map[string]any{"name": "vpn", "type": "vless", "server": "node", "port": 8443, "uuid": tc.uuid, "flow": "xtls-rprx-vision", "network": "tcp", "tls": true, "udp": true, "packet-encoding": "xudp", "servername": "target.test", "client-fingerprint": "chrome", "skip-cert-verify": false, "reality-opts": map[string]any{"public-key": tc.key, "short-id": sid, "support-x25519mlkem768": false}}}, "rules": []string{"MATCH,vpn"}}
		if err := writeJSON(tc.file, cfg); err != nil {
			return err
		}
	}
	if err := generateCertificate(); err != nil {
		return err
	}
	if err := os.Chmod(fixtureDir, 0700); err != nil {
		return err
	}
	if err := os.Chown(fixtureDir, 10001, 10001); err != nil {
		return err
	}
	fmt.Println("Generated ephemeral local fixtures; credentials were not printed.")
	return nil
}

func randomHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func writeJSON(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(name, b)
}
func writeFile(name string, b []byte) error {
	path := filepath.Join(fixtureDir, name)
	if err := os.WriteFile(path, b, 0600); err != nil {
		return err
	}
	return os.Chown(path, 10001, 10001)
}

func generateCertificate() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Local REALITY target"}, DNSNames: []string{"target.test", "target"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFile("target.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return err
	}
	return writeFile("target.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
}
