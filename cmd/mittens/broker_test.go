package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// brokerClient returns an *http.Client that dials the given Unix socket.
func brokerClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// shortSockPath creates a short Unix socket path under /tmp to stay within
// macOS's 104-byte sun_path limit. t.TempDir() paths are too long when
// combined with test names.
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mb-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "b.sock")
}

// startBroker creates and starts a broker in a background goroutine.
// Returns the broker and a cleanup function. The broker is ready to
// accept connections when this function returns.
func startBroker(t *testing.T, seed string) (*HostBroker, *http.Client) {
	t.Helper()
	sockPath := shortSockPath(t)
	b := NewHostBroker(sockPath, seed, nil)

	go func() { _ = b.Serve() }()

	// Poll until the broker is accepting connections.
	client := brokerClient(sockPath)
	waitForBroker(t, client)

	t.Cleanup(func() { _ = b.Close() })
	return b, client
}

func TestBroker_GET_Empty(t *testing.T) {
	_, client := startBroker(t, "")

	resp, err := client.Get("http://broker/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("GET empty broker: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestBroker_GET_WithSeed(t *testing.T) {
	seed := `{"accessToken":"tok","expiresAt":1700000000}`
	_, client := startBroker(t, seed)

	resp, err := client.Get("http://broker/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET seeded broker: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != seed {
		t.Errorf("GET body = %q, want %q", body, seed)
	}
}

func TestBroker_PUT_Fresher(t *testing.T) {
	seed := `{"accessToken":"old","expiresAt":100}`
	_, client := startBroker(t, seed)

	newer := `{"accessToken":"new","expiresAt":200}`
	resp, err := client.Do(putReq(newer))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("PUT fresher: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	// Verify GET returns the newer creds.
	resp, err = client.Get("http://broker/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != newer {
		t.Errorf("GET after PUT = %q, want %q", body, newer)
	}
}

func TestBroker_PUT_Stale(t *testing.T) {
	seed := `{"accessToken":"fresh","expiresAt":500}`
	_, client := startBroker(t, seed)

	stale := `{"accessToken":"stale","expiresAt":100}`
	resp, err := client.Do(putReq(stale))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("PUT stale: status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestBroker_PUT_InvalidJSON(t *testing.T) {
	_, client := startBroker(t, "")

	resp, err := client.Do(putReq("not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT invalid JSON: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestBroker_PUT_MissingExpiresAt(t *testing.T) {
	_, client := startBroker(t, "")

	resp, err := client.Do(putReq(`{"accessToken":"tok"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT missing expiresAt: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestBroker_ConcurrentPUTs(t *testing.T) {
	seed := `{"accessToken":"seed","expiresAt":0}`
	b, client := startBroker(t, seed)

	var wg sync.WaitGroup
	for i := 1; i <= 100; i++ {
		wg.Add(1)
		go func(exp int) {
			defer wg.Done()
			cred := `{"accessToken":"tok","expiresAt":` + itoa(exp) + `}`
			resp, err := client.Do(putReq(cred))
			if err != nil {
				return
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	// The highest expiresAt should win.
	final := b.Credentials()
	if exp := expiresAt(final); exp != 100 {
		t.Errorf("after concurrent PUTs: expiresAt = %d, want 100", exp)
	}
}

func TestBroker_Credentials(t *testing.T) {
	seed := `{"accessToken":"tok","expiresAt":42}`
	b, _ := startBroker(t, seed)

	got := b.Credentials()
	if got != seed {
		t.Errorf("Credentials() = %q, want %q", got, seed)
	}
}

func TestBroker_MethodNotAllowed(t *testing.T) {
	_, client := startBroker(t, "")

	req, _ := http.NewRequest(http.MethodDelete, "http://broker/", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if allow := resp.Header.Get("Allow"); allow != "GET, PUT" {
		t.Errorf("Allow header = %q, want %q", allow, "GET, PUT")
	}
}

func TestBroker_Lifecycle(t *testing.T) {
	sockPath := shortSockPath(t)
	b := NewHostBroker(sockPath, "", nil)

	errCh := make(chan error, 1)
	go func() { errCh <- b.Serve() }()

	// Wait for socket.
	client := brokerClient(sockPath)
	waitForBroker(t, client)

	// Close should return nil.
	if err := b.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}

	// Serve should return http.ErrServerClosed.
	if err := <-errCh; err != http.ErrServerClosed {
		t.Errorf("Serve() after Close = %v, want ErrServerClosed", err)
	}
}

func TestBroker_PUT_WritesThrough(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "creds.json")
	store := &FileCredentialStore{path: storePath}

	sockPath := shortSockPath(t)
	b := NewHostBroker(sockPath, `{"expiresAt":10}`, []CredentialStore{store})
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	client := brokerClient(sockPath)
	waitForBroker(t, client)

	// PUT fresher creds — should write-through to the store.
	newer := `{"accessToken":"refreshed","expiresAt":999}`
	resp, err := client.Do(putReq(newer))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != newer {
		t.Errorf("store file = %q, want %q", data, newer)
	}
}

func TestBroker_PullFromHost(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "creds.json")
	store := &FileCredentialStore{path: storePath}

	sockPath := shortSockPath(t)
	b := NewHostBroker(sockPath, `{"expiresAt":10}`, []CredentialStore{store})

	// Write fresher creds to the host store file.
	fresher := `{"accessToken":"host-refreshed","expiresAt":500}`
	os.WriteFile(storePath, []byte(fresher), 0600)

	// pullFromHost should pick it up.
	b.pullFromHost()

	got := b.Credentials()
	if got != fresher {
		t.Errorf("after pullFromHost: creds = %q, want %q", got, fresher)
	}
}

func TestBroker_PullFromHost_StaleIgnored(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "creds.json")
	store := &FileCredentialStore{path: storePath}

	sockPath := shortSockPath(t)
	seed := `{"accessToken":"current","expiresAt":500}`
	b := NewHostBroker(sockPath, seed, []CredentialStore{store})

	// Write staler creds to host store.
	os.WriteFile(storePath, []byte(`{"expiresAt":100}`), 0600)

	b.pullFromHost()

	got := b.Credentials()
	if got != seed {
		t.Errorf("pullFromHost should not downgrade: creds = %q, want %q", got, seed)
	}
}

// ---------------------------------------------------------------------------
// TCP mode tests
// ---------------------------------------------------------------------------

func TestBroker_TCP_GetPut(t *testing.T) {
	b := NewHostBroker("", `{"expiresAt":10}`, nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	// Use a regular HTTP client against the TCP port.
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}

	// GET should return the seed.
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"expiresAt":10`) {
		t.Errorf("GET body = %q, want seed", body)
	}

	// PUT fresher creds.
	newer := `{"accessToken":"tcp-new","expiresAt":999}`
	req, _ := http.NewRequest(http.MethodPut, base+"/", strings.NewReader(newer))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: status = %d, want 204", resp.StatusCode)
	}

	// GET should return the newer creds.
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != newer {
		t.Errorf("GET after PUT = %q, want %q", body, newer)
	}
}

func TestBroker_ListenTCP_BindsLoopback(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	addr, ok := b.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr type = %T, want *net.TCPAddr", b.ln.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener IP = %v, want loopback", addr.IP)
	}
	if addr.Port != port {
		t.Fatalf("listener port = %d, want %d", addr.Port, port)
	}
}

func TestBroker_TCP_RequiresAuthTokenWhenConfigured(t *testing.T) {
	b := NewHostBroker("", `{"expiresAt":10}`, nil)
	b.AuthToken = "test-token"
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET without token: status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("X-Mittens-Token", "wrong")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET with wrong token: status = %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("X-Mittens-Token", "test-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET with valid token: status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `"expiresAt":10`) {
		t.Fatalf("GET with valid token body = %q, want seed", body)
	}
}

func TestBroker_TCP_OpenEndpoint(t *testing.T) {
	var opened string
	var mu sync.Mutex

	b := NewHostBroker("", "", nil)
	b.OnOpen = func(url string) {
		mu.Lock()
		opened = url
		mu.Unlock()
	}

	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}

	// POST a URL to /open.
	testURL := "https://accounts.anthropic.com/authorize?code=abc123"
	req, _ := http.NewRequest(http.MethodPost, base+"/open", strings.NewReader(testURL))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST /open: status = %d, want 204", resp.StatusCode)
	}

	mu.Lock()
	if opened != testURL {
		t.Errorf("OnOpen called with %q, want %q", opened, testURL)
	}
	mu.Unlock()
}

func TestBroker_TCP_OpenRejectsNonHTTP(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}

	// POST a non-HTTP URL — should be rejected.
	req, _ := http.NewRequest(http.MethodPost, base+"/open", strings.NewReader("file:///etc/passwd"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /open with file:// URL: status = %d, want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Cross-broker sync via TCP (simulates two containers syncing through host stores)
// ---------------------------------------------------------------------------

func TestBroker_CrossBrokerSync(t *testing.T) {
	// This test simulates the full sync path:
	// Container A → PUT to Broker A → write-through to host store
	// Broker B → pullFromHost → picks up fresher creds from host store
	// Container B → GET from Broker B → gets the synced creds

	tmp := t.TempDir()
	sharedStorePath := filepath.Join(tmp, "shared-creds.json")
	storeA := &FileCredentialStore{path: sharedStorePath}
	storeB := &FileCredentialStore{path: sharedStorePath}

	tcpClient := &http.Client{Timeout: 2 * time.Second}

	// Start Broker A (for "container A") with no initial creds.
	brokerA := NewHostBroker("", "", []CredentialStore{storeA})
	portA, err := brokerA.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = brokerA.Serve() }()
	t.Cleanup(func() { _ = brokerA.Close() })
	baseA := fmt.Sprintf("http://127.0.0.1:%d", portA)
	waitForTCPBroker(t, tcpClient, baseA)

	// Start Broker B (for "container B") with no initial creds.
	brokerB := NewHostBroker("", "", []CredentialStore{storeB})
	portB, err := brokerB.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = brokerB.Serve() }()
	t.Cleanup(func() { _ = brokerB.Close() })
	baseB := fmt.Sprintf("http://127.0.0.1:%d", portB)
	waitForTCPBroker(t, tcpClient, baseB)

	// Container A does /login and gets fresh credentials.
	freshCreds := `{"accessToken":"fresh-from-login","expiresAt":9999}`
	req, _ := http.NewRequest(http.MethodPut, baseA+"/", strings.NewReader(freshCreds))
	req.Header.Set("Content-Type", "application/json")
	resp, err := tcpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT to broker A: status = %d, want 204", resp.StatusCode)
	}

	// Verify write-through: host store should have the creds.
	data, err := os.ReadFile(sharedStorePath)
	if err != nil {
		t.Fatalf("host store should have creds after PUT write-through: %v", err)
	}
	if string(data) != freshCreds {
		t.Errorf("host store = %q, want %q", data, freshCreds)
	}

	// Broker B polls host stores (simulating the 5s hostSync tick).
	brokerB.pullFromHost()

	// Container B should now get the fresh creds via GET.
	resp, err = tcpClient.Get(baseB + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET from broker B: status = %d, want 200", resp.StatusCode)
	}
	if string(body) != freshCreds {
		t.Errorf("broker B GET = %q, want %q", body, freshCreds)
	}
}

func TestBroker_CrossBrokerSync_BidirectionalFreshest(t *testing.T) {
	// Both containers get creds; the fresher one should win across all brokers.

	tmp := t.TempDir()
	sharedStorePath := filepath.Join(tmp, "shared-creds.json")
	store := &FileCredentialStore{path: sharedStorePath}

	tcpClient := &http.Client{Timeout: 2 * time.Second}

	brokerA := NewHostBroker("", "", []CredentialStore{store})
	portA, err := brokerA.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = brokerA.Serve() }()
	t.Cleanup(func() { _ = brokerA.Close() })
	baseA := fmt.Sprintf("http://127.0.0.1:%d", portA)
	waitForTCPBroker(t, tcpClient, baseA)

	brokerB := NewHostBroker("", "", []CredentialStore{store})
	portB, err := brokerB.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = brokerB.Serve() }()
	t.Cleanup(func() { _ = brokerB.Close() })
	baseB := fmt.Sprintf("http://127.0.0.1:%d", portB)
	waitForTCPBroker(t, tcpClient, baseB)

	// Container A pushes creds with expiresAt=100.
	credsA := `{"accessToken":"from-A","expiresAt":100}`
	req, _ := http.NewRequest(http.MethodPut, baseA+"/", strings.NewReader(credsA))
	req.Header.Set("Content-Type", "application/json")
	resp, err := tcpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Container B pushes creds with expiresAt=500 (fresher).
	credsB := `{"accessToken":"from-B","expiresAt":500}`
	req, _ = http.NewRequest(http.MethodPut, baseB+"/", strings.NewReader(credsB))
	req.Header.Set("Content-Type", "application/json")
	resp, err = tcpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Host store should have B's creds (fresher).
	data, _ := os.ReadFile(sharedStorePath)
	if string(data) != credsB {
		t.Errorf("host store = %q, want %q (B's fresher creds)", data, credsB)
	}

	// Broker A pulls from host — should get B's creds.
	brokerA.pullFromHost()
	got := brokerA.Credentials()
	if got != credsB {
		t.Errorf("broker A after pull = %q, want %q", got, credsB)
	}

	// Container A GETs — should get B's creds.
	resp, err = tcpClient.Get(baseA + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != credsB {
		t.Errorf("container A GET = %q, want %q", body, credsB)
	}
}

