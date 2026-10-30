package bootstrap

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

var managed = []string{"api-token", "database-password", "database-url", "vless.json"}

func fresh(t *testing.T) Options {
	t.Helper()
	return Options{Dir: filepath.Join(t.TempDir(), "config"), Destination: "target.test:443", ServerNames: []string{"target.test"}}
}
func readFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, name := range managed {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal("cannot read generated file")
		}
		out[name] = b
	}
	return out
}
func assertUnchanged(t *testing.T, dir string, before map[string][]byte) {
	t.Helper()
	for name, b := range before {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(after, b) {
			t.Fatalf("%s changed", name)
		}
	}
}
func TestInitFreshAndRepeat(t *testing.T) {
	o := fresh(t)
	r, err := Init(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Created) != 4 || len(r.Preserved) != 0 {
		t.Fatal("unexpected creation summary")
	}
	before := readFiles(t, o.Dir)
	info, _ := os.Stat(o.Dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("directory must be private")
	}
	for _, name := range managed {
		info, _ := os.Stat(filepath.Join(o.Dir, name))
		if info.Mode().Perm() != 0644 {
			t.Fatal("secret must be readable by Compose container UID")
		}
	}
	if len(strings.TrimSpace(string(before["api-token"]))) != 64 {
		t.Fatal("invalid generated token")
	}
	var v node.VLESS
	if json.Unmarshal(before["vless.json"], &v) != nil || v.Validate() != nil {
		t.Fatal("invalid VLESS settings")
	}
	if v.Listen != "0.0.0.0" || v.Port != 8443 || len(v.Users) != 0 {
		t.Fatal("unexpected listener or initial users")
	}
	key, err := base64.RawURLEncoding.DecodeString(v.Reality.PrivateKey)
	if err != nil {
		t.Fatal("invalid key encoding")
	}
	if _, err := ecdh.X25519().NewPrivateKey(key); err != nil {
		t.Fatal("invalid X25519 key")
	}
	u, err := url.Parse(strings.TrimSpace(string(before["database-url"])))
	if err != nil {
		t.Fatal("invalid database URL")
	}
	password, _ := u.User.Password()
	if password != strings.TrimSpace(string(before["database-password"])) || u.Host != "database:5432" || u.User.Username() != "pulse_node" || u.Path != "/pulse_node" {
		t.Fatal("database credentials mismatch")
	}
	// Preserve even noncanonical JSON formatting and restrictive operator-selected file modes.
	b := append([]byte("\n"), before["vless.json"]...)
	if err := os.WriteFile(filepath.Join(o.Dir, "vless.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	before["vless.json"] = b
	if err := os.Chmod(filepath.Join(o.Dir, "api-token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, repeat := range []Options{o, {Dir: o.Dir}} {
		r, err = Init(repeat)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Created) != 0 || len(r.Preserved) != 4 {
			t.Fatal("unexpected preservation summary")
		}
		assertUnchanged(t, o.Dir, before)
	}
	info, _ = os.Stat(filepath.Join(o.Dir, "api-token"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("existing mode changed")
	}
}
func TestInitRecoverDatabasePair(t *testing.T) {
	for _, missing := range []string{"database-password", "database-url"} {
		t.Run(missing, func(t *testing.T) {
			o := fresh(t)
			if _, err := Init(o); err != nil {
				t.Fatal(err)
			}
			before := readFiles(t, o.Dir)
			if err := os.Remove(filepath.Join(o.Dir, missing)); err != nil {
				t.Fatal(err)
			}
			r, err := Init(Options{Dir: o.Dir})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Created) != 1 || r.Created[0] != missing {
				t.Fatal("wrong recovery")
			}
			assertUnchanged(t, o.Dir, before)
		})
	}
	t.Run("escaped password", func(t *testing.T) {
		o := fresh(t)
		if err := os.Mkdir(o.Dir, 0700); err != nil {
			t.Fatal(err)
		}
		password := "synthetic:@/#?%+password"
		if err := os.WriteFile(filepath.Join(o.Dir, "database-password"), []byte(password+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(o); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(o.Dir, "database-password")); err != nil {
			t.Fatal(err)
		}
		if _, err := Init(Options{Dir: o.Dir}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(o.Dir, "database-password"))
		if string(b) != password+"\n" {
			t.Fatal("escaped password was not recovered")
		}
	})
}
func TestInitRejectsWithoutChangingFiles(t *testing.T) {
	for _, kind := range []string{"conflict", "bad token", "bad vless", "unknown vless field", "database mismatch", "external database", "symlink", "directory", "fifo", "oversized", "insecure directory"} {
		t.Run(kind, func(t *testing.T) {
			o := fresh(t)
			if _, err := Init(o); err != nil {
				t.Fatal(err)
			}
			path := func(name string) string { return filepath.Join(o.Dir, name) }
			write := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(path(name), []byte(value), 0644); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "conflict":
				o.Port = 443
			case "bad token":
				write("api-token", "DO_NOT_ECHO_SECRET")
			case "bad vless":
				write("vless.json", "DO_NOT_ECHO_SECRET")
			case "unknown vless field":
				b, _ := os.ReadFile(path("vless.json"))
				write("vless.json", strings.Replace(string(b), "{", "{\"unknown\":true,", 1))
			case "database mismatch":
				write("database-password", "DO_NOT_ECHO_SECRET")
			case "external database":
				write("database-url", "postgres://pulse_node:DO_NOT_ECHO_SECRET@external.test:5432/pulse_node?sslmode=disable")
			case "oversized":
				write("api-token", strings.Repeat("x", 4097))
			case "insecure directory":
				if err := os.Chmod(o.Dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink", "directory", "fifo":
				if err := os.Remove(path("api-token")); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink("database-password", path("api-token"))
				case "directory":
					err = os.Mkdir(path("api-token"), 0700)
				case "fifo":
					err = syscall.Mkfifo(path("api-token"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			// Remove one independent secret: failed validation must not recreate it.
			if err := os.Remove(path("database-url")); err != nil {
				t.Fatal(err)
			}
			// External-DB case instead removes token, keeping its invalid URL.
			if kind == "external database" {
				write("database-url", "postgres://pulse_node:DO_NOT_ECHO_SECRET@external.test:5432/pulse_node?sslmode=disable")
				if err := os.Remove(path("api-token")); err != nil {
					t.Fatal(err)
				}
			}
			// Keep a mismatched pair for this case.
			if kind == "database mismatch" {
				write("database-url", "postgres://pulse_node:different@database:5432/pulse_node?sslmode=disable")
			}
			_, err := Init(o)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if strings.Contains(err.Error(), "DO_NOT_ECHO_SECRET") {
				t.Fatal("secret leaked")
			}
			if kind != "external database" && kind != "database mismatch" {
				if _, err := os.Lstat(path("database-url")); !os.IsNotExist(err) {
					t.Fatal("wrote a file before all validation completed")
				}
			}
		})
	}
}
func TestInitRequiresSettingsAndExclusiveLock(t *testing.T) {
	o := fresh(t)
	if _, err := Init(Options{Dir: o.Dir}); err == nil {
		t.Fatal("missing transport accepted")
	}
	if _, err := os.Stat(filepath.Join(o.Dir, "api-token")); !os.IsNotExist(err) {
		t.Fatal("secret created on invalid input")
	}
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	before := readFiles(t, o.Dir)
	f, err := os.OpenFile(filepath.Join(o.Dir, ".init.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(o); err == nil {
		t.Fatal("concurrent init accepted")
	}
	assertUnchanged(t, o.Dir, before)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
}

func TestInitRejectsDirectorySymlinkAliases(t *testing.T) {
	for _, suffix := range []string{"", "/", "/."} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			o := fresh(t)
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, o.Dir); err != nil {
				t.Fatal(err)
			}
			o.Dir += suffix
			if _, err := Init(o); err == nil {
				t.Fatal("directory symlink accepted")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatal("symlink target modified")
			}
		})
	}
}

func TestInitCompletesPublishedPrefix(t *testing.T) {
	for count := 1; count < len(managed); count++ {
		t.Run(managed[count-1], func(t *testing.T) {
			o := fresh(t)
			if _, err := Init(o); err != nil {
				t.Fatal(err)
			}
			before := readFiles(t, o.Dir)
			// A crash can leave any published prefix. No unpublished credentials are durable.
			for _, name := range managed[count:] {
				if err := os.Remove(filepath.Join(o.Dir, name)); err != nil {
					t.Fatal(err)
				}
				delete(before, name)
			}
			result, err := Init(o)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Preserved) != count || len(result.Created) != len(managed)-count {
				t.Fatal("partial init was not completed")
			}
			assertUnchanged(t, o.Dir, before)
			// Now every file is valid and a second repeat is entirely read-only.
			complete := readFiles(t, o.Dir)
			if _, err := Init(Options{Dir: o.Dir}); err != nil {
				t.Fatal(err)
			}
			assertUnchanged(t, o.Dir, complete)
		})
	}
}

func TestInitPreservesExistingUsersAndCustomTransport(t *testing.T) {
	o := fresh(t)
	o.Listen = "127.0.0.1"
	o.Port = 9443
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	before := readFiles(t, o.Dir)
	var v node.VLESS
	if err := json.Unmarshal(before["vless.json"], &v); err != nil {
		t.Fatal("invalid test settings")
	}
	v.Users = []node.User{{Name: "synthetic-user", UUID: "11111111-2222-4333-8444-555555555555"}}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Dir, "vless.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	before["vless.json"] = raw
	if _, err := Init(Options{Dir: o.Dir}); err != nil {
		t.Fatal(err)
	}
	assertUnchanged(t, o.Dir, before)
}
