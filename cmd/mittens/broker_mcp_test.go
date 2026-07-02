package main

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// startMCPBroker starts a broker on a Unix socket and returns it plus the sock
// path for raw (hijack) dialing.
func startMCPBroker(t *testing.T, specs []MCPProxySpec) (*HostBroker, string) {
	t.Helper()
	sock := shortSockPath(t)
	b := NewHostBroker(sock, "", nil)
	b.RegisterMCPServers(specs)
	go func() { _ = b.Serve() }()
	waitForBroker(t, brokerClient(sock))
	t.Cleanup(func() { _ = b.Close() })
	return b, sock
}

// dialMCPStream opens a raw hijack stream to /mcp/<name> and consumes the
// response headers, returning the conn and a reader positioned at the body.
func dialMCPStream(t *testing.T, sock, name string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /mcp/"+name+" HTTP/1.1\r\nHost: broker\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("unexpected status: %s", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return conn, br
}

// TestBrokerMCP_RoundTripHalfClose pipes bytes to a `cat` child and reads them
// back after a half-close (cat flushes and exits on stdin EOF).
func TestBrokerMCP_RoundTripHalfClose(t *testing.T) {
	_, sock := startMCPBroker(t, []MCPProxySpec{{Name: "echo", Command: "cat"}})
	conn, br := dialMCPStream(t, sock, "echo")
	defer conn.Close()

	if _, err := io.WriteString(conn, "hello mcp\n"); err != nil {
		t.Fatal(err)
	}
	// Half-close: cat sees stdin EOF, flushes, and exits.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil {
			t.Fatal(err)
		}
	}
	out, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hello mcp\n" {
		t.Fatalf("round trip = %q, want %q", out, "hello mcp\n")
	}
}

func TestBrokerMCP_UnregisteredIs404(t *testing.T) {
	_, sock := startMCPBroker(t, []MCPProxySpec{{Name: "echo", Command: "cat"}})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /mcp/nope HTTP/1.1\r\nHost: broker\r\n\r\n")
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "404") {
		t.Fatalf("expected 404, got %s", status)
	}
}

// TestBrokerMCP_ReconnectRespawns exercises kill-and-respawn: a first stream is
// abruptly closed, then a second stream to the same name works (the previous
// child was reaped).
func TestBrokerMCP_ReconnectRespawns(t *testing.T) {
	_, sock := startMCPBroker(t, []MCPProxySpec{{Name: "echo", Command: "cat"}})

	conn1, _ := dialMCPStream(t, sock, "echo")
	io.WriteString(conn1, "first\n")
	conn1.Close() // abrupt disconnect

	// Give the broker a moment to tear down the first child.
	time.Sleep(200 * time.Millisecond)

	conn2, br2 := dialMCPStream(t, sock, "echo")
	defer conn2.Close()
	io.WriteString(conn2, "second\n")
	if cw, ok := conn2.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	out, err := io.ReadAll(br2)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "second\n" {
		t.Fatalf("respawn round trip = %q, want %q", out, "second\n")
	}
}