func TestBroker_HostSyncAutomatic(t *testing.T) {
	// Verify that pullFromHost picks up changes written to the host store.

	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "creds.json")
	store := &FileCredentialStore{path: storePath}

	sockPath := shortSockPath(t)
	b := NewHostBroker(sockPath, "", []CredentialStore{store})

	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	client := brokerClient(sockPath)
	waitForBroker(t, client)

	// Initially empty.
	resp, _ := client.Get("http://broker/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initial GET: status = %d, want 204", resp.StatusCode)
	}

	// Write creds directly to the host store file (simulating external refresh).
	freshCreds := `{"accessToken":"external","expiresAt":777}`
	os.WriteFile(storePath, []byte(freshCreds), 0600)

	// Manually trigger pullFromHost (the hostSync goroutine does this every 5s).
	b.pullFromHost()

	// GET should now return the fresh creds.
	resp, _ = client.Get("http://broker/")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after host write: status = %d, want 200", resp.StatusCode)
	}
	if string(body) != freshCreds {
		t.Errorf("GET body = %q, want %q", body, freshCreds)
	}
}

func TestBroker_TCP_WriteThroughAndPull(t *testing.T) {
	// Full TCP-mode test: PUT → write-through → pullFromHost on second broker.
	// This mirrors the actual production flow.

	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "host-creds.json")
	store := &FileCredentialStore{path: storePath}

	tcpClient := &http.Client{Timeout: 2 * time.Second}

	// Broker A (TCP mode).
	brokerA := NewHostBroker("", "", []CredentialStore{store})
	portA, err := brokerA.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = brokerA.Serve() }()
	t.Cleanup(func() { _ = brokerA.Close() })
	baseA := fmt.Sprintf("http://127.0.0.1:%d", portA)
	waitForTCPBroker(t, tcpClient, baseA)

	// Broker B (TCP mode) with same store.
	brokerB := NewHostBroker("", "", []CredentialStore{store})
	portB, err := brokerB.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = brokerB.Serve() }()
	t.Cleanup(func() { _ = brokerB.Close() })
	baseB := fmt.Sprintf("http://127.0.0.1:%d", portB)
	waitForTCPBroker(t, tcpClient, baseB)

	// PUT fresh creds to Broker A via TCP.
	freshCreds := `{"accessToken":"tcp-fresh","expiresAt":5000}`
	req, _ := http.NewRequest(http.MethodPut, baseA+"/", strings.NewReader(freshCreds))
	req.Header.Set("Content-Type", "application/json")
	resp, err := tcpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT to A: status = %d", resp.StatusCode)
	}

	// Verify host store has the creds.
	data, _ := os.ReadFile(storePath)
	if string(data) != freshCreds {
		t.Fatalf("host store = %q, want %q", data, freshCreds)
	}

	// Broker B pulls from host.
	brokerB.pullFromHost()

	// GET from Broker B via TCP.
	resp, err = tcpClient.Get(baseB + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET from B: status = %d", resp.StatusCode)
	}
	if string(body) != freshCreds {
		t.Errorf("broker B GET = %q, want %q", body, freshCreds)
	}
}

