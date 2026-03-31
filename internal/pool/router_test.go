package pool

import (
	"path/filepath"
	"testing"
)

func TestResolve_ExactMatch(t *testing.T) {
	routes := map[string]ModelConfig{
		"planner":     {Provider: "anthropic", Model: "claude-opus-4-6", Adapter: "claude-code"},
		"implementer": {Provider: "openai", Model: "gpt-5.3-spark", Adapter: "openai-codex"},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("planner")
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Errorf("Resolve(planner) = %+v, want anthropic/claude-opus-4-6", got)
	}

	got = r.Resolve("implementer")
	if got.Provider != "openai" || got.Model != "gpt-5.3-spark" {
		t.Errorf("Resolve(implementer) = %+v, want openai/gpt-5.3-spark", got)
	}
}

func TestResolve_FallbackToDefault(t *testing.T) {
	routes := map[string]ModelConfig{
		"planner": {Provider: "anthropic", Model: "claude-opus-4-6"},
		"default": {Provider: "anthropic", Model: "claude-sonnet-4-6"},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("unknown-role")
	if got.Provider != "anthropic" || got.Model != "claude-sonnet-4-6" {
		t.Errorf("Resolve(unknown-role) = %+v, want default anthropic/claude-sonnet-4-6", got)
	}
}

func TestResolve_NoDefault(t *testing.T) {
	routes := map[string]ModelConfig{
		"planner": {Provider: "anthropic", Model: "claude-opus-4-6"},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("unknown-role")
	if got.Provider != "" || got.Model != "" {
		t.Errorf("Resolve(unknown-role) = %+v, want zero-value", got)
	}
}

func TestResolve_EmptyRouter(t *testing.T) {
	r := NewModelRouter(map[string]ModelConfig{})
	got := r.Resolve("anything")
	if got.Provider != "" || got.Model != "" {
		t.Errorf("Resolve(anything) on empty router = %+v, want zero-value", got)
	}
}

func TestNewModelRouter_NilMap(t *testing.T) {
	r := NewModelRouter(nil)
	if r == nil {
		t.Fatal("NewModelRouter(nil) returned nil")
	}
	got := r.Resolve("anything")
	if got.Provider != "" || got.Model != "" {
		t.Errorf("Resolve on nil-map router = %+v, want zero-value", got)
	}
}

func TestResolve_DefaultNotInRoutes(t *testing.T) {
	routes := map[string]ModelConfig{
		"planner": {Provider: "anthropic", Model: "opus"},
		"default": {Provider: "anthropic", Model: "sonnet"},
	}
	r := NewModelRouter(routes)

	// "default" should not be resolvable as a role name — it's only the fallback.
	got := r.Resolve("default")
	// Since "default" is removed from routes and stored as fallback,
	// resolving "default" should return the fallback itself.
	if got.Model != "sonnet" {
		t.Errorf("Resolve(default) = %+v, want fallback sonnet", got)
	}
}

func TestResolveModel_NilRouter(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()

	pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
	got := pm.ResolveModel("planner")
	if got.Provider != "" || got.Model != "" {
		t.Errorf("ResolveModel with nil router = %+v, want zero-value", got)
	}
}

func TestResolveModel_WithRouter(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()

	router := NewModelRouter(map[string]ModelConfig{
		"planner": {Provider: "anthropic", Model: "opus"},
	})
	pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir, Router: router}, wal, nil)

	got := pm.ResolveModel("planner")
	if got.Provider != "anthropic" || got.Model != "opus" {
		t.Errorf("ResolveModel(planner) = %+v, want anthropic/opus", got)
	}
}

func TestResolve_WithFlags(t *testing.T) {
	routes := map[string]ModelConfig{
		"implementer": {
			Provider: "openai",
			Model:    "gpt-5.3",
			Flags:    map[string]string{"temperature": "0.2"},
		},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("implementer")
	if got.Flags["temperature"] != "0.2" {
		t.Errorf("Resolve flags = %v, want temperature=0.2", got.Flags)
	}
}

func TestResolve_ExactMatchOverFallback(t *testing.T) {
	routes := map[string]ModelConfig{
		"planner": {Provider: "anthropic", Model: "opus"},
		"default": {Provider: "fallback", Model: "fallback-model"},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("planner")
	if got.Provider != "anthropic" || got.Model != "opus" {
		t.Errorf("exact match should win over fallback, got %+v", got)
	}
}

func TestResolve_AllFieldsPreserved(t *testing.T) {
	routes := map[string]ModelConfig{
		"worker": {
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
			Adapter:  "claude",
			APIKey:   "sk-test",
			Flags:    map[string]string{"max-tokens": "4096", "temperature": "0.7"},
		},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("worker")
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", got.Provider, "anthropic")
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-sonnet-4-6")
	}
	if got.Adapter != "claude" {
		t.Errorf("Adapter = %q, want %q", got.Adapter, "claude")
	}
	if got.APIKey != "sk-test" {
		t.Errorf("APIKey = %q, want %q", got.APIKey, "sk-test")
	}
	if len(got.Flags) != 2 || got.Flags["max-tokens"] != "4096" || got.Flags["temperature"] != "0.7" {
		t.Errorf("Flags = %v, want map[max-tokens:4096 temperature:0.7]", got.Flags)
	}
}

func TestResolve_CaseSensitive(t *testing.T) {
	routes := map[string]ModelConfig{
		"Planner": {Provider: "upper", Model: "upper-model"},
		"planner": {Provider: "lower", Model: "lower-model"},
	}
	r := NewModelRouter(routes)

	got := r.Resolve("Planner")
	if got.Provider != "upper" {
		t.Errorf("Resolve(Planner) = %+v, want provider=upper", got)
	}

	got = r.Resolve("planner")
	if got.Provider != "lower" {
		t.Errorf("Resolve(planner) = %+v, want provider=lower", got)
	}

	got = r.Resolve("PLANNER")
	if got.Provider != "" || got.Model != "" {
		t.Errorf("Resolve(PLANNER) should be zero-value, got %+v", got)
	}
}
