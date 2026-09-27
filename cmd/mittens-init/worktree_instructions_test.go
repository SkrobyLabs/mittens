package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeInstructionsUseCurrentPersistentWorkspace(t *testing.T) {
	for _, projectFile := range []string{"CLAUDE.md", "AGENTS.md", "GEMINI.md"} {
		t.Run(projectFile, func(t *testing.T) {
			cfg := &config{AIDir: t.TempDir(), AIProjectFile: projectFile, HostWorkspace: filepath.Join(t.TempDir(), "repo with spaces")}
			path := filepath.Join(cfg.AIDir, projectFile)
			if err := os.WriteFile(path, []byte("Personal instructions.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			appendWorktreeInfo(cfg)
			cfg.HostWorkspace = filepath.Join(t.TempDir(), "next-workspace")
			resetGeneratedProjectInstructions(cfg)
			appendWorktreeInfo(cfg)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if !strings.Contains(text, filepath.Join(cfg.HostWorkspace, ".mittens-worktrees")) || strings.Contains(text, "repo with spaces") {
				t.Fatalf("instructions do not use the current workspace: %s", text)
			}
			if strings.Count(text, "# Persistent Git Worktrees") != 1 || !strings.Contains(text, "Personal instructions.") {
				t.Fatalf("generated guidance duplicated or personal instructions lost: %s", text)
			}
			if _, err := os.Stat(cfg.HostWorkspace); !os.IsNotExist(err) {
				t.Fatalf("instruction generation must not create workspace directories: %v", err)
			}
		})
	}
}

func TestWorktreeInstructionsSkipMissingWorkspace(t *testing.T) {
	cfg := &config{AIDir: t.TempDir(), AIProjectFile: "AGENTS.md"}
	appendWorktreeInfo(cfg)
	if _, err := os.Stat(filepath.Join(cfg.AIDir, cfg.AIProjectFile)); !os.IsNotExist(err) {
		t.Fatalf("unexpected instructions without a workspace: %v", err)
	}
}
