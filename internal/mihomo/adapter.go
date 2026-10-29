package mihomo

import (
	"context"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

// Adapter owns Mihomo-specific startup wiring. The agent lifecycle sees only
// node.Core and node.Runtime; it does not know the controller protocol or port.
type Adapter struct {
	binary    string
	directory string
}

func NewAdapter(binary, directory string) *Adapter {
	return &Adapter{binary: binary, directory: directory}
}

func (a *Adapter) Start(ctx context.Context, settings *node.VLESS) (node.Runtime, error) {
	process, err := Start(ctx, Options{
		Binary: a.binary, Directory: a.directory,
		ControllerAddress: "127.0.0.1:19090", VLESS: settings,
	})
	if err != nil {
		return nil, err
	}
	return process, nil
}

var _ node.Core = (*Adapter)(nil)
var _ node.Runtime = (*Process)(nil)