// ---------------------------------------------------------------------------
// OAuth callback interception tests
// ---------------------------------------------------------------------------

func TestExtractOAuthCallbackPort(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want int
	}{
		{
			name: "real OAuth URL",
			url:  "https://claude.ai/oauth/authorize?client_id=test&redirect_uri=http%3A%2F%2Flocalhost%3A35167%2Fcallback&state=abc",
			want: 35167,
		},
		{
			name: "no redirect_uri",
			url:  "https://claude.ai/oauth/authorize?client_id=test&state=abc",
			want: 0,
		},
		{
			name: "non-localhost redirect",
			url:  "https://claude.ai/oauth/authorize?redirect_uri=https%3A%2F%2Fexample.com%2Fcallback",
			want: 0,
		},
		{
			name: "regular URL",
			url:  "https://example.com/page",
			want: 0,
		},
		{
			name: "localhost without port",
			url:  "https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%2Fcallback",
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOAuthCallbackPort(tc.url)
			if got != tc.want {
				t.Errorf("extractOAuthCallbackPort(%q) = %d, want %d", tc.url, got, tc.want)
			}
		})
	}
}

func TestBroker_OAuthCallbackIntercept(t *testing.T) {
	// Find a free port for the simulated callback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	callbackPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // free the port for the broker's interceptor

	var openedURL string
	b := NewHostBroker("", "", nil)
	b.OnOpen = func(u string) { openedURL = u }

	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}
	waitForTCPBroker(t, client, base)

	// POST an OAuth URL with a localhost redirect_uri.
	oauthURL := fmt.Sprintf(
		"https://claude.ai/oauth/authorize?client_id=test&redirect_uri=%s&state=teststate",
		url.QueryEscape(fmt.Sprintf("http://localhost:%d/callback", callbackPort)),
	)
	req, _ := http.NewRequest(http.MethodPost, base+"/open", strings.NewReader(oauthURL))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// OAuth URLs now return 200 with a JSON body containing the callbackID.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /open (OAuth): status = %d, body = %q", resp.StatusCode, body)
	}
	var openResp struct {
		CallbackID string `json:"callbackID"`
	}
	if err := json.Unmarshal(body, &openResp); err != nil || openResp.CallbackID == "" {
		t.Fatalf("POST /open: expected JSON {callbackID}, got %q (err: %v)", body, err)
	}

	// OnOpen should have been called with the original URL.
	if openedURL != oauthURL {
		t.Errorf("OnOpen URL = %q, want %q", openedURL, oauthURL)
	}

	// Start awaiting the callback in a goroutine — this blocks until the browser hits the port.
	type awaitResult struct {
		body string
		code int
		err  error
	}
	awaitCh := make(chan awaitResult, 1)
	awaitClient := &http.Client{Timeout: 10 * time.Second}
	go func() {
		r, awaitErr := awaitClient.Get(base + "/await-callback/" + openResp.CallbackID)
		if awaitErr != nil {
			awaitCh <- awaitResult{err: awaitErr}
			return
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		awaitCh <- awaitResult{body: string(b), code: r.StatusCode}
	}()

	// Simulate browser redirect hitting the intercepted callback port.
	callbackReq := fmt.Sprintf("http://127.0.0.1:%d/callback?code=TESTCODE&state=teststate", callbackPort)
	resp, err = client.Get(callbackReq)
	if err != nil {
		t.Fatalf("browser callback: %v", err)
	}
	closePage, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browser callback: status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(closePage), "Login complete") {
		t.Errorf("close page missing expected text, got %q", closePage)
	}

	// The await-callback goroutine should now have the URL.
	result := <-awaitCh
	if result.err != nil {
		t.Fatalf("GET /await-callback: %v", result.err)
	}
	if result.code != http.StatusOK {
		t.Fatalf("GET /await-callback: status = %d", result.code)
	}
	want := fmt.Sprintf("http://localhost:%d/callback?code=TESTCODE&state=teststate", callbackPort)
	if result.body != want {
		t.Errorf("await-callback = %q, want %q", result.body, want)
	}

	// A second call with the same ID should return 404 (consumed and deleted).
	resp, err = client.Get(base + "/await-callback/" + openResp.CallbackID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("second GET /await-callback: status = %d, want 404", resp.StatusCode)
	}
}

