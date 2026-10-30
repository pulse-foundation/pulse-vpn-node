package main

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

func serveTarget() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(w, http.MaxBytesReader(w, r.Body, 2<<20)) })
	failures := make(chan error, 6)
	go func() {
		s := &http.Server{Addr: ":8443", Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
		failures <- s.ListenAndServeTLS(filepath.Join(fixtureDir, "target.crt"), filepath.Join(fixtureDir, "target.key"))
	}()
	go func() {
		s := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
		failures <- s.ListenAndServe()
	}()
	go func() {
		conn, err := net.ListenPacket("udp", ":9000")
		if err != nil {
			failures <- err
			return
		}
		defer conn.Close()
		b := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFrom(b)
			if err != nil {
				failures <- err
				return
			}
			if _, err := conn.WriteTo(b[:n], addr); err != nil {
				failures <- err
				return
			}
		}
	}()
	go func() {
		listener, err := net.Listen("tcp", ":9001")
		if err != nil {
			failures <- err
			return
		}
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				failures <- err
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	go func() { failures <- serveTrafficTCP() }()
	go func() { failures <- serveTrafficUDP() }()
	<-failures
	return errors.New("local target stopped")
}

func targetHealth() error {
	client := http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := client.Get("http://127.0.0.1:8081/health")
	if err != nil {
		return errors.New("target not ready")
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return errors.New("target not ready")
	}
	return nil
}
