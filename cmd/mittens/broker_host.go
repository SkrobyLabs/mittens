package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxOpenURLSize = 4096

func (b *HostBroker) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	if !b.Host.OpenURLs {
		b.blog("OPEN -> 403 (disabled)")
		http.Error(w, "URL opening disabled by host policy", http.StatusForbidden)
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

	b.blog("OPEN -> %s", redactURL(rawURL))

	// Intercept OAuth login so browser redirects land on the host and can be
	// replayed into the container.
	if port := extractOAuthCallbackPort(rawURL); port > 0 {
		ready := make(chan struct{})
		go b.interceptOAuthCallback(port, ready)
		<-ready
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
	if !b.Host.Notifications {
		b.blog("NOTIFY -> 204 (disabled)")
		w.WriteHeader(http.StatusNoContent)
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

	b.blog("NOTIFY -> %s: %s", payload.Container, payload.Event)

	if b.OnNotify != nil {
		b.OnNotify(payload.Container, payload.Event, payload.Message)
	}
	w.WriteHeader(http.StatusNoContent)
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

	b.blog("login-callback -> %s", redactURL(cb))
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, cb)
}

// handleClipboard reads the host clipboard and returns PNG image data.
// Returns 200 with image/png body if an image is available, 204 otherwise.
func (b *HostBroker) handleClipboard(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}
	if !b.Host.ClipboardImages {
		b.blog("CLIPBOARD -> 204 (disabled)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if b.OnClipboardRead == nil {
		b.blog("CLIPBOARD -> 204 (no reader configured)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	png := b.OnClipboardRead()
	if len(png) == 0 {
		b.blog("CLIPBOARD -> 204 (no image)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	b.blog("CLIPBOARD -> 200 (%d bytes)", len(png))
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

const maxEgressDenySize = 512

// egressDenyWarnCap bounds how many unique blocked hosts trigger an OnEgressDeny
// callback, so a probing agent can't flood the host terminal. Every denial is
// still recorded in the broker log regardless of the cap.
const egressDenyWarnCap = 20

// handleEgressDeny records a hostname the in-container firewall reported as
// out-of-allowlist — blocked when enforcing, or observed-but-allowed during a
// learn pass. It logs every report (wording follows the mode) and invokes
// OnEgressDeny once per unique host (up to a cap).
func (b *HostBroker) handleEgressDeny(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	body, ok := b.readBody(w, r, maxEgressDenySize)
	if !ok {
		return
	}
	host := strings.TrimSpace(string(body))
	if host == "" {
		http.Error(w, "empty host", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	first := !b.deniedHosts[host]
	if first {
		if b.deniedHosts == nil {
			b.deniedHosts = make(map[string]bool)
		}
		b.deniedHosts[host] = true
	}
	count := len(b.deniedHosts)
	b.mu.Unlock()

	if first {
		if b.Learn {
			b.blog("EGRESS-OBSERVE -> %s (allowed, outside allowlist)", host)
		} else {
			b.blog("EGRESS-DENY -> %s (blocked, not in allowlist)", host)
		}
		if b.OnEgressDeny != nil && count <= egressDenyWarnCap {
			b.OnEgressDeny(host)
		} else if count == egressDenyWarnCap+1 {
			b.blog("EGRESS -> further host notices suppressed (%d+ unique); see mittens logs", egressDenyWarnCap)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ObservedHosts returns the sorted set of hostnames reported to /egress-deny
// during the run — blocked under enforcement, or observed-but-allowed under a
// firewall-learn pass.
func (b *HostBroker) ObservedHosts() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	hosts := make([]string, 0, len(b.deniedHosts))
	for h := range b.deniedHosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// interceptOAuthCallback starts a temporary HTTP server on the host at the
// given port to receive the OAuth browser redirect. When OnLoginForward is
// set, each request is proxied straight into the container's login server and
// the provider's own responses (including its success page) are relayed to
// the browser. Otherwise — or when forwarding fails — the full callback URL
// is stored for the container to pick up via GET /login-callback and a static
// success page is served.
func (b *HostBroker) interceptOAuthCallback(port int, ready chan struct{}) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		b.blog("OAuth intercept: failed to listen on :%d: %v", port, err)
		close(ready)
		return
	}
	b.blog("OAuth intercept: listening on :%d", port)
	close(ready)

	doneCh := make(chan string, 1)
	finish := func(msg string) {
		select {
		case doneCh <- msg:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if b.forwardLoginRequest(w, r, port, finish) {
			return
		}
		// Fallback: capture the callback for the container shim to replay.
		callbackURL := fmt.Sprintf("http://localhost:%d%s", port, r.URL.RequestURI())
		b.mu.Lock()
		b.pendingCallback = callbackURL
		b.mu.Unlock()
		finish("captured callback " + redactURL(callbackURL))
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, oauthSuccessPage)
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	select {
	case msg := <-doneCh:
		b.blog("OAuth intercept: %s", msg)
	case <-time.After(2 * time.Minute):
		b.blog("OAuth intercept: timeout")
	case <-b.done:
		b.blog("OAuth intercept: broker closing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// forwardLoginRequest proxies one browser request into the container's login
// server via OnLoginForward and relays the response. Returns false when no
// forwarder is configured or forwarding failed, so the caller can fall back
// to capture-and-replay. Signals finish once the provider sends a response
// that does not redirect back to the intercepted port, meaning the login flow
// has completed.
func (b *HostBroker) forwardLoginRequest(w http.ResponseWriter, r *http.Request, port int, finish func(string)) bool {
	if b.OnLoginForward == nil {
		return false
	}
	resp, err := b.OnLoginForward(port, r.URL.RequestURI())
	if err != nil {
		b.blog("OAuth forward: %s failed: %v (falling back to capture)", redactURL(r.URL.RequestURI()), err)
		return false
	}
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	if resp.Location != "" {
		w.Header().Set("Location", resp.Location)
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
	b.blog("OAuth forward: %s -> %d", redactURL(r.URL.RequestURI()), resp.Status)
	if !redirectsToPort(resp, port) {
		finish("forwarded login flow complete")
	}
	return true
}

// redirectsToPort reports whether resp redirects the browser back to the
// intercepted localhost port, meaning another request in the login flow is
// expected (e.g. Codex's callback redirecting to its local success page).
func redirectsToPort(resp *LoginForwardResponse, port int) bool {
	if resp.Status < 300 || resp.Status > 399 || resp.Location == "" {
		return false
	}
	u, err := url.Parse(resp.Location)
	if err != nil {
		return false
	}
	if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" {
		return false
	}
	p, err := strconv.Atoi(u.Port())
	return err == nil && p == port
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