func TestBroker_LoginCallbackEmpty(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// /login-callback is now a stub — always returns 204.
	resp, err := client.Get(base + "/login-callback")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("GET /login-callback: status = %d, want 204", resp.StatusCode)
	}
}

func TestBroker_OpenNonOAuthStillWorks(t *testing.T) {
	var openedURL string
	b := NewHostBroker("", "", nil)
	b.OnOpen = func(u string) { openedURL = u }

	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// POST a regular (non-OAuth) URL — should return 204 with no callbackID.
	regularURL := "https://docs.anthropic.com/en/docs"
	req, _ := http.NewRequest(http.MethodPost, base+"/open", strings.NewReader(regularURL))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /open regular URL: status = %d", resp.StatusCode)
	}
	if len(body) > 0 {
		t.Errorf("POST /open regular URL: unexpected body %q", body)
	}
	if openedURL != regularURL {
		t.Errorf("OnOpen = %q, want %q", openedURL, regularURL)
	}
}

func TestBroker_AwaitCallbackUnknownID(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	resp, err := client.Get(base + "/await-callback/nonexistent-id")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /await-callback/nonexistent: status = %d, want 404", resp.StatusCode)
	}
}

func TestBroker_ConcurrentOAuth(t *testing.T) {
	// Two OAuth flows initiated concurrently — each container gets only its own callback.

	// Find two free ports for the simulated callbacks.
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port1 := ln1.Addr().(*net.TCPAddr).Port
	ln1.Close()

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port2 := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	b := NewHostBroker("", "", nil)
	brokerPort, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", brokerPort)
	client := &http.Client{Timeout: 5 * time.Second}
	waitForTCPBroker(t, client, base)

	makeOAuthURL := func(cbPort int) string {
		return fmt.Sprintf(
			"https://claude.ai/oauth/authorize?client_id=test&redirect_uri=%s&state=s%d",
			url.QueryEscape(fmt.Sprintf("http://localhost:%d/callback", cbPort)), cbPort,
		)
	}

	// POST both OAuth URLs and collect their callback IDs.
	getCallbackID := func(oauthURL string) string {
		req, _ := http.NewRequest(http.MethodPost, base+"/open", strings.NewReader(oauthURL))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /open: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /open: status = %d, body = %q", resp.StatusCode, body)
		}
		var r struct {
			CallbackID string `json:"callbackID"`
		}
		if err := json.Unmarshal(body, &r); err != nil || r.CallbackID == "" {
			t.Fatalf("POST /open: bad JSON %q: %v", body, err)
		}
		return r.CallbackID
	}

	id1 := getCallbackID(makeOAuthURL(port1))
	id2 := getCallbackID(makeOAuthURL(port2))

	if id1 == id2 {
		t.Fatal("two OAuth flows got the same callbackID")
	}

	awaitClient := &http.Client{Timeout: 10 * time.Second}

	type awaitResult struct {
		body string
		code int
		err  error
	}

	await := func(id string) chan awaitResult {
		ch := make(chan awaitResult, 1)
		go func() {
			resp, err := awaitClient.Get(base + "/await-callback/" + id)
			if err != nil {
				ch <- awaitResult{err: err}
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			ch <- awaitResult{body: string(body), code: resp.StatusCode}
		}()
		return ch
	}

	ch1 := await(id1)
	ch2 := await(id2)

	// Simulate browser callbacks in reverse order to prove isolation.
	cb2 := fmt.Sprintf("http://127.0.0.1:%d/callback?code=CODE2&state=s%d", port2, port2)
	resp, err := client.Get(cb2)
	if err != nil {
		t.Fatalf("browser callback 2: %v", err)
	}
	resp.Body.Close()

	cb1 := fmt.Sprintf("http://127.0.0.1:%d/callback?code=CODE1&state=s%d", port1, port1)
	resp, err = client.Get(cb1)
	if err != nil {
		t.Fatalf("browser callback 1: %v", err)
	}
	resp.Body.Close()

	r1 := <-ch1
	r2 := <-ch2

	if r1.err != nil {
		t.Fatalf("await id1: %v", r1.err)
	}
	if r2.err != nil {
		t.Fatalf("await id2: %v", r2.err)
	}

	want1 := fmt.Sprintf("http://localhost:%d/callback?code=CODE1&state=s%d", port1, port1)
	want2 := fmt.Sprintf("http://localhost:%d/callback?code=CODE2&state=s%d", port2, port2)

	if r1.body != want1 {
		t.Errorf("container1 got %q, want %q", r1.body, want1)
	}
	if r2.body != want2 {
		t.Errorf("container2 got %q, want %q", r2.body, want2)
	}
}

