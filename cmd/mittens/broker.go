package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
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
	creds     string // latest credential JSON
	mu        sync.RWMutex
	srv       *http.Server
	ln        net.Listener
	stores    []CredentialStore // host credential stores for bidirectional sync
	done      chan struct{}     // signals hostSync goroutine to stop

	// pendingCallback holds a captured OAuth callback URL for the container to replay.
	pendingCallback string

	// Refresh coordination: only one container should perform a proactive token
	// refresh at a time. The first to POST /refresh becomes the coordinator;
	// others receive "wait" until fresh credentials arrive or the deadline expires.
	refreshInProgress bool
	refreshDeadline   time.Time
	refreshMu         sync.Mutex

	// OnOpen is called when a container requests a URL to be opened on the host.
	OnOpen func(url string)

	// OnNotify is called when a container sends a notification to the host.
	OnNotify func(container, event, message string)

	// OnClipboardRead is called when a container requests clipboard image data.
	// Returns PNG bytes or nil if no image is available.
	OnClipboardRead func() []byte

	// OnPoolSpawn is called when Kitchen requests a worker container spawn.
	OnPoolSpawn func(spec pool.WorkerSpec) (containerName, containerID string, err error)

	// OnPoolKill is called when Kitchen requests a worker container kill.
	OnPoolKill func(workerID string) error

	// Runtime API callbacks exposed on /v1/workers/*.
	OnRuntimeListWorkers       func() ([]pool.RuntimeWorker, error)
	OnRuntimeGetWorker         func(workerID string) (*pool.RuntimeWorker, error)
	OnRuntimeRecycleWorker     func(workerID string) error
	OnRuntimeGetWorkerActivity func(workerID string) (*pool.WorkerActivity, []pool.WorkerActivityRecord, error)
	OnRuntimeSubmitAssignment  func(workerID string, assignment pool.Assignment) error

	// PoolToken is a separate token for pool management endpoints.
	PoolToken string

	// LogFile is an optional file for persistent debug logging.
	LogFile *os.File

	runtimeNotifyMu   sync.RWMutex
	runtimeNotifySubs map[int]chan pool.RuntimeEvent
	runtimeNotifySeq  int
}

// refreshCoordTimeout is how long a refresh coordinator holds the lock before
// it is considered stale and a new coordinator can be appointed.
const refreshCoordTimeout = 2 * time.Minute

// NewHostBroker creates a broker that will listen on sockPath.
// seed is the initial credential JSON (may be empty).
// stores are host credential stores used for bidirectional sync.
func NewHostBroker(sockPath, seed string, stores []CredentialStore) *HostBroker {
	b := &HostBroker{
		sockPath:          sockPath,
		creds:             seed,
		stores:            stores,
		done:              make(chan struct{}),
		runtimeNotifySubs: make(map[int]chan pool.RuntimeEvent),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/open", b.withAuth(b.handleOpen))
	mux.HandleFunc("/notify", b.withAuth(b.handleNotify))
	mux.HandleFunc("/refresh", b.withAuth(b.handleRefresh))
	mux.HandleFunc("/login-callback", b.withAuth(b.handleLoginCallback))
	mux.HandleFunc("/clipboard", b.withAuth(b.handleClipboard))
	mux.HandleFunc("POST /v1/workers", b.withPoolAuth(b.handleRuntimeSpawnWorker))
	mux.HandleFunc("DELETE /v1/workers/{id}", b.withPoolAuth(b.handleRuntimeKillWorker))
	mux.HandleFunc("GET /v1/workers", b.withPoolAuth(b.handleRuntimeWorkers))
	mux.HandleFunc("GET /v1/workers/{id}", b.withPoolAuth(b.handleRuntimeWorker))
	mux.HandleFunc("POST /v1/workers/{id}/recycle", b.withPoolAuth(b.handleRuntimeRecycleWorker))
	mux.HandleFunc("GET /v1/workers/{id}/activity", b.withPoolAuth(b.handleRuntimeWorkerActivity))
	mux.HandleFunc("POST /v1/workers/{id}/assignments", b.withPoolAuth(b.handleRuntimeWorkerAssignment))
	mux.HandleFunc("GET /v1/events", b.withPoolAuth(b.handleRuntimeEvents))
	mux.HandleFunc("/", b.withAuth(b.handle))
	b.srv = &http.Server{Handler: mux}
	return b
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

// Credentials returns the current credential JSON held by the broker.
func (b *HostBroker) Credentials() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.creds
}

func (b *HostBroker) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.handleGet(w)
	case http.MethodPut:
		b.handlePut(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *HostBroker) handleGet(w http.ResponseWriter) {
	b.mu.RLock()
	data := b.creds
	b.mu.RUnlock()

	if data == "" {
		b.blog("GET → 204 (no credentials)")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.blog("GET → 200 (expiresAt: %d, %d bytes)", expiresAt(data), len(data))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, data)
}

const maxCredentialSize = 64 * 1024 // 64KB

// readBody reads and size-checks the request body. Returns nil, false on error
// (the response has already been written).
func (b *HostBroker) readBody(w http.ResponseWriter, r *http.Request, maxSize int) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxSize)+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxSize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// requireMethod validates the request method. Returns false (response already
// written) if it doesn't match.
func (b *HostBroker) requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func (b *HostBroker) handlePut(w http.ResponseWriter, r *http.Request) {
	body, ok := b.readBody(w, r, maxCredentialSize)
	if !ok {
		return
	}

	incoming := string(body)
	incomingExp := expiresAt(incoming)
	if incomingExp == 0 {
		b.blog("PUT → 400 (missing/invalid expiresAt, %d bytes, keys: %s)", len(body), jsonKeys(incoming))
		http.Error(w, "invalid credentials: missing or invalid expiresAt", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	currentExp := expiresAt(b.creds)
	if incomingExp > currentExp {
		b.creds = incoming
		b.mu.Unlock()
		b.blog("PUT → 204 accepted (incoming: %d, was: %d)", incomingExp, currentExp)
		// Fresh credentials received — reset refresh coordination so the next
		// nearing-expiry cycle can appoint a new coordinator.
		b.refreshMu.Lock()
		b.refreshInProgress = false
		b.refreshMu.Unlock()
		// Write-through: persist fresher creds to host stores immediately.
		b.persistToHost(incoming)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.mu.Unlock()
	b.blog("PUT → 409 stale (incoming: %d, current: %d)", incomingExp, currentExp)
	http.Error(w, "stale credentials", http.StatusConflict)
}

// hostSync polls host credential stores every 5 seconds.
// If the host has fresher creds, the broker's in-memory state is updated.
func (b *HostBroker) hostSync() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			b.pullFromHost()
		}
	}
}

