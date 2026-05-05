package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
		b.mu.Lock()
		b.pendingCallback = callbackURL
		b.mu.Unlock()
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
		b.blog("OAuth intercept: captured callback %s", redactURL(cb))
	case <-time.After(2 * time.Minute):
		b.blog("OAuth intercept: timeout")
	case <-b.done:
		b.blog("OAuth intercept: broker closing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
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
