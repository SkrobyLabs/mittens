package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostOpenURLsRejectsUnsupportedAsk(t *testing.T) {
	p := defaultProjectPolicy()
	p.Host.OpenURLs = "ask"
	for _, err := range []error{
		p.Validate(),
		savePolicyFile(filepath.Join(t.TempDir(), "policy.yaml"), p),
		setPolicyField(defaultProjectPolicy(), "host.open_urls", "ask"),
	} {
		if err == nil || !strings.Contains(err.Error(), "ask is not supported") || !strings.Contains(err.Error(), "allow") || !strings.Contains(err.Error(), "deny") {
			t.Fatalf("got %v, want unsupported ask with allow/deny migration guidance", err)
		}
	}

	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte("version: 2\nhost:\n  open_urls: ask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPolicyFile(path); err == nil || !strings.Contains(err.Error(), "ask is not supported") {
		t.Fatalf("loadPolicyFile() = %v, want unsupported ask rejection", err)
	}
}

func TestHostOpenURLsPolicyMapping(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		valid   bool
		enabled bool
	}{
		{mode: "", valid: true, enabled: true},
		{mode: "allow", valid: true, enabled: true},
		{mode: "deny", valid: true},
		{mode: "ask"},
		{mode: "ALLOW"},
		{mode: "unexpected"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p := defaultProjectPolicy()
			p.Host.OpenURLs = tc.mode
			if err := p.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v, valid=%v", err, tc.valid)
			}
			// Defensive runtime mapping also denies unsupported values when an
			// embedded caller bypasses policy validation.
			a := &App{}
			a.applyProjectPolicy(p)
			a.ensureHostPolicyDefaults()
			if a.HostBridge.OpenURLs != tc.enabled {
				t.Fatalf("OpenURLs = %v, want %v", a.HostBridge.OpenURLs, tc.enabled)
			}
		})
	}
}
