package mihomo

import (
	"context"
	"testing"

	"github.com/pulse-foundation/pulse-vpn-node/internal/node"
)

func TestAdapterFailureReturnsNilRuntime(t *testing.T) {
	for _, settings := range []*node.VLESS{nil, {}} {
		var adapter node.Core = NewAdapter("/missing-core", t.TempDir())
		runtime, err := adapter.Start(context.Background(), settings)
		if err == nil || runtime != nil {
			t.Fatal("failed adapter must not return a typed-nil or live runtime")
		}
	}
}
