package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/pulse-foundation/pulse-vpn-node/internal/bootstrap"
)

const initUsage = `Usage: pulse-vpn-node init [options]
  --config-dir DIR                 Private configuration directory (default .local)
  --reality-destination HOST:PORT  Required when creating vless.json
  --reality-server-names NAME,...  Required when creating vless.json
  --listen IP                     Container VPN bind address (default 0.0.0.0)
  --port PORT                     Container VPN port (default 8443)
Existing settings and secrets are validated and preserved. No services are started.
`

func runInit(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	// flag's default errors echo supplied values; init diagnostics must not.
	flags.SetOutput(io.Discard)
	o := bootstrap.Options{}
	names := ""
	flags.StringVar(&o.Dir, "config-dir", ".local", "")
	flags.StringVar(&o.Destination, "reality-destination", "", "")
	flags.StringVar(&names, "reality-server-names", "", "")
	flags.StringVar(&o.Listen, "listen", "", "")
	flags.IntVar(&o.Port, "port", 0, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(out, initUsage)
			return err
		}
		return errors.New("invalid init arguments; use init --help")
	}
	if flags.NArg() != 0 {
		return errors.New("init does not accept positional arguments; use init --help")
	}
	invalid := false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			invalid = invalid || o.Port < 1 || o.Port > 65535
		default:
			invalid = invalid || f.Value.String() == ""
		}
	})
	if invalid {
		return errors.New("invalid empty or out-of-range init option; use init --help")
	}
	if names != "" {
		o.ServerNames = strings.Split(names, ",")
	}
	result, err := bootstrap.Init(o)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return errors.New("cannot write init summary")
	}
	return nil
}
