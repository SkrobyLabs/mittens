package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTeamSkillDirs(t *testing.T) {
	cfg := &config{
		AIHome: "/home/tester",
		AIDir:  "/home/tester/.claude",
	}

	cfg.AIBinary = "claude"
	if got := teamSkillDirs(cfg); len(got) != 1 || got[0] != "/home/tester/.claude/skills" {
		t.Fatalf("teamSkillDirs(claude) = %v", got)
	}

	cfg.AIBinary = "codex"
	if got := teamSkillDirs(cfg); len(got) != 1 || got[0] != "/home/tester/.agents/skills" {
		t.Fatalf("teamSkillDirs(codex) = %v", got)
	}
}

func TestSetupTeamPrompt_CodexWritesAGENTSAndSkills(t *testing.T) {
	home := t.TempDir()
	aiDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(aiDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		TeamMCP:       true,
		AIHome:        home,
		AIDir:         aiDir,
		AIBinary:      "codex",
		AIProjectFile: "AGENTS.md",
	}

	setupTeamPrompt(cfg)

	agentsPath := filepath.Join(aiDir, "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(data), "$mt-plan") {
		t.Fatalf("codex AGENTS.md should mention codex team skills, got: %s", data)
	}
	if strings.Contains(string(data), "/mt:plan") {
		t.Fatal("codex AGENTS.md should not mention Claude slash commands")
	}
	if !strings.Contains(string(data), "timeoutSec <= 90") {
		t.Fatal("codex AGENTS.md should include bounded wait guidance")
	}
	if !strings.Contains(string(data), "Do NOT call wait_for_task directly from the main leader flow") {
		t.Fatal("codex AGENTS.md should keep wait_for_task off the main leader path")
	}
	if !strings.Contains(string(data), "terminal status") {
		t.Fatal("codex AGENTS.md should preserve specific terminal statuses after timeout recovery")
	}
	if !strings.Contains(string(data), "get_task_state") {
		t.Fatal("codex AGENTS.md should use get_task_state for cheap routine polling")
	}
	if !strings.Contains(string(data), "get_pool_state") {
		t.Fatal("codex AGENTS.md should use get_pool_state for cheap capacity checks")
	}
	if !(strings.Contains(string(data), "Reserve get_task_result") && strings.Contains(string(data), "terminal inspection")) &&
		!strings.Contains(string(data), "Use this after a task reaches a terminal state") {
		t.Fatal("codex AGENTS.md should reserve get_task_result for terminal inspection")
	}
	if !(strings.Contains(string(data), "Reserve get_status") && strings.Contains(string(data), "explicit full status reports")) &&
		!strings.Contains(string(data), "Use this for explicit status reports, not routine scheduling checks") {
		t.Fatal("codex AGENTS.md should reserve get_status for explicit full status reports")
	}
	if !(strings.Contains(string(data), "coarse") && strings.Contains(string(data), "tight loop")) {
		t.Fatal("codex AGENTS.md should discourage tight polling loops")
	}

	skillPath := filepath.Join(home, ".agents", "skills", "mt-plan", "SKILL.md")
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read codex skill: %v", err)
	}
	if !strings.Contains(string(skillData), "name: \"mt-plan\"") {
		t.Fatalf("codex skill missing expected name: %s", skillData)
	}
	if !strings.Contains(string(skillData), "timeoutSec <= 90") {
		t.Fatal("codex skill should include bounded wait guidance")
	}
	if !strings.Contains(string(skillData), "Do NOT call wait_for_task directly from the main leader flow") {
		t.Fatal("codex skill should keep wait_for_task off the main leader path")
	}
	if !strings.Contains(string(skillData), "get_task_state") {
		t.Fatal("codex skill should use get_task_state for cheap routine polling")
	}
	if !strings.Contains(string(skillData), "get_pool_state") {
		t.Fatal("codex skill should use get_pool_state for cheap capacity checks")
	}
	if !(strings.Contains(string(skillData), "Reserve get_task_result") && strings.Contains(string(skillData), "terminal inspection")) &&
		!strings.Contains(string(skillData), "call get_task_result only after the task reaches a terminal state") {
		t.Fatal("codex skill should reserve get_task_result for terminal inspection")
	}
	if !(strings.Contains(string(skillData), "Reserve get_status") && strings.Contains(string(skillData), "explicit full status reports")) {
		t.Fatal("codex skill should reserve get_status for explicit full status reports")
	}
	if !(strings.Contains(string(skillData), "coarse") && strings.Contains(string(skillData), "tight loop")) {
		t.Fatal("codex skill should discourage tight polling loops")
	}
	if !strings.Contains(string(skillData), "full stored") || !strings.Contains(string(skillData), "planner output") {
		t.Fatal("codex skill should describe stored planner output precisely")
	}
}

func TestSetupTeamPrompt_ClaudeWritesProjectFileAndSlashSkills(t *testing.T) {
	home := t.TempDir()
	aiDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(aiDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		TeamMCP:       true,
		AIHome:        home,
		AIDir:         aiDir,
		AIBinary:      "claude",
		AIProjectFile: "CLAUDE.md",
	}

	setupTeamPrompt(cfg)

	projectPath := filepath.Join(aiDir, "CLAUDE.md")
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(data), "/mt:plan") {
		t.Fatalf("claude prompt should mention slash commands, got: %s", data)
	}

	skillPath := filepath.Join(aiDir, "skills", "mt:plan", "SKILL.md")
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read claude skill: %v", err)
	}
	if !strings.Contains(string(skillData), "name: \"mt:plan\"") {
		t.Fatalf("claude skill missing expected name: %s", skillData)
	}
}

func TestCodexTeamMCPAddArgs_IncludeRequiredEnv(t *testing.T) {
	env := map[string]string{
		"MITTENS_STATE_DIR":    "/tmp/state",
		"MITTENS_SESSION_ID":   "team-123",
		"MITTENS_BROKER_PORT":  "8080",
		"MITTENS_BROKER_TOKEN": "secret",
		"MITTENS_POOL_TOKEN":   "pool-token",
		"MITTENS_MAX_WORKERS":  "8",
		"MITTENS_TEAM_CONFIG":  "/tmp/team.yaml",
		"MITTENS_PLANS_DIR":    "/tmp/plans",
	}
	for k, v := range env {
		old, had := os.LookupEnv(k)
		if err := os.Setenv(k, v); err != nil {
			t.Fatalf("setenv %s: %v", k, err)
		}
		defer func(key, prev string, ok bool) {
			if ok {
				_ = os.Setenv(key, prev)
			} else {
				_ = os.Unsetenv(key)
			}
		}(k, old, had)
	}

	got := codexTeamMCPAddArgs()
	wantPrefix := []string{"mcp", "add", "team"}
	if !slices.Equal(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("codexTeamMCPAddArgs prefix = %v, want %v", got[:len(wantPrefix)], wantPrefix)
	}

	for k, v := range env {
		pair := k + "=" + v
		found := false
		for i := 0; i+1 < len(got); i++ {
			if got[i] == "--env" && got[i+1] == pair {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("codexTeamMCPAddArgs missing --env %s", pair)
		}
	}

	if got[len(got)-2] != "--" || got[len(got)-1] != "/usr/local/bin/team-mcp" {
		t.Fatalf("codexTeamMCPAddArgs suffix = %v, want [-- /usr/local/bin/team-mcp]", got[len(got)-2:])
	}
}
