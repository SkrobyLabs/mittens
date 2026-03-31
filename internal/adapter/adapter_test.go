package adapter

import (
	"testing"
)

func TestNew_ClaudeCode(t *testing.T) {
	a, err := New("claude-code", "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
}

func TestNew_Default(t *testing.T) {
	a, err := New("", "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil adapter for empty name")
	}
}

func TestNew_Codex(t *testing.T) {
	a, err := New("openai-codex", "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
	if _, ok := a.(*codexAdapter); !ok {
		t.Fatalf("expected *codexAdapter, got %T", a)
	}
}

func TestNew_Unknown(t *testing.T) {
	_, err := New("nonexistent", "/tmp")
	if err == nil {
		t.Fatal("expected error for unknown adapter")
	}
}

func TestNew_WithSkipPermsFlag(t *testing.T) {
	a, err := New("claude-code", "/tmp", func(c *Config) {
		c.SkipPermsFlag = "--dangerously-skip-permissions"
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ca, ok := a.(*claudeAdapter)
	if !ok {
		t.Fatal("expected *claudeAdapter")
	}
	if ca.skipPermsFlag != "--dangerously-skip-permissions" {
		t.Errorf("skipPermsFlag = %q, want --dangerously-skip-permissions", ca.skipPermsFlag)
	}
}

func TestNew_WithModel(t *testing.T) {
	a, err := New("claude-code", "/tmp", func(c *Config) {
		c.Model = "claude-sonnet-4-6"
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ca, ok := a.(*claudeAdapter)
	if !ok {
		t.Fatal("expected *claudeAdapter")
	}
	if ca.model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", ca.model)
	}
}

func TestClaudeAdapter_Healthy(t *testing.T) {
	a := &claudeAdapter{workDir: "/tmp"}
	// Just verify the method doesn't panic; actual result depends on PATH.
	_ = a.Healthy()
}

func TestDefaultAdapterForProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"", "claude-code"},
		{"claude", "claude-code"},
		{"anthropic", "claude-code"},
		{"codex", "openai-codex"},
		{"openai", "openai-codex"},
		{"gemini", "gemini-cli"},
		{"unknown", ""},
	}

	for _, tc := range tests {
		if got := DefaultAdapterForProvider(tc.provider); got != tc.want {
			t.Errorf("DefaultAdapterForProvider(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}