// pullFromHost reads from all host stores, picks the freshest, and updates
// the broker if the host has newer credentials.
func (b *HostBroker) pullFromHost() {
	var bestJSON string
	var bestExp int64

	for _, s := range b.stores {
		data, err := s.Extract()
		if err != nil || data == "" {
			continue
		}
		exp := expiresAt(data)
		if exp > bestExp {
			bestJSON = data
			bestExp = exp
		}
	}

	if bestJSON == "" {
		return
	}

	b.mu.Lock()
	currentExp := expiresAt(b.creds)
	if bestExp > currentExp {
		b.creds = bestJSON
		b.mu.Unlock()
		b.blog("hostSync: pulled fresher creds from host (host: %d, was: %d)", bestExp, currentExp)
		return
	}
	b.mu.Unlock()
}

// persistToHost writes credentials to all host stores (fire-and-forget).
func (b *HostBroker) persistToHost(jsonData string) {
	for _, s := range b.stores {
		if err := s.Persist(jsonData); err != nil {
			b.blog("persistToHost: FAILED %s: %v", s.Label(), err)
			logWarn("Broker: persist to %s: %v", s.Label(), err)
		} else {
			b.blog("persistToHost: wrote to %s", s.Label())
		}
	}
}

const maxOpenURLSize = 4096

func (b *HostBroker) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	body, ok := b.readBody(w, r, maxOpenURLSize)
	if !ok {
		return
	}

	rawURL := strings.TrimSpace(string(body))
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}

	b.blog("OPEN → %s", redactURL(rawURL))

	// Intercept OAuth login: start a temp listener on the callback port so
	// the browser redirect lands on the host (not lost inside the container).
	if port := extractOAuthCallbackPort(rawURL); port > 0 {
		ready := make(chan struct{})
		go b.interceptOAuthCallback(port, ready)
		<-ready // wait for listener to bind before opening the browser
	}

	if b.OnOpen != nil {
		b.OnOpen(rawURL)
	}
	w.WriteHeader(http.StatusNoContent)
}

const maxNotifySize = 4096

