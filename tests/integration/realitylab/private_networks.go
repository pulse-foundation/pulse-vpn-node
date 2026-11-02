package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func privateNetworks() error {
	// Positive control covers the same VLESS credentials, REALITY handshake,
	// public-domain HTTP/HTTPS and public-IP UDP paths as the negative checks.
	if err := probe(); err != nil {
		return err
	}
	privateIP := net.ParseIP(os.Getenv("REALITY_TEST_PRIVATE_IP"))
	if privateIP == nil || !privateIP.IsPrivate() {
		return errors.New("private target IP required")
	}
	client, err := proxyClient("client:7890")
	if err != nil {
		return err
	}
	for _, address := range []string{
		"private-target:8081", net.JoinHostPort(privateIP.String(), "8081"),
		net.JoinHostPort("::ffff:"+privateIP.String(), "8081"),
		"127.0.0.1:8080", "node:8080", "localhost:8080",
	} {
		res, err := client.Get("http://" + address + "/health")
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				return fmt.Errorf("private TCP destination timed out instead of rejecting: %s", address)
			}
			continue
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadGateway {
			return fmt.Errorf("private TCP destination reachable: %s (HTTP %d)", address, res.StatusCode)
		}
	}
	fresh, err := openUDP("client:7890")
	if err != nil {
		return err
	}
	defer fresh.close()
	if err := fresh.echoTo(privateIP.To4(), time.Now().Add(udpEchoTimeout)); !errors.Is(err, errUDPEchoTimeout) {
		return fmt.Errorf("fresh private UDP destination was not blocked: %v", err)
	}
	fmt.Println("PASS: fresh UDP association to private destination rejected")
	session, err := openUDP("client:7890")
	if err != nil {
		return err
	}
	defer session.close()
	// A positive echo on the same association rules out a broken SOCKS relay.
	if err := session.echo(); err != nil {
		return err
	}
	if err := session.echoTo(privateIP.To4(), time.Now().Add(udpEchoTimeout)); !errors.Is(err, errUDPEchoTimeout) {
		return fmt.Errorf("private UDP destination was not blocked: %v", err)
	}
	if err := session.echo(); err != nil {
		return err
	}
	fmt.Println("PASS: private domain/IP/mapped-IP TCP, loopback/management and private UDP blocked; public TCP/UDP preserved")
	return nil
}
