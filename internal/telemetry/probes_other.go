//go:build !linux

package telemetry

import "net"

func openPingSocket() (net.PacketConn, uint16, error) { return nil, 0, errUnsupportedProbe }