func (b *HostBroker) handleNotify(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	body, ok := b.readBody(w, r, maxNotifySize)
	if !ok {
		return
	}

	var payload struct {
		Container string `json:"container"`
		Event     string `json:"event"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	b.blog("NOTIFY → %s: %s", payload.Container, payload.Event)

	if b.OnNotify != nil {
		b.OnNotify(payload.Container, payload.Event, payload.Message)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRefresh coordinates proactive token refresh across containers.
// The first container to POST becomes the coordinator (receives "refresh");
// subsequent POsters receive "wait" until fresh creds arrive or the deadline expires.
func (b *HostBroker) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}

	b.refreshMu.Lock()
	now := time.Now()
	inProgress := b.refreshInProgress && now.Before(b.refreshDeadline)
	if !inProgress {
		b.refreshInProgress = true
		b.refreshDeadline = now.Add(refreshCoordTimeout)
	}
	b.refreshMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if inProgress {
		b.blog("REFRESH → wait (coordinator active until %s)", b.refreshDeadline.Format("15:04:05"))
		_, _ = io.WriteString(w, `{"action":"wait"}`)
		return
	}
	b.blog("REFRESH → refresh (coordinator appointed)")
	_, _ = io.WriteString(w, `{"action":"refresh"}`)
}

// handleLoginCallback returns the captured OAuth callback URL (if any) so the
// container can replay it to Claude Code's local callback server.
func (b *HostBroker) handleLoginCallback(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}

	b.mu.Lock()
	cb := b.pendingCallback
	b.pendingCallback = ""
	b.mu.Unlock()

	if cb == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	b.blog("login-callback → %s", redactURL(cb))
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, cb)
}

// handleClipboard reads the host clipboard and returns PNG image data.
// Returns 200 with image/png body if an image is available, 204 otherwise.
func (b *HostBroker) handleClipboard(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}

	if b.OnClipboardRead == nil {
		b.blog("CLIPBOARD → 204 (no reader configured)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	png := b.OnClipboardRead()
	if len(png) == 0 {
		b.blog("CLIPBOARD → 204 (no image)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	b.blog("CLIPBOARD → 200 (%d bytes)", len(png))
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

// withAuth wraps an HTTP handler with the broker's authorization check.
func (b *HostBroker) withAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.authorize(w, r) {
			return
		}
		handler(w, r)
	}
}

// withPoolAuth wraps an HTTP handler with both standard auth and an additional
// pool-management token check. Only Kitchen receives the pool token.
func (b *HostBroker) withPoolAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.authorize(w, r) {
			return
		}
		if b.PoolToken != "" {
			pt := r.Header.Get("X-Mittens-Pool-Token")
			if subtle.ConstantTimeCompare([]byte(pt), []byte(b.PoolToken)) != 1 {
				http.Error(w, "forbidden: missing or invalid pool token", http.StatusForbidden)
				return
			}
		}
		handler(w, r)
	}
}

func (b *HostBroker) authorize(w http.ResponseWriter, r *http.Request) bool {
	if b.AuthToken == "" {
		return true
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Mittens-Token")), []byte(b.AuthToken)) == 1 {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (b *HostBroker) subscribeRuntimeEvents(buffer int) (<-chan pool.RuntimeEvent, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan pool.RuntimeEvent, buffer)

	b.runtimeNotifyMu.Lock()
	id := b.runtimeNotifySeq
	b.runtimeNotifySeq++
	b.runtimeNotifySubs[id] = ch
	b.runtimeNotifyMu.Unlock()

	cancel := func() {
		b.runtimeNotifyMu.Lock()
		sub, ok := b.runtimeNotifySubs[id]
		if ok {
			delete(b.runtimeNotifySubs, id)
			close(sub)
		}
		b.runtimeNotifyMu.Unlock()
	}
	return ch, cancel
}

func (b *HostBroker) sendRuntimeEvent(event pool.RuntimeEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	b.runtimeNotifyMu.RLock()
	defer b.runtimeNotifyMu.RUnlock()
	for _, ch := range b.runtimeNotifySubs {
		select {
		case ch <- event:
		default:
		}
	}
}

// interceptOAuthCallback starts a temporary HTTP server on the host at the
// given port to capture the OAuth browser redirect. Once the callback arrives,
// it stores the full callback URL for the container to pick up via
// GET /login-callback.
func (b *HostBroker) interceptOAuthCallback(port int, ready chan struct{}) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		b.blog("OAuth intercept: failed to listen on :%d: %v", port, err)
		close(ready)
		return
	}
	b.blog("OAuth intercept: listening on :%d", port)
	close(ready)

	callbackCh := make(chan string, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		callbackURL := fmt.Sprintf("http://localhost:%d%s", port, r.URL.RequestURI())
		select {
		case callbackCh <- callbackURL:
		default:
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, oauthSuccessPage)
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	select {
	case cb := <-callbackCh:
		b.mu.Lock()
		b.pendingCallback = cb
		b.mu.Unlock()
		b.blog("OAuth intercept: captured callback")
	case <-time.After(2 * time.Minute):
		b.blog("OAuth intercept: timeout")
	case <-b.done:
		b.blog("OAuth intercept: broker closing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// jsonKeys returns the sorted top-level keys of a JSON object as a bracketed
// list (e.g. `[claudeAiOauth, primaryApiKey]`), or "<invalid JSON>" on failure.
func jsonKeys(s string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "<invalid JSON>"
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, ", ") + "]"
}

// redactURL replaces the values of sensitive query parameters in a URL with
// "REDACTED". Falls back to the original string if parsing fails.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	redacted := false
	for _, param := range []string{"code", "token", "access_token", "refresh_token"} {
		if q.Has(param) {
			q.Set(param, "REDACTED")
			redacted = true
		}
	}
	if !redacted {
		return rawURL
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// extractOAuthCallbackPort parses an OAuth authorization URL and returns the
// port from the redirect_uri parameter if it points to localhost.
// Returns 0 if the URL is not an OAuth redirect or doesn't use localhost.
func extractOAuthCallbackPort(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	redirectURI := u.Query().Get("redirect_uri")
	if redirectURI == "" {
		return 0
	}
	ru, err := url.Parse(redirectURI)
	if err != nil {
		return 0
	}
	h := ru.Hostname()
	if h != "localhost" && h != "127.0.0.1" {
		return 0
	}
	port, err := strconv.Atoi(ru.Port())
	if err != nil {
		return 0
	}
	return port
}
