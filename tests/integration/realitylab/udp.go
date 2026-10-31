package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

var errUDPEchoTimeout = errors.New("UDP echo timed out")

type udpSession struct {
	control net.Conn
	conn    *net.UDPConn
}

func (s *udpSession) close() { s.conn.Close(); s.control.Close() }
func udpEcho(proxy string) error {
	session, err := openUDP(proxy)
	if err != nil {
		return err
	}
	defer session.close()
	return session.echo()
}
func openUDP(proxy string) (_ *udpSession, resultErr error) {
	control, err := net.DialTimeout("tcp", proxy, time.Second)
	if err != nil {
		return nil, errors.New("SOCKS control unavailable")
	}
	defer func() {
		if resultErr != nil {
			control.Close()
		}
	}()
	_ = control.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(control, greeting[:]); err != nil || greeting != [2]byte{5, 0} {
		return nil, errors.New("SOCKS greeting failed")
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	var head [4]byte
	if _, err := io.ReadFull(control, head[:]); err != nil || head[0] != 5 || head[1] != 0 {
		return nil, errors.New("SOCKS UDP associate failed")
	}
	host, err := readAddress(control, head[3])
	if err != nil {
		return nil, err
	}
	var port [2]byte
	if _, err := io.ReadFull(control, port[:]); err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host, _, _ = net.SplitHostPort(proxy)
	}
	relay, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))))
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		return nil, err
	}

	return &udpSession{control: control, conn: conn}, nil
}
func (s *udpSession) echo() error {
	return s.echoUntil(time.Now().Add(3 * time.Second))
}

func (s *udpSession) echoUntil(deadline time.Time) error {
	conn := s.conn
	var head [4]byte
	var port [2]byte
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("UDP echo deadline failed: %w", err)
	}
	payload := make([]byte, 256)
	_, _ = rand.Read(payload)
	// Mihomo resolves UDP destinations on the client. Docker deliberately does not
	// expose backend DNS to this client, so the harness supplies the target IPv4.
	target := net.ParseIP(os.Getenv("REALITY_TEST_TARGET_IP")).To4()
	if target == nil {
		return errors.New("test target IPv4 is required")
	}
	packet := append([]byte{0, 0, 0, 1}, target...)
	packet = append(packet, byte(9000>>8), byte(9000&255))
	packet = append(packet, payload...)
	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("UDP echo write failed: %w", err)
	}
	b := make([]byte, 2048)
	n, err := conn.Read(b)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return errUDPEchoTimeout
		}
		return fmt.Errorf("UDP echo read failed: %w", err)
	}
	reader := bytes.NewReader(b[:n])
	if _, err := io.ReadFull(reader, head[:]); err != nil || head[0] != 0 || head[1] != 0 || head[2] != 0 {
		return errors.New("invalid SOCKS UDP frame")
	}
	if _, err := readAddress(reader, head[3]); err != nil {
		return err
	}
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return err
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, payload) {
		return errors.New("UDP echo integrity failed")
	}
	return nil
}

// TCP and UDP trackers are closed independently, possibly in different sweeps.
// Permit transient echoes, but require a complete normal echo timeout inside a
// bounded cleanup window. A shortened final timeout or a probe fault cannot
// establish cessation of transfer.
func (s *udpSession) revoked() error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		probeDeadline := time.Now().Add(3 * time.Second)
		if probeDeadline.After(deadline) {
			return errors.New("revoked UDP association still transfers")
		}
		err := s.echoUntil(probeDeadline)
		if errors.Is(err, errUDPEchoTimeout) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("revoked UDP probe failed: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func readAddress(r io.Reader, kind byte) (string, error) {
	n := 0
	switch kind {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", err
		}
		n = int(l[0])
	default:
		return "", errors.New("invalid SOCKS address type")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	if kind == 3 {
		return string(b), nil
	}
	return net.IP(b).String(), nil
}
