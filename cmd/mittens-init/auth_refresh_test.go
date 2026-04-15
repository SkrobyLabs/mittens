package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunAuthProbe_Success(t *testing.T) {
	binary := writeProbeScript(t, `#!/bin/bash
set -euo pipefail
cred="$HOME/.claude/.credentials.json"
sleep 0.1
cat > "$cred" <<'EOF'
{"expiresAt":200}
EOF
`)
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)

	origTimeout, origInterval := authProbeTimeout, authProbePollInterval
	authProbeTimeout = 2 * time.Second
	authProbePollInterval = 10 * time.Millisecond
	defer func() {
		authProbeTimeout = origTimeout
		authProbePollInterval = origInterval
	}()

	if !runAuthProbe(binary, credFile, nil) {
		t.Fatal("runAuthProbe() = false, want true")
	}
	if got := credExpiresAtFile(credFile); got != 200 {
		t.Fatalf("expiresAt = %d, want 200", got)
	}
}

func TestRunAuthProbe_Timeout(t *testing.T) {
	binary := writeProbeScript(t, `#!/bin/bash
set -euo pipefail
sleep 5
`)
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)

	origTimeout, origInterval := authProbeTimeout, authProbePollInterval
	authProbeTimeout = 100 * time.Millisecond
	authProbePollInterval = 10 * time.Millisecond
	defer func() {
		authProbeTimeout = origTimeout
		authProbePollInterval = origInterval
	}()

	if runAuthProbe(binary, credFile, nil) {
		t.Fatal("runAuthProbe() = true, want false")
	}
	if got := credExpiresAtFile(credFile); got != 100 {
		t.Fatalf("expiresAt = %d, want 100", got)
	}
}

func TestRunAuthProbe_NoCredentialChange(t *testing.T) {
	binary := writeProbeScript(t, `#!/bin/bash
set -euo pipefail
exit 0
`)
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)

	origTimeout, origInterval := authProbeTimeout, authProbePollInterval
	authProbeTimeout = 200 * time.Millisecond
	authProbePollInterval = 10 * time.Millisecond
	defer func() {
		authProbeTimeout = origTimeout
		authProbePollInterval = origInterval
	}()

	if runAuthProbe(binary, credFile, nil) {
		t.Fatal("runAuthProbe() = true, want false")
	}
}

func TestTriggerRefreshProbe_RestoresOriginalCredentialsOnFailure(t *testing.T) {
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)
	binary := writeProbeScript(t, `#!/bin/bash
set -euo pipefail
exit 0
`)

	if triggerRefreshProbe(binary, credFile, callbackEventLogger{}, nil) {
		t.Fatal("triggerRefreshProbe() = true, want false")
	}
	if got := credExpiresAtFile(credFile); got != 100 {
		t.Fatalf("expiresAt = %d, want restored 100", got)
	}
}

