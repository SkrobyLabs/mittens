package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProxyAllowedCONNECT(t *testing.T) {
	wl := newDomainWhitelist([]string{"api.github.com"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &proxyServer{whitelist: wl, listener: ln}
	go p.serve()
	defer p.close()

	addr := ln.Addr().String()

	// Send a CONNECT request to the proxy.
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The proxy tries to dial the real api.github.com:443 — in a test
	// environment this may succeed or fail depending on network.
	// We just verify it attempted the connection (200) rather than blocked (403).
	if resp.StatusCode == 403 {
		t.Error("expected CONNECT to api.github.com to be allowed, got 403")
	}
}

func TestProxyBlockedCONNECT(t *testing.T) {
	wl := newDomainWhitelist([]string{"api.github.com"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &proxyServer{whitelist: wl, listener: ln}
	go p.serve()
	defer p.close()

	addr := ln.Addr().String()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("expected 403 for blocked domain, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not in whitelist") {
		t.Errorf("expected 'not in whitelist' in body, got: %s", string(body))
	}
}

func TestProxyBlockedNon443Port(t *testing.T) {
	wl := newDomainWhitelist([]string{"api.github.com"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &proxyServer{whitelist: wl, listener: ln}
	go p.serve()
	defer p.close()

	addr := ln.Addr().String()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT api.github.com:8080 HTTP/1.1\r\nHost: api.github.com:8080\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("expected 403 for non-443 CONNECT, got %d", resp.StatusCode)
	}
}

func TestProxyBlockedHTTP(t *testing.T) {
	wl := newDomainWhitelist([]string{"api.github.com"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &proxyServer{whitelist: wl, listener: ln}
	go p.serve()
	defer p.close()

	addr := ln.Addr().String()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "GET http://evil.com/ HTTP/1.1\r\nHost: evil.com\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("expected 403 for blocked HTTP request, got %d", resp.StatusCode)
	}
}
