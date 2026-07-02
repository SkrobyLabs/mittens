package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

const oauthSuccessPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>mittens — Login successful</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{
  font-family:system-ui,-apple-system,sans-serif;
  min-height:100vh;display:flex;align-items:center;justify-content:center;
  background:linear-gradient(135deg,#f0f4ff 0%,#e8eef8 100%);
  color:#1a1a2e;
}
@media(prefers-color-scheme:dark){
  body{background:linear-gradient(135deg,#1a1a2e 0%,#16213e 100%);color:#e8e8e8}
  .card{background:#1f2937;box-shadow:0 8px 32px rgba(0,0,0,.4)}
  .brand{color:#d1d5db}
  h1{color:#f9fafb}
  .subtitle{color:#9ca3af}
  .hint{color:#6b7280;border-top-color:#374151}
}
.card{
  background:#fff;border-radius:16px;padding:48px 40px;max-width:420px;width:90%;
  text-align:center;box-shadow:0 8px 32px rgba(0,0,0,.08);
}
.logo{margin-bottom:8px}
.logo img{width:96px;height:auto}
.brand{font-size:14px;font-weight:600;letter-spacing:2px;text-transform:uppercase;color:#374151;margin-bottom:28px}
.check{
  width:56px;height:56px;border-radius:50%;
  background:#22c55e;color:#fff;font-size:28px;line-height:56px;
  margin:0 auto 20px;
}
h1{font-size:22px;font-weight:600;margin-bottom:8px;color:#111827}
.subtitle{color:#555;font-size:15px;line-height:1.5;margin-bottom:24px}
.hint{font-size:13px;color:#888;border-top:1px solid #eee;padding-top:20px}
</style>
</head>
<body>
<div class="card">
  <div class="logo"><img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAGAAAAA0CAMAAABCWc3rAAAAY1BMVEX///+qqpnMzLu7qqqqmZm7qpnMu7vd3cy7u6rdzLvu7t3u3d3dzMzMu6r/7u7//+7////u7u7d3d3u3cyqmYi7mYiqiHf/7t2ZiHe7qoiqmXfMu5nMzKqZiGaqqoiId2aqiIiR4ZqfAAAAAXRSTlMAQObYZgAABElJREFUWMPtV8uS4zYMlGlTEMAHRJNry96ZJP//lWlQk8PUOimbrtyWh7HKnkIT3Y2Hpun3GT8H5575t+NpFMBP/wlwnMl+Xw6HwyDA8Xha5n/9lSUwkfen6ak8H6dwiJHo+OinRFk1kyGk4fjTFDmsa3lww0Si66qlxMf4Tx5HQRAkuOXb13MkJBZyEcFvtAxGxyFGCFWKLpzjHucYqeiqIoDQLJqBME6QC1ryWshV8JHJtYPDzVewJivICSp4FhcH44MJ0CyKPHJRIYrRcM4kBrAWeAgfmpkHbcQuGwARFwkxFqWyAmEFIoUiWRIQ7EQekmFmzlqE4lyLONw1x7IWgwSck/7XWApEaR4BYCchSIbCMGNQ1UK82oeuICwSQouDW+0j+SGAEBQSznQG+zCsUEPUTpKAJ0fVyEJ8id639CpNKcUsWR2pXbtzVVNjAACiFNQAUhTHhszpx2Xyr2SxLIfk6QqacdeSwbz58Ty3S5JQTHnzT4BMyXGtzm8+pRdKGpdp3nMVPqOQdd0RCurWcshCJUuH5ITA7F5WwPvFT4cEg++cr2Z7KI5nKAHr4LP7qQaHZjeisJ2WkpmnA+BYg8CTonmYzlVzpV4EbrgZLbPp2V1DnSSAQIKvpGo1PI5m1LFSJkWrWDOCVtzUguZgieROUaVOEioB2uhILZ/R5frtOzsWVOueCMSAjQynAyhYo9dzmHHLuH450mDwFHbNM1kJAqeXm30R4+vNwozutAe2QlMKNZvaoKhyXXvTU0mxM8Wvz7XFKJm/nImeHPocMPPApxpyB0CfomwUOXp5sXBoaRKT2RT8C/rOP+YxyqRqb+QVMxvFQANTzWUUa/F8BQIiFau0fTrsI0FQBdkkhlOlDIycaENrtc4gPShSAV+BStmbhA1MDVguzMJhYPBjCFj7ObTEVMAIzt4k+lyDzhAGHnJm4Toy94/aJ3ptW3NVYr1mqX3sAMesC7XVmrV5qKaRsQ8FChxPy7YhC67EIYP5bOucSRwCNhbOoWgZ60bwqVjvL+zbtl1unoP0IV+Z9kQwccjaCKXYBgDgo73FoZr8vbXtfvvJ1sGpUL1eIT0njCNT+7kt/9eTsD9jr8OKxUdcE5nc7+2HZ+aPWkEZhpmtFKEO9+uTI0w1cBFSlxYDEnR9Xu4NcyxhWbHdVeLMg/FxWjtgYjF7wMCb4N9v7Q9oAuVvfkEZ4Kuxrej7+XPhGbpq/blhWPt2gyaXCxJhMJWGJP523Hn291SZb7j5fi5w1rZ9Gpzf3gwfbU8R5y2udx9oHli3kz9A9O0TkuDbt+KjEZVer1gQUdSCZY7kyvOCNwROrU3+5N94B0HP2FcUVfRmtFip6SPCT9iHuuh+Wtz0TgpLn2rr/sZHjDSMqm37y+8HCi/tLZnR2Arsjsbw4X+NtG33twSwEEfu937sFf8WPU8d/8577BNnPv3PIbz0WvD7PDx/A5FRT23ffejWAAAAAElFTkSuQmCC" alt="mittens"></div>
  <div class="brand">mittens</div>
  <div class="check">✓</div>
  <h1>Login successful</h1>
  <p class="subtitle">mittens intercepted the OAuth callback and forwarded your credentials to the container.</p>
  <p class="hint">You can close this tab and return to your terminal.</p>
</div>
</body>
</html>`

// HostBroker is an HTTP server that bridges communication between the host and
// mittens containers. It handles credential sync (freshest-wins), OAuth login
// interception, URL forwarding, notifications, and token refresh coordination.
//
// Containers push refreshed tokens via PUT and pull the latest via GET.
// The broker accepts a PUT only when the incoming expiresAt exceeds the
// currently stored value, so the freshest token always wins.
type HostBroker struct {
	sockPath  string
	Name      string // provider name for log identification, e.g. "claude", "gemini"
	AuthToken string
	Host      HostBridgeConfig
	creds     string // latest credential JSON
	mu        sync.RWMutex
	srv       *http.Server
	ln        net.Listener
	stores    []CredentialStore // host credential stores for bidirectional sync
	done      chan struct{}     // signals hostSync goroutine to stop

	// pendingCallback holds a captured OAuth callback URL for the container to replay.
	pendingCallback string

	// MCP proxy state: registered specs and live host children, guarded by mcpMu.
	// mcpMu is held only for state transitions, never across stream I/O.
	mcpSpecs     map[string]MCPProxySpec
	mcpChildren  map[string]*mcpChild
	mcpLocks     map[string]*sync.Mutex
	mcpWorkspace string
	mcpMu        sync.Mutex

	// Refresh coordination: only one container should perform a proactive token
	// refresh at a time. The first to POST /refresh becomes the coordinator;
	// others receive "wait" until fresh credentials arrive or the deadline expires.
	refreshInProgress bool
	refreshDeadline   time.Time
	refreshMu         sync.Mutex

	// OnOpen is called when a container requests a URL to be opened on the host.
	OnOpen func(url string)

	// OnLoginForward proxies an OAuth browser request straight into the
	// container's login callback server and returns its response. When set, the
	// intercept server relays the provider's real responses (including its own
	// success page) instead of capturing the callback for shim replay. May be nil.
	OnLoginForward func(port int, requestURI string) (*LoginForwardResponse, error)

	// OnNotify is called when a container sends a notification to the host.
	OnNotify func(container, event, message string)

	// OnClipboardRead is called when a container requests clipboard image data.
	// Returns PNG bytes or nil if no image is available.
	OnClipboardRead func() []byte

	// OnEgressDeny is called the first time the firewall blocks egress to a
	// given hostname, so the host can surface it. May be nil.
	OnEgressDeny func(host string)

	// Learn marks the run as a firewall-learn pass: out-of-allowlist hosts are
	// observed-but-allowed rather than blocked. It only changes how reports are
	// logged, so the logs describe exactly what happened.
	Learn bool

	// deniedHosts deduplicates blocked-egress reports across the run.
	deniedHosts map[string]bool

	// LogFile is an optional file for persistent debug logging.
	LogFile *os.File
}

// LoginForwardResponse carries an in-container login server response proxied
// back to the user's browser. Only the headers the browser needs to continue
// the flow are relayed.
type LoginForwardResponse struct {
	Status      int
	Location    string
	ContentType string
	Body        []byte
}

// HostBridgeConfig controls optional host integrations exposed to containers.
type HostBridgeConfig struct {
	OpenURLs        bool
	Notifications   bool
	ClipboardImages bool
}

// NewHostBroker creates a broker that will listen on sockPath.
// seed is the initial credential JSON (may be empty).
// stores are host credential stores used for bidirectional sync.
func NewHostBroker(sockPath, seed string, stores []CredentialStore) *HostBroker {
	b := &HostBroker{
		sockPath: sockPath,
		creds:    seed,
		stores:   stores,
		done:     make(chan struct{}),
		Host:     defaultHostBridgeConfig(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/open", b.withAuth(b.handleOpen))
	mux.HandleFunc("/notify", b.withAuth(b.handleNotify))
	mux.HandleFunc("/refresh", b.withAuth(b.handleRefresh))
	mux.HandleFunc("/login-callback", b.withAuth(b.handleLoginCallback))
	mux.HandleFunc("/clipboard", b.withAuth(b.handleClipboard))
	mux.HandleFunc("/egress-deny", b.withAuth(b.handleEgressDeny))
	mux.HandleFunc("/mcp/", b.withAuth(b.handleMCPStream))
	mux.HandleFunc("/", b.withAuth(b.handle))
	b.srv = &http.Server{Handler: mux}
	return b
}

func defaultHostBridgeConfig() HostBridgeConfig {
	return HostBridgeConfig{
		OpenURLs:        true,
		Notifications:   true,
		ClipboardImages: true,
	}
}

// blog writes a timestamped log entry to the broker's log file (if set).
func (b *HostBroker) blog(format string, args ...interface{}) {
	if b.LogFile == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	name := b.Name
	if name == "" {
		name = "?"
	}
	fmt.Fprintf(b.LogFile, "%s [broker:%d/%s] %s\n", ts, os.Getpid(), name, msg)
}

// ListenTCPAddr binds to a random TCP port on the given address and returns the port.
func (b *HostBroker) ListenTCPAddr(addr string) (int, error) {
	ln, err := net.Listen("tcp", addr+":0")
	if err != nil {
		return 0, err
	}
	b.ln = ln
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// ListenTCP binds to a random TCP port on localhost and returns the port.
// Call this before Serve() to use TCP mode instead of Unix socket.
func (b *HostBroker) ListenTCP() (int, error) {
	return b.ListenTCPAddr("127.0.0.1")
}

// Serve starts the broker. If ListenTCP() was called first, serves on that
// listener. Otherwise falls back to Unix socket mode using sockPath.
// Blocks until shut down via Close(). Call in a goroutine.
func (b *HostBroker) Serve() error {
	if b.ln == nil {
		// Unix socket mode (tests or when ListenTCP wasn't called).
		if b.sockPath == "" {
			return fmt.Errorf("no listener: call ListenTCP() or provide a socket path")
		}
		os.Remove(b.sockPath)
		ln, err := net.Listen("unix", b.sockPath)
		if err != nil {
			return err
		}
		b.ln = ln
		_ = os.Chmod(b.sockPath, 0666)
	}

	seedExp := expiresAt(b.creds)
	b.blog("listening on %s (seed expiresAt: %d, stores: %d)", b.ln.Addr(), seedExp, len(b.stores))

	// Start bidirectional host sync loop.
	if len(b.stores) > 0 {
		go b.hostSync()
	}

	return b.srv.Serve(b.ln)
}

// Close gracefully shuts down the broker and stops the host sync loop.
func (b *HostBroker) Close() error {
	b.blog("shutting down")
	b.closeMCPChildren()
	close(b.done)
	if b.LogFile != nil {
		b.LogFile.Close()
		b.LogFile = nil
	}
	if b.srv != nil {
		return b.srv.Shutdown(context.Background())
	}
	return nil
}
