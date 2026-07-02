package main

import (
	"io"
	"net/http"
	"testing"
)

// TestExtractOAuthCallbackPort_NonProviderMCP verifies the generic OAuth
// callback interception (Resolved Q7) triggers for an arbitrary MCP remote
// server's auth URL, not just provider login — any localhost redirect_uri is
// tunneled.
func TestExtractOAuthCallbackPort_NonProviderMCP(t *testing.T) {
	url := "https://mcp.example.test/oauth/authorize?client_id=abc&redirect_uri=http%3A%2F%2Flocalhost%3A57219%2Fcallback&response_type=code"
	if port := extractOAuthCallbackPort(url); port != 57219 {
		t.Fatalf("expected callback port 57219 for MCP OAuth URL, got %d", port)
	}
}

// TestBroker_LoginCallbackReplaysMCPFlow verifies the container can retrieve a
// captured callback for a non-provider (MCP) OAuth flow via /login-callback,
// which the shim replays into the in-container listener.
func TestBroker_LoginCallbackReplaysMCPFlow(t *testing.T) {
	b, client := startBroker(t, "")
	captured := "http://localhost:57219/callback?code=mcp-code-xyz"
	b.mu.Lock()
	b.pendingCallback = captured
	b.mu.Unlock()

	resp, err := client.Get("http://broker/login-callback")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != captured {
		t.Fatalf("callback = %q, want %q", body, captured)
	}

	// The slot is single-use (v1 single-flow constraint): a second read is empty.
	resp2, err := client.Get("http://broker/login-callback")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("second read status = %d, want 204", resp2.StatusCode)
	}
}
