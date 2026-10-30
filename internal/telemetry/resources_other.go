//go:build !linux

package telemetry

import "context"

func filesystemValues(ctx context.Context, path string) ([6]uint64, string) {
	return [6]uint64{}, "unsupported"
}
