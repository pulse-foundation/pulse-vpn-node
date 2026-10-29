package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func proxyClient(address string) (*http.Client, error) {
	cert, err := os.ReadFile(filepath.Join(fixtureDir, "target.crt"))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		return nil, errors.New("invalid fixture certificate")
	}
	proxy := &url.URL{Scheme: "http", Host: address}
	return &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func transfer(client *http.Client, scheme string) error {
	port := "8081"
	if scheme == "https" {
		port = "8443"
	}
	payload := make([]byte, 1<<20)
	_, _ = rand.Read(payload)
	res, err := client.Post(scheme+"://target:"+port+"/echo", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		return errors.New("proxied payload request failed")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, int64(len(payload)+1)))
	if err != nil || res.StatusCode != 200 || !bytes.Equal(payload, b) {
		return errors.New("proxied payload integrity failed")
	}
	return nil
}

func probe() error {
	// The target is only on the backend network. A direct path must not work.
	direct := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	if res, err := direct.Get("http://target:8081/health"); err == nil {
		res.Body.Close()
		return errors.New("target is directly reachable: test isolation failed")
	}
	token, err := os.ReadFile(filepath.Join(fixtureDir, "api-token"))
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("GET", "http://node:8080/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+string(token))
	res, err := direct.Do(req)
	if err != nil {
		return errors.New("agent status unavailable")
	}
	var status struct {
		Status     string `json:"status"`
		Configured bool   `json:"vpnConfigured"`
	}
	err = json.NewDecoder(res.Body).Decode(&status)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || status.Status != "ready" || !status.Configured {
		return errors.New("VPN not configured/ready")
	}
	client, err := proxyClient("client:7890")
	if err != nil {
		return err
	}
	// Readiness is condition-based; a failed attempt is never counted as a passing probe.
	deadline := time.Now().Add(20 * time.Second)
	for {
		err = transfer(client, "http")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := transfer(client, "https"); err != nil {
		return err
	}
	if err := udpEcho("client:7890"); err != nil {
		return err
	}
	fmt.Println("PASS: isolated VLESS/REALITY/Vision via Mihomo; HTTP + verified HTTPS 1 MiB echo; UDP/XUDP echo")
	return nil
}

func expectRejected(address string) error {
	// Ensure rejection is an authentication/path failure, not an unstarted client.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			return errors.New("negative-test client never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	client, err := proxyClient(address)
	if err != nil {
		return err
	}
	if err := transfer(client, "https"); err == nil {
		return errors.New("unexpected successful transfer")
	}
	fmt.Println("PASS: negative connection test rejected transfer")
	return nil
}
