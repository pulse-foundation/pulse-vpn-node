//go:build linux

package telemetry

import (
	"net"
	"os"
	"syscall"
)

// Linux's datagram ICMP socket uses ping_group_range, not CAP_NET_RAW. Binding
// port zero allocates the kernel echo identifier that the reply must match.
func openPingSocket() (net.PacketConn, uint16, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_ICMP)
	if err != nil {
		return nil, 0, errICMPUnavailable
	}
	file := os.NewFile(uintptr(fd), "telemetry-ping")
	defer file.Close()
	if err = syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		return nil, 0, errICMPUnavailable
	}
	address, err := syscall.Getsockname(fd)
	if err != nil {
		return nil, 0, errICMPUnavailable
	}
	inet, ok := address.(*syscall.SockaddrInet4)
	if !ok {
		return nil, 0, errICMPUnavailable
	}
	conn, err := net.FilePacketConn(file)
	if err != nil {
		return nil, 0, errICMPUnavailable
	}
	return conn, uint16(inet.Port), nil
}
