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
	if !strings.Contains(string(body), "blocked by mittens firewall") {
		t.Errorf("expected block notice in body, got: %s", string(body))
	}
	if !strings.Contains(string(body), "mittens policy allow evil.com") {
		t.Errorf("expected copy-pasteable allow command in body, got: %s", string(body))
	}
}

func TestDenyMessageIncludesGuidance(t *testing.T) {
	msg := denyMessage("api.example.com")
	if !strings.Contains(msg, "api.example.com") {
		t.Errorf("deny message should name the host: %s", msg)
	}
	if !strings.Contains(msg, "mittens policy allow api.example.com") {
		t.Errorf("deny message should give the allow command: %s", msg)
	}
}

func TestProxyLearnModeAllowsAndReports(t *testing.T) {
	wl := newDomainWhitelist([]string{"api.github.com"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	reported := make(chan string, 1)
	p := &proxyServer{
		whitelist: wl,
		listener:  ln,
		learn:     true,
		onDeny:    func(host string) { reported <- host },
	}
	go p.serve()
	defer p.close()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// .invalid never resolves, so the upstream dial fails fast (502) without a
	// real network call — but in learn mode the host must still be reported and
	// must NOT be refused with 403.
	fmt.Fprintf(conn, "CONNECT outside.invalid:443 HTTP/1.1\r\nHost: outside.invalid:443\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == 403 {
		t.Errorf("learn mode must not refuse out-of-allowlist hosts, got 403")
	}

	select {
	case host := <-reported:
		if host != "outside.invalid" {
			t.Errorf("reported host = %q, want outside.invalid", host)
		}
	case <-time.After(2 * time.Second):
		t.Error("learn mode did not report the out-of-allowlist host")
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