// ---------------------------------------------------------------------------
// /notify endpoint tests
// ---------------------------------------------------------------------------

func TestBroker_TCP_NotifyEndpoint(t *testing.T) {
	var mu sync.Mutex
	var gotContainer, gotEvent string

	b := NewHostBroker("", "", nil)
	b.OnNotify = func(container, event, _ string) {
		mu.Lock()
		gotContainer = container
		gotEvent = event
		mu.Unlock()
	}

	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// POST valid notification.
	payload := `{"container":"mittens-42","event":"stop","message":""}`
	req, _ := http.NewRequest(http.MethodPost, base+"/notify", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST /notify: status = %d, want 204", resp.StatusCode)
	}

	mu.Lock()
	if gotContainer != "mittens-42" {
		t.Errorf("container = %q, want %q", gotContainer, "mittens-42")
	}
	if gotEvent != "stop" {
		t.Errorf("event = %q, want %q", gotEvent, "stop")
	}
	mu.Unlock()
}

func TestBroker_TCP_NotifyWithMessage(t *testing.T) {
	var mu sync.Mutex
	var gotMessage string

	b := NewHostBroker("", "", nil)
	b.OnNotify = func(_, _, message string) {
		mu.Lock()
		gotMessage = message
		mu.Unlock()
	}

	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	payload := `{"container":"mittens-99","event":"notification","message":"needs your attention"}`
	req, _ := http.NewRequest(http.MethodPost, base+"/notify", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST /notify: status = %d, want 204", resp.StatusCode)
	}

	mu.Lock()
	if gotMessage != "needs your attention" {
		t.Errorf("message = %q, want %q", gotMessage, "needs your attention")
	}
	mu.Unlock()
}

