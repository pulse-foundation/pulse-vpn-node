package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"time"
)

// These asymmetric targets exist only on the disposable lab's internal backend.
// The request header is upload traffic too; all sizes are capped before allocation.
func serveTrafficTCP() error {
	listener, err := net.Listen("tcp", ":9002")
	if err != nil {
		return err
	}
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
			var header [8]byte
			for {
				if _, err := io.ReadFull(conn, header[:]); err != nil {
					return
				}
				upload, download := binary.BigEndian.Uint32(header[:4]), binary.BigEndian.Uint32(header[4:])
				if upload > 1<<20 || download > 1<<20 {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(upload)); err != nil {
					return
				}
				if _, err := io.Copy(conn, bytes.NewReader(bytes.Repeat([]byte{0x5a}, int(download)))); err != nil {
					return
				}
			}
		}()
	}
}

func serveTrafficUDP() error {
	conn, err := net.ListenPacket("udp", ":9003")
	if err != nil {
		return err
	}
	defer conn.Close()
	packet := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFrom(packet)
		if err != nil {
			return err
		}
		if n < 2 {
			continue
		}
		download := int(binary.BigEndian.Uint16(packet[:2]))
		if download > 1500 {
			continue
		}
		if _, err := conn.WriteTo(bytes.Repeat([]byte{0x5a}, download), addr); err != nil {
			return err
		}
	}
}