func TestAttemptAuthRefresh_Coordinator(t *testing.T) {
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)
	binary := writeProbeScript(t, `#!/bin/bash
set -euo pipefail
cred="$HOME/.claude/.credentials.json"
sleep 0.05
cat > "$cred" <<'EOF'
{"expiresAt":300}
EOF
`)

	var mu sync.Mutex
	var stored string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"action":"refresh"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/":
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			mu.Lock()
			stored = string(body)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	origTimeout, origPoll, origRetryTimeout, origRetryPoll := authProbeTimeout, authProbePollInterval, authRetryTimeout, authRetryPollInterval
	authProbeTimeout = 2 * time.Second
	authProbePollInterval = 10 * time.Millisecond
	authRetryTimeout = 500 * time.Millisecond
	authRetryPollInterval = 10 * time.Millisecond
	defer func() {
		authProbeTimeout = origTimeout
		authProbePollInterval = origPoll
		authRetryTimeout = origRetryTimeout
		authRetryPollInterval = origRetryPoll
	}()

	cfg := &config{
		AIBinary:     binary,
		AIDir:        filepath.Dir(credFile),
		AICredFile:   filepath.Base(credFile),
		ProviderName: "anthropic",
	}
	bc := &brokerClient{baseURL: srv.URL, httpClient: srv.Client()}

	if !attemptAuthRefresh(bc, cfg) {
		t.Fatal("attemptAuthRefresh() = false, want true")
	}
	if got := credExpiresAtFile(credFile); got != 300 {
		t.Fatalf("expiresAt = %d, want 300", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(stored, `"expiresAt":300`) {
		t.Fatalf("stored broker creds = %q, want refreshed creds", stored)
	}
}

func TestAttemptAuthRefresh_Waiter(t *testing.T) {
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)
	var (
		mu    sync.Mutex
		gets  int
		fresh = `{"expiresAt":250}`
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"action":"wait"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/":
			mu.Lock()
			gets++
			count := gets
			mu.Unlock()
			if count < 2 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fresh))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	origRetryTimeout, origRetryPoll := authRetryTimeout, authRetryPollInterval
	authRetryTimeout = 300 * time.Millisecond
	authRetryPollInterval = 10 * time.Millisecond
	defer func() {
		authRetryTimeout = origRetryTimeout
		authRetryPollInterval = origRetryPoll
	}()

	cfg := &config{
		AIBinary:     "claude",
		AIDir:        filepath.Dir(credFile),
		AICredFile:   filepath.Base(credFile),
		ProviderName: "anthropic",
	}
	bc := &brokerClient{baseURL: srv.URL, httpClient: srv.Client()}

	if !attemptAuthRefresh(bc, cfg) {
		t.Fatal("attemptAuthRefresh() = false, want true")
	}
	if got := credExpiresAtFile(credFile); got != 250 {
		t.Fatalf("expiresAt = %d, want 250", got)
	}
}

func TestAttemptAuthRefresh_Timeout(t *testing.T) {
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"action":"wait"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	origRetryTimeout, origRetryPoll := authRetryTimeout, authRetryPollInterval
	authRetryTimeout = 50 * time.Millisecond
	authRetryPollInterval = 10 * time.Millisecond
	defer func() {
		authRetryTimeout = origRetryTimeout
		authRetryPollInterval = origRetryPoll
	}()

	cfg := &config{
		AIBinary:     "claude",
		AIDir:        filepath.Dir(credFile),
		AICredFile:   filepath.Base(credFile),
		ProviderName: "anthropic",
	}
	bc := &brokerClient{baseURL: srv.URL, httpClient: srv.Client()}

	if attemptAuthRefresh(bc, cfg) {
		t.Fatal("attemptAuthRefresh() = true, want false")
	}
}

func TestAttemptAuthRefresh_PushFailureReturnsFalse(t *testing.T) {
	credFile := writeCredFile(t, filepath.Join(t.TempDir(), ".claude", ".credentials.json"), 100)
	binary := writeProbeScript(t, `#!/bin/bash
set -euo pipefail
cred="$HOME/.claude/.credentials.json"
sleep 0.05
cat > "$cred" <<'EOF'
{"expiresAt":300}
EOF
`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"action":"refresh"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	origTimeout, origPoll := authProbeTimeout, authProbePollInterval
	authProbeTimeout = 2 * time.Second
	authProbePollInterval = 10 * time.Millisecond
	defer func() {
		authProbeTimeout = origTimeout
		authProbePollInterval = origPoll
	}()

	cfg := &config{
		AIBinary:     binary,
		AIDir:        filepath.Dir(credFile),
		AICredFile:   filepath.Base(credFile),
		ProviderName: "anthropic",
	}
	bc := &brokerClient{baseURL: srv.URL, httpClient: srv.Client()}

	if attemptAuthRefresh(bc, cfg) {
		t.Fatal("attemptAuthRefresh() = true, want false")
	}
	if got := credExpiresAtFile(credFile); got != 300 {
		t.Fatalf("expiresAt = %d, want local refresh retained", got)
	}
}

func writeProbeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCredFile(t *testing.T, path string, expiresAt int64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]int64{"expiresAt": expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