func TestBroker_TCP_NotifyRejectsGET(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	resp, err := client.Get(base + "/notify")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /notify: status = %d, want 405", resp.StatusCode)
	}
}

func TestBroker_TCP_NotifyRejectsOversized(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// Send a payload larger than maxNotifySize (4096).
	oversized := strings.Repeat("x", 5000)
	req, _ := http.NewRequest(http.MethodPost, base+"/notify", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized POST /notify: status = %d, want 413", resp.StatusCode)
	}
}

func TestBroker_TCP_NotifyRejectsInvalidJSON(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	req, _ := http.NewRequest(http.MethodPost, base+"/notify", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid JSON POST /notify: status = %d, want 400", resp.StatusCode)
	}
}

func TestBroker_TCP_NotifySpecialCharacters(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{"double quotes", `She said "hello"`},
		{"backslashes", `path\to\file`},
		{"mixed special", `He said "it's a \"test\"" with \n newlines`},
		{"no extraneous quotes", "needs attention"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotMessage string

			b := NewHostBroker("", "", nil)
			b.OnNotify = func(_, _, message string) {
				mu.Lock()
				gotMessage = message
				mu.Unlock()
			}

			port, err := b.ListenTCP()
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = b.Serve() }()
			t.Cleanup(func() { _ = b.Close() })

			base := fmt.Sprintf("http://127.0.0.1:%d", port)
			client := &http.Client{Timeout: 2 * time.Second}
			waitForTCPBroker(t, client, base)

			// Use json.Marshal to build valid JSON regardless of special chars.
			type notifyPayload struct {
				Container string `json:"container"`
				Event     string `json:"event"`
				Message   string `json:"message"`
			}
			data, err := json.Marshal(notifyPayload{
				Container: "mittens-special",
				Event:     "notification",
				Message:   tc.message,
			})
			if err != nil {
				t.Fatal(err)
			}

			req, _ := http.NewRequest(http.MethodPost, base+"/notify", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("POST /notify: status = %d, want 204", resp.StatusCode)
			}

			mu.Lock()
			if gotMessage != tc.message {
				t.Errorf("message = %q, want %q", gotMessage, tc.message)
			}
			// Verify no extraneous single-quote wrapping.
			if len(gotMessage) > 0 && gotMessage[0] == '\'' && gotMessage[len(gotMessage)-1] == '\'' {
				t.Errorf("message has extraneous single-quote wrapping: %q", gotMessage)
			}
			mu.Unlock()
		})
	}
}

