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
	"strings"
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
	// learn, when true, forwards out-of-allowlist hosts instead of blocking them,
	// still reporting each via onDeny so the run can discover required domains.
	learn bool
	// onDeny, if set, is called with the hostname of each denied (or, in learn
	// mode, observed-but-allowed) request so the host broker can surface it.
	// May be nil.
	onDeny func(host string)
}

// reportDeny forwards a denied hostname to the deny reporter, if configured.
func (p *proxyServer) reportDeny(host string) {
	if p.onDeny != nil {
		p.onDeny(host)
	}
}

// forkProxy starts the proxy as a separate child process that stays root.
// This is necessary because the parent process will syscall.Exec to drop
// privileges — which would kill an in-process goroutine-based proxy.
// The child process inherits the root UID and survives the parent's exec.
func forkProxy(domains []string, cfg *config) error {
	// Serialize config for the child via env vars.
	domainsJSON, _ := json.Marshal(domains)

	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(),
		"MITTENS_PROXY_MODE=1",
		"MITTENS_PROXY_DOMAINS="+string(domainsJSON),
	)
	if cfg.Verbose {
		cmd.Env = append(cmd.Env, "MITTENS_PROXY_VERBOSE=1")
	}
	if cfg.FirewallLearn {
		cmd.Env = append(cmd.Env, "MITTENS_PROXY_LEARN=1")
	}
	// Pass broker connection details so the proxy can report blocked egress
	// attempts to the host (visible via `mittens logs`).
	cmd.Env = append(cmd.Env,
		"MITTENS_PROXY_BROKER_SOCK="+cfg.BrokerSock,
		"MITTENS_PROXY_BROKER_PORT="+cfg.BrokerPort,
		"MITTENS_PROXY_BROKER_TOKEN="+cfg.BrokerToken,
	)

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
	learn := os.Getenv("MITTENS_PROXY_LEARN") == "1"

	wl := newDomainWhitelist(domains)
	onDeny := newDenyReporter(&config{
		BrokerSock:  os.Getenv("MITTENS_PROXY_BROKER_SOCK"),
		BrokerPort:  os.Getenv("MITTENS_PROXY_BROKER_PORT"),
		BrokerToken: os.Getenv("MITTENS_PROXY_BROKER_TOKEN"),
	})
	p, err := startProxy(wl, verbose, learn, onDeny)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mittens-proxy] %v\n", err)
		os.Exit(1)
	}

	_ = p
	// Block forever — the parent will kill us when the container exits.
	select {}
}

// newDenyReporter returns a function that reports each unique denied hostname to
// the host broker exactly once per run. Returns nil when no broker is
// configured, leaving the proxy to log denials locally only.
func newDenyReporter(cfg *config) func(string) {
	client := newBrokerClient(cfg)
	if client == nil {
		return nil
	}
	var mu sync.Mutex
	seen := make(map[string]bool)
	return func(host string) {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			return
		}
		mu.Lock()
		dup := seen[host]
		seen[host] = true
		mu.Unlock()
		if dup {
			return
		}
		// Fire-and-forget so request handling is never blocked on the broker.
		go func() { _, _ = client.post("/egress-deny", "text/plain", host) }()
	}
}

// startProxy creates and starts the forward proxy on 127.0.0.1:3128.
// Returns the proxy server (for later shutdown) or an error.
func startProxy(wl *domainWhitelist, verbose, learn bool, onDeny func(string)) (*proxyServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:3128")
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}

	logFile, _ := os.OpenFile("/tmp/proxy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)

	p := &proxyServer{
		whitelist: wl,
		listener:  ln,
		verbose:   verbose,
		learn:     learn,
		logFile:   logFile,
		onDeny:    onDeny,
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
		p.reportDeny(host)
		if !p.learn {
			p.log("DENIED  CONNECT %s:%s (not in whitelist)", host, port)
			writeHTTPError(conn, 403, denyMessage(host))
			return
		}
		p.log("LEARN   CONNECT %s:%s (allowed, outside allowlist)", host, port)
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
		p.reportDeny(host)
		if !p.learn {
			p.log("DENIED  %s http://%s%s (not in whitelist)", req.Method, host, req.URL.Path)
			writeHTTPError(conn, 403, denyMessage(host))
			return
		}
		p.log("LEARN   %s http://%s%s (allowed, outside allowlist)", req.Method, host, req.URL.Path)
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

// denyMessage is the body returned to the agent when the firewall blocks a
// host. It names the host and gives the exact, copy-pasteable command the
// operator runs on the host to allow it, so the agent can relay an actionable
// fix instead of an opaque network error.
func denyMessage(host string) string {
	return fmt.Sprintf("domain %q blocked by mittens firewall; ask the operator to allow it on the host: mittens policy allow %s", host, host)
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
