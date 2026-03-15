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
.logo{font-size:48px;margin-bottom:8px}
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
  <div class="logo">🐱</div>
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

	// LogFile is an optional file for persistent debug logging.
	LogFile *os.File
}

// refreshCoordTimeout is how long a refresh coordinator holds the lock before
// it is considered stale and a new coordinator can be appointed.
const refreshCoordTimeout = 2 * time.Minute

// NewHostBroker creates a broker that will listen on sockPath.
// seed is the initial credential JSON (may be empty).
// stores are host credential stores used for bidirectional sync.
func NewHostBroker(sockPath, seed string, stores []CredentialStore) *HostBroker {
	b := &HostBroker{
		sockPath: sockPath,
		creds:    seed,
		stores:   stores,
		done:     make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/open", b.handleOpen)
	mux.HandleFunc("/notify", b.handleNotify)
	mux.HandleFunc("/refresh", b.handleRefresh)
	mux.HandleFunc("/login-callback", b.handleLoginCallback)
	mux.HandleFunc("/", b.handle)
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
	if !b.authorize(w, r) {
		return
	}
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

func (b *HostBroker) handlePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCredentialSize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxCredentialSize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
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
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOpenURLSize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxOpenURLSize {
		http.Error(w, "URL too large", http.StatusRequestEntityTooLarge)
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
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxNotifySize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxNotifySize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
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
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	if !b.authorize(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
