package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// proxyServer is an FQDN-filtering HTTP/HTTPS forward proxy.
// It handles explicit CONNECT requests (HTTPS) and plain HTTP forwarding,
// checking hostnames against a domain whitelist.
//
// All requests are logged to /tmp/proxy.log. Denied requests are always
// logged; allowed requests are logged when verbose is true.
type proxyServer struct {
	whitelist *domainWhitelist
	listener  net.Listener
	verbose   bool
	logFile   *os.File
}

// forkProxy starts the proxy as a separate child process that stays root.
// This is necessary because the parent process will syscall.Exec to drop
// privileges — which would kill an in-process goroutine-based proxy.
// The child process inherits the root UID and survives the parent's exec.
func forkProxy(domains []string, verbose bool) error {
	// Serialize config for the child via env vars.
	domainsJSON, _ := json.Marshal(domains)

	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(),
		"MITTENS_PROXY_MODE=1",
		"MITTENS_PROXY_DOMAINS="+string(domainsJSON),
	)
	if verbose {
		cmd.Env = append(cmd.Env, "MITTENS_PROXY_VERBOSE=1")
	}

	// Detach stdout/stderr so they don't block.
	logFile, _ := os.OpenFile("/tmp/proxy-child.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("fork proxy: %w", err)
	}

	// Don't Wait() — let the child run independently.
	return nil
}

// runProxyMain is the entry point for the forked proxy child process.
// It reads config from env vars and runs the proxy until killed.
func runProxyMain() {
	var domains []string
	if raw := os.Getenv("MITTENS_PROXY_DOMAINS"); raw != "" {
		json.Unmarshal([]byte(raw), &domains)
	}
	verbose := os.Getenv("MITTENS_PROXY_VERBOSE") == "1"

	wl := newDomainWhitelist(domains)
	p, err := startProxy(wl, verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mittens-proxy] %v\n", err)
		os.Exit(1)
	}

	_ = p
	// Block forever — the parent will kill us when the container exits.
	select {}
}

// startProxy creates and starts the forward proxy on 127.0.0.1:3128.
// Returns the proxy server (for later shutdown) or an error.
func startProxy(wl *domainWhitelist, verbose bool) (*proxyServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:3128")
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}

	logFile, _ := os.OpenFile("/tmp/proxy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)

	p := &proxyServer{
		whitelist: wl,
		listener:  ln,
		verbose:   verbose,
		logFile:   logFile,
	}
	go p.serve()
	return p, nil
}

func (p *proxyServer) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go p.handleConn(conn)
	}
}

func (p *proxyServer) close() {
	p.listener.Close()
	if p.logFile != nil {
		p.logFile.Close()
	}
}

// log writes a timestamped line to /tmp/proxy.log.
// Denied requests are always logged; allowed requests only when verbose.
func (p *proxyServer) log(format string, args ...interface{}) {
	if p.logFile == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(p.logFile, "%s %s\n", ts, msg)
}

func (p *proxyServer) handleConn(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	if req.Method == http.MethodConnect {
		p.handleConnect(conn, req)
	} else {
		p.handleHTTP(conn, br, req)
	}
}

// handleConnect handles HTTPS CONNECT tunneling.
func (p *proxyServer) handleConnect(conn net.Conn, req *http.Request) {
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}

	// Only allow CONNECT to port 443.
	if port != "443" {
		p.log("DENIED  CONNECT %s:%s (port not allowed)", host, port)
		writeHTTPError(conn, 403, fmt.Sprintf("CONNECT to port %s not allowed", port))
		return
	}

	if !p.whitelist.allowed(host) {
		p.log("DENIED  CONNECT %s:%s (not in whitelist)", host, port)
		writeHTTPError(conn, 403, fmt.Sprintf("domain %q not in whitelist", host))
		return
	}

	// Dial the upstream server.
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if err != nil {
		p.log("ERROR   CONNECT %s:%s (dial failed: %v)", host, port, err)
		writeHTTPError(conn, 502, "failed to connect to upstream")
		return
	}
	defer upstream.Close()

	if p.verbose {
		p.log("ALLOWED CONNECT %s:%s", host, port)
	}

	// Tell the client the tunnel is established.
	_, _ = fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Splice the connections bidirectionally.
	splice(conn, upstream)
}

// handleHTTP forwards a plain HTTP request.
func (p *proxyServer) handleHTTP(conn net.Conn, br *bufio.Reader, req *http.Request) {
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "80"
	}

	// Only allow ports 80 and 443.
	if port != "80" && port != "443" {
		p.log("DENIED  %s http://%s:%s%s (port not allowed)", req.Method, host, port, req.URL.Path)
		writeHTTPError(conn, 403, fmt.Sprintf("port %s not allowed", port))
		return
	}

	if !p.whitelist.allowed(host) {
		p.log("DENIED  %s http://%s%s (not in whitelist)", req.Method, host, req.URL.Path)
		writeHTTPError(conn, 403, fmt.Sprintf("domain %q not in whitelist", host))
		return
	}

	// Dial upstream.
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if err != nil {
		p.log("ERROR   %s http://%s%s (dial failed: %v)", req.Method, host, req.URL.Path, err)
		writeHTTPError(conn, 502, "failed to connect to upstream")
		return
	}
	defer upstream.Close()

	if p.verbose {
		p.log("ALLOWED %s http://%s%s", req.Method, host, req.URL.Path)
	}

	// Remove hop-by-hop headers.
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")

	// Forward the request.
	if err := req.Write(upstream); err != nil {
		writeHTTPError(conn, 502, "failed to write to upstream")
		return
	}

	// Relay the response back (including any subsequent data for keep-alive).
	splice(conn, upstream)
}

func writeHTTPError(conn net.Conn, code int, msg string) {
	body := fmt.Sprintf("%d %s\r\n", code, msg)
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
	_, _ = conn.Write([]byte(resp))
}

// splice copies data bidirectionally between two connections.
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	copy := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		// Signal the other direction to stop.
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}

	go copy(a, b)
	go copy(b, a)
	wg.Wait()
}
