package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

// runMCPProxy is the mittens-mcp-proxy persona: it bridges the container-side
// MCP client's stdio to a host-side MCP process via a hijacked broker stream.
// The provider MCP config for proxied servers points command -> this binary
// with args [<server-name>].
func runMCPProxy() int {
	if len(os.Args) < 2 || strings.TrimSpace(os.Args[1]) == "" {
		fmt.Fprintln(os.Stderr, "mittens-mcp-proxy: missing server name")
		return 1
	}
	name := os.Args[1]

	cfg := loadConfig()
	if !cfg.hasBroker() {
		fmt.Fprintln(os.Stderr, "mittens-mcp-proxy: no broker configured")
		return 1
	}

	conn, err := dialBroker(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mittens-mcp-proxy: dial broker: %v\n", err)
		return 1
	}
	defer conn.Close()

	// Send a raw hijack request. We do NOT use the 5s-timeout http.Client here;
	// the stream is long-lived.
	req := "GET /mcp/" + name + " HTTP/1.1\r\nHost: broker\r\n"
	if cfg.BrokerToken != "" {
		req += "X-Mittens-Token: " + cfg.BrokerToken + "\r\n"
	}
	req += "\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		fmt.Fprintf(os.Stderr, "mittens-mcp-proxy: write request: %v\n", err)
		return 1
	}

	// Read the status line and headers up to the blank line. The bufio.Reader
	// may buffer initial body bytes, so it must be used for the stdout pump.
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "mittens-mcp-proxy: read status: %v\n", err)
		return 1
	}
	if !strings.Contains(status, " 200 ") {
		fmt.Fprintf(os.Stderr, "mittens-mcp-proxy: broker refused %q: %s", name, strings.TrimSpace(status))
		return 1
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "mittens-mcp-proxy: read headers: %v\n", err)
			return 1
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	// stdin -> conn; propagate EOF as a half-close so the host child sees stdin
	// close (MCP's polite teardown).
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	// conn -> stdout; exit when the stream ends.
	_, _ = io.Copy(os.Stdout, br)
	return 0
}

func dialBroker(cfg *config) (net.Conn, error) {
	if cfg.BrokerSock != "" {
		return net.Dial("unix", cfg.BrokerSock)
	}
	return net.Dial("tcp", "host.docker.internal:"+cfg.BrokerPort)
}
