package pool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTeamConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "team.yaml")
	content := `models:
  planner:
    provider: anthropic
    model: claude-opus-4-6
    adapter: claude-code
  implementer:
    provider: openai
    model: gpt-5.3-spark
    adapter: openai-codex
    flags:
      temperature: "0.2"
  default:
    provider: anthropic
    model: claude-sonnet-4-6
    adapter: claude-code
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadTeamConfig(path)
	if err != nil {
		t.Fatalf("LoadTeamConfig: %v", err)
	}

	if len(cfg.Models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(cfg.Models))
	}
	if cfg.Models["planner"].Provider != "anthropic" {
		t.Errorf("planner provider = %q, want anthropic", cfg.Models["planner"].Provider)
	}
	if cfg.Models["implementer"].Flags["temperature"] != "0.2" {
		t.Errorf("implementer flags = %v, want temperature=0.2", cfg.Models["implementer"].Flags)
	}
	if cfg.Models["default"].Model != "claude-sonnet-4-6" {
		t.Errorf("default model = %q, want claude-sonnet-4-6", cfg.Models["default"].Model)
	}
}

func TestLoadTeamConfig_FileNotExist(t *testing.T) {
	cfg, err := LoadTeamConfig("/nonexistent/team.yaml")
	if err != nil {
		t.Fatalf("LoadTeamConfig: %v", err)
	}
	if cfg.Models != nil {
		t.Errorf("expected nil Models for missing file, got %v", cfg.Models)
	}
}

func TestLoadTeamConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "team.yaml")
	if err := os.WriteFile(path, []byte("models: [invalid: yaml: {{{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadTeamConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadTeamConfig_Minimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "team.yaml")
	content := `models:
  default:
    provider: anthropic
    model: claude-sonnet-4-6
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadTeamConfig(path)
	if err != nil {
		t.Fatalf("LoadTeamConfig: %v", err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(cfg.Models))
	}
	if cfg.Models["default"].Provider != "anthropic" {
		t.Errorf("default provider = %q, want anthropic", cfg.Models["default"].Provider)
	}
}

func TestDefaultTeamConfigPath(t *testing.T) {
	got := DefaultTeamConfigPath("/tmp/state")
	want := filepath.Join("/tmp/state", "team.yaml")
	if got != want {
		t.Errorf("DefaultTeamConfigPath = %q, want %q", got, want)
	}
}
