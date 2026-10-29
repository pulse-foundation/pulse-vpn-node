package node

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const VisionFlow = "xtls-rprx-vision"

type VLESS struct {
	Listen  string  `json:"listen"`
	Port    int     `json:"port"`
	Users   []User  `json:"users"`
	Reality Reality `json:"reality"`
}

type User struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}
type Reality struct {
	Destination string   `json:"destination"`
	PrivateKey  string   `json:"privateKey"`
	ShortIDs    []string `json:"shortIds"`
	ServerNames []string `json:"serverNames"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var labelPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func (v VLESS) Validate() error {
	if net.ParseIP(v.Listen) == nil || v.Port < 1 || v.Port > 65535 {
		return errors.New("VLESS requires an IP listen address and a valid port")
	}
	if len(v.Users) == 0 || len(v.Users) > 1024 {
		return errors.New("VLESS requires 1 to 1024 users")
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for _, u := range v.Users {
		id := strings.ToLower(u.UUID)
		if !namePattern.MatchString(u.Name) || !uuidPattern.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return errors.New("invalid VLESS user name or UUID")
		}
		if names[u.Name] || ids[id] {
			return errors.New("duplicate VLESS user name or UUID")
		}
		names[u.Name] = true
		ids[id] = true
	}
	host, port, err := net.SplitHostPort(v.Reality.Destination)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || n < 1 || n > 65535 || (net.ParseIP(host) == nil && !dnsName(host)) {
		return errors.New("invalid REALITY destination host or port")
	}
	key, err := base64.RawURLEncoding.DecodeString(v.Reality.PrivateKey)
	if err != nil || len(key) != 32 {
		return errors.New("invalid REALITY private key")
	}
	if len(v.Reality.ShortIDs) == 0 || len(v.Reality.ShortIDs) > 16 {
		return errors.New("REALITY requires 1 to 16 short IDs")
	}
	for _, id := range v.Reality.ShortIDs {
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) == 0 || len(decoded) > 8 {
			return errors.New("invalid REALITY short ID")
		}
	}
	if len(v.Reality.ServerNames) == 0 || len(v.Reality.ServerNames) > 16 {
		return errors.New("REALITY requires 1 to 16 server names")
	}
	for _, name := range v.Reality.ServerNames {
		if !dnsName(name) || net.ParseIP(name) != nil {
			return errors.New("invalid REALITY server name")
		}
	}
	return nil
}

func dnsName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !labelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func (v VLESS) Address() string { return net.JoinHostPort(v.Listen, strconv.Itoa(v.Port)) }

func (v VLESS) ProbeAddress() string {
	host := v.Listen
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return net.JoinHostPort(host, strconv.Itoa(v.Port))
}
