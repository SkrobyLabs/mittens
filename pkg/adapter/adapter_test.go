package adapter

import (
	"strings"
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

func TestNew_WithOnActivity(t *testing.T) {
	callback := func(Activity) {}

	a, err := New("claude-code", "/tmp", func(c *Config) {
		c.OnActivity = callback
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ca, ok := a.(*claudeAdapter)
	if !ok {
		t.Fatal("expected *claudeAdapter")
	}
	if ca.onActivity == nil {
		t.Fatal("expected onActivity callback to be wired")
	}

	a, err = New("openai-codex", "/tmp", func(c *Config) {
		c.OnActivity = callback
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	xa, ok := a.(*codexAdapter)
	if !ok {
		t.Fatal("expected *codexAdapter")
	}
	if xa.onActivity == nil {
		t.Fatal("expected onActivity callback to be wired")
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

func TestEmitActivity_CallsBothCallbacksForToolStart(t *testing.T) {
	var gotActivity Activity
	var toolName, summary string

	emitActivity(func(activity Activity) {
		gotActivity = activity
	}, func(name, inputSummary string) {
		toolName = name
		summary = inputSummary
	}, Activity{
		Kind:    ActivityKindTool,
		Phase:   ActivityPhaseStarted,
		Name:    "Read",
		Summary: "/tmp/file.go",
	})

	if gotActivity.Name != "Read" || gotActivity.Summary != "/tmp/file.go" {
		t.Fatalf("unexpected activity: %+v", gotActivity)
	}
	if toolName != "Read" || summary != "/tmp/file.go" {
		t.Fatalf("legacy callback = (%q, %q), want (%q, %q)", toolName, summary, "Read", "/tmp/file.go")
	}
}

func TestEmitActivity_LegacyToolCallbackOnlySeesToolStarts(t *testing.T) {
	var legacyCalls int

	emitActivity(nil, func(string, string) {
		legacyCalls++
	}, Activity{
		Kind:    ActivityKindTool,
		Phase:   ActivityPhaseCompleted,
		Name:    "Read",
		Summary: "/tmp/file.go",
	})
	emitActivity(nil, func(string, string) {
		legacyCalls++
	}, Activity{
		Kind:    ActivityKindStatus,
		Phase:   ActivityPhaseCompleted,
		Name:    "response",
		Summary: "done",
	})

	if legacyCalls != 0 {
		t.Fatalf("legacy callback called %d times, want 0", legacyCalls)
	}
}

func TestShortSummary(t *testing.T) {
	if got := shortSummary("  hello\n\nworld  "); got != "hello world" {
		t.Fatalf("shortSummary() = %q, want %q", got, "hello world")
	}

	text := strings.Repeat("x", 130)
	got := shortSummary(text)
	if len(got) > 123 {
		t.Fatalf("shortSummary() length = %d, want <= 123", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("shortSummary() = %q, want ellipsis suffix", got)
	}
}
