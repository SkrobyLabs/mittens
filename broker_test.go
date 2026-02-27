package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
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

// startBroker creates and starts a broker in a background goroutine.
// Returns the broker and a cleanup function. The broker is ready to
// accept connections when this function returns.
func startBroker(t *testing.T, seed string) (*CredentialBroker, *http.Client) {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "broker.sock")
	b := NewCredentialBroker(sockPath, seed)

	go func() { _ = b.Serve() }()

	// Poll until the broker is accepting connections.
	client := brokerClient(sockPath)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://broker/")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

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
	sockPath := filepath.Join(t.TempDir(), "broker.sock")
	b := NewCredentialBroker(sockPath, "")

	errCh := make(chan error, 1)
	go func() { errCh <- b.Serve() }()

	// Wait for socket.
	client := brokerClient(sockPath)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://broker/")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Close should return nil.
	if err := b.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}

	// Serve should return http.ErrServerClosed.
	if err := <-errCh; err != http.ErrServerClosed {
		t.Errorf("Serve() after Close = %v, want ErrServerClosed", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func putReq(body string) *http.Request {
	req, _ := http.NewRequest(http.MethodPut, "http://broker/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
