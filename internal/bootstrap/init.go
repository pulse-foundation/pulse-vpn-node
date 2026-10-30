// Package bootstrap prepares the standard Compose configuration without starting services.
package bootstrap

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/pulse-foundation/pulse-vpn-node/internal/config"
	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

type Options struct {
	Dir         string
	Destination string
	ServerNames []string
	Listen      string
	Port        int
}

type Result struct {
	Created   []string `json:"created"`
	Preserved []string `json:"preserved"`
}

var filenames = []string{"api-token", "database-password", "database-url", "vless.json"}

// Init preserves valid existing files byte-for-byte. Validation precedes publication;
// after an interrupted publication, retrying completes the missing files without rotation.
func Init(o Options) (Result, error) {
	result := Result{Created: []string{}, Preserved: []string{}}
	root, lock, err := openDirectory(o.Dir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	defer lock.Close() // Closing releases the kernel lock, including on process exit.
	existing := make(map[string][]byte)
	for _, name := range filenames {
		raw, err := readExisting(root, name)
		if err != nil {
			return result, err
		}
		existing[name] = raw
	}
	pending, err := prepare(o, existing)
	if err != nil {
		return result, err
	}
	for _, name := range filenames {
		if existing[name] != nil {
			result.Preserved = append(result.Preserved, name)
			continue
		}
		if err := publish(root, name, pending[name]); err != nil {
			return result, err
		}
		result.Created = append(result.Created, name)
	}
	return result, nil
}

func prepare(o Options, existing map[string][]byte) (map[string][]byte, error) {
	pending := make(map[string][]byte)
	if raw := existing["api-token"]; raw != nil {
		if _, err := config.ParseToken(raw); err != nil {
			return nil, errors.New("invalid existing api-token")
		}
	}
	password, err := databasePassword(existing)
	if err != nil {
		return nil, err
	}
	var vpn *node.VLESS
	if raw := existing["vless.json"]; raw != nil {
		vpn, err = config.ParseVLESS(raw)
		if err != nil {
			return nil, errors.New("invalid existing vless.json")
		}
		if (o.Listen != "" && o.Listen != vpn.Listen) || (o.Port != 0 && o.Port != vpn.Port) ||
			(o.Destination != "" && o.Destination != vpn.Reality.Destination) ||
			(o.ServerNames != nil && !slices.Equal(o.ServerNames, vpn.Reality.ServerNames)) {
			return nil, errors.New("requested transport settings conflict with existing vless.json; init does not update settings")
		}
	} else {
		listen, port := o.Listen, o.Port
		if listen == "" {
			listen = "0.0.0.0"
		}
		if port == 0 {
			port = 8443
		}
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, errors.New("cannot generate REALITY key")
		}
		vpn = &node.VLESS{Listen: listen, Port: port, Users: []node.User{}, Reality: node.Reality{
			Destination: o.Destination, ServerNames: o.ServerNames,
			PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortIDs: []string{randomHex(8)},
		}}
		if err := vpn.Validate(); err != nil {
			return nil, err
		}
		raw, err := json.MarshalIndent(vpn, "", "  ")
		if err != nil {
			return nil, errors.New("cannot encode VLESS settings")
		}
		pending["vless.json"] = append(raw, '\n')
	}
	if existing["api-token"] == nil {
		pending["api-token"] = []byte(randomHex(32) + "\n")
	}
	if password == "" {
		password = randomHex(32)
	}
	pending["database-password"] = []byte(password + "\n")
	u := url.URL{Scheme: "postgres", User: url.UserPassword("pulse_node", password), Host: "database:5432", Path: "/pulse_node", RawQuery: "sslmode=disable"}
	pending["database-url"] = []byte(u.String() + "\n")
	return pending, nil
}

func randomHex(n int) string {
	raw := make([]byte, n)
	// crypto/rand.Read fills the buffer or terminates the process on entropy failure.
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func databasePassword(existing map[string][]byte) (string, error) {
	password := ""
	if raw := existing["database-password"]; raw != nil {
		password = strings.TrimRight(string(raw), "\n")
		if !validPassword(password) {
			return "", errors.New("invalid existing database-password")
		}
	}
	if raw := existing["database-url"]; raw != nil {
		u, err := url.Parse(strings.TrimSpace(string(raw)))
		if err != nil || u == nil || u.User == nil {
			return "", errors.New("invalid existing database-url")
		}
		value, hasPassword := u.User.Password()
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
			u.User.Username() != "pulse_node" || u.Host != "database:5432" || u.Path != "/pulse_node" ||
			u.Fragment != "" || u.Opaque != "" || len(q) != 1 || len(q["sslmode"]) != 1 || q.Get("sslmode") != "disable" ||
			!hasPassword || !validPassword(value) {
			return "", errors.New("database-url must match the standard Compose database and contain a valid password")
		}
		if password != "" && password != value {
			return "", errors.New("database-password and database-url do not match")
		}
		password = value
	}
	return password, nil
}

func validPassword(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