// ---------------------------------------------------------------------------
// /refresh endpoint tests
// ---------------------------------------------------------------------------

func TestBroker_RefreshFirstWins(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// First POST — should become coordinator.
	resp, err := client.Post(base+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /refresh first: status = %d, want 200", resp.StatusCode)
	}
	var p struct{ Action string }
	json.Unmarshal(body, &p)
	if p.Action != "refresh" {
		t.Errorf("first POST /refresh: action = %q, want \"refresh\"", p.Action)
	}

	// Second POST — should wait.
	resp, err = client.Post(base+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(body, &p)
	if p.Action != "wait" {
		t.Errorf("second POST /refresh: action = %q, want \"wait\"", p.Action)
	}
}

func TestBroker_RefreshExpiresAfterTimeout(t *testing.T) {
	b := NewHostBroker("", "", nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// Appoint a coordinator.
	resp, err := client.Post(base+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Backdate the deadline to simulate expiry.
	b.refreshMu.Lock()
	b.refreshDeadline = time.Now().Add(-time.Second)
	b.refreshMu.Unlock()

	// Next POST should become coordinator again.
	resp, err = client.Post(base+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var p struct{ Action string }
	json.Unmarshal(body, &p)
	if p.Action != "refresh" {
		t.Errorf("POST /refresh after timeout: action = %q, want \"refresh\"", p.Action)
	}
}

func TestBroker_RefreshResetsOnFreshCreds(t *testing.T) {
	b := NewHostBroker("", `{"expiresAt":10}`, nil)
	port, err := b.ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	waitForTCPBroker(t, client, base)

	// Appoint a coordinator.
	resp, err := client.Post(base+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// PUT fresher credentials — should reset the refresh lock.
	req, _ := http.NewRequest(http.MethodPut, base+"/", strings.NewReader(`{"accessToken":"new","expiresAt":9999}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT fresher: status = %d, want 204", resp.StatusCode)
	}

	// POST /refresh should get "refresh" again (lock was reset).
	resp, err = client.Post(base+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var p struct{ Action string }
	json.Unmarshal(body, &p)
	if p.Action != "refresh" {
		t.Errorf("POST /refresh after cred reset: action = %q, want \"refresh\"", p.Action)
	}
}

// ---------------------------------------------------------------------------
// jsonKeys / redactURL tests
// ---------------------------------------------------------------------------

func TestJsonKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"sorted keys", `{"b":1,"a":2,"c":3}`, "[a, b, c]"},
		{"nested creds", `{"claudeAiOauth":{"expiresAt":100},"primaryApiKey":"sk"}`, "[claudeAiOauth, primaryApiKey]"},
		{"empty object", `{}`, "[]"},
		{"invalid JSON", `not json`, "<invalid JSON>"},
		{"empty string", ``, "<invalid JSON>"},
		{"array", `[1,2,3]`, "<invalid JSON>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonKeys(tc.input)
			if got != tc.want {
				t.Errorf("jsonKeys(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			"code param",
			"http://localhost:42415/callback?code=SECRET&state=abc",
			"http://localhost:42415/callback?code=REDACTED&state=abc",
		},
		{
			"access_token param",
			"https://example.com/cb?access_token=tok123&type=bearer",
			"https://example.com/cb?access_token=REDACTED&type=bearer",
		},
		{
			"multiple sensitive params",
			"https://example.com/cb?code=C&token=T&refresh_token=R&ok=yes",
			"https://example.com/cb?code=REDACTED&ok=yes&refresh_token=REDACTED&token=REDACTED",
		},
		{
			"no sensitive params",
			"https://example.com/page?q=search&page=1",
			"https://example.com/page?q=search&page=1",
		},
		{
			"no query string",
			"https://docs.anthropic.com/en/docs",
			"https://docs.anthropic.com/en/docs",
		},
		{
			"invalid URL passthrough",
			"://not-a-url",
			"://not-a-url",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURL(tc.url)
			if got != tc.want {
				t.Errorf("redactURL(%q)\n  got  %q\n  want %q", tc.url, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func waitForBroker(t *testing.T, client *http.Client) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://broker/")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("broker did not start in time")
}

func waitForTCPBroker(t *testing.T, client *http.Client, base string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("TCP broker did not start in time")
}

func putReq(body string) *http.Request {
	req, _ := http.NewRequest(http.MethodPut, "http://broker/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
