package team

import (
	"strings"
	"testing"
)

func TestLeaderSystemPrompt_NonEmpty(t *testing.T) {
	p := LeaderSystemPrompt("claude")
	if p == "" {
		t.Fatal("leader system prompt is empty")
	}
}

func TestLeaderSystemPrompt_ContainsKeyTools(t *testing.T) {
	p := LeaderSystemPrompt("claude")
	tools := []string{
		"spawn_worker", "kill_worker",
		"enqueue_task", "dispatch_task",
		"get_pool_state", "get_status", "get_task_state", "get_task_result", "get_task_output",
		"submit_pipeline", "cancel_pipeline",
		"dispatch_review", "report_review",
		"pending_questions", "answer_question",
		"resolve_escalation",
	}
	for _, tool := range tools {
		if !strings.Contains(p, tool) {
			t.Errorf("prompt missing tool reference: %s", tool)
		}
	}
}

func TestLeaderSkills_Count(t *testing.T) {
	skills := LeaderSkills("claude")
	if len(skills) != 4 {
		t.Fatalf("expected 4 skills, got %d", len(skills))
	}
}

func TestLeaderSkills_Names(t *testing.T) {
	skills := LeaderSkills("claude")
	names := make(map[string]bool)
	for _, s := range skills {
		names[s.Name] = true
	}
	required := []string{"mt:status", "mt:plan"}
	for _, name := range required {
		if !names[name] {
			t.Errorf("missing required skill: %s", name)
		}
	}
}

func TestLeaderSkills_ContentNonEmpty(t *testing.T) {
	for _, s := range LeaderSkills("claude") {
		if s.Content == "" {
			t.Errorf("skill %q has empty content", s.Name)
		}
		if !strings.Contains(s.Content, "---") {
			t.Errorf("skill %q missing YAML frontmatter", s.Name)
		}
	}
}

func TestLeaderSystemPrompt_CodexAvoidsClaudeSpecificWorkflow(t *testing.T) {
	p := LeaderSystemPrompt("codex")
	if strings.Contains(p, "/mt:plan") {
		t.Fatal("codex prompt should not mention Claude slash commands")
	}
	if strings.Contains(p, "run_in_background: true") {
		t.Fatal("codex prompt should not require Claude background-agent syntax")
	}
	if !strings.Contains(p, "$mt-plan") {
		t.Fatal("codex prompt should mention codex-specific team skills")
	}
	if !strings.Contains(p, "/agent") {
		t.Fatal("codex prompt should mention Codex subagents")
	}
	if !strings.Contains(p, "MCP toolset is unavailable") {
		t.Fatal("codex prompt should fail fast when team MCP is unavailable")
	}
	if !strings.Contains(strings.ToLower(p), "do not fall back to local planning") {
		t.Fatal("codex prompt should explicitly forbid local planning fallback")
	}
	if !strings.Contains(p, "timeoutSec <= 90") {
		t.Fatal("codex prompt should recommend bounded wait_for_task timeouts")
	}
	if !strings.Contains(p, "Do NOT call wait_for_task directly from the main leader flow") {
		t.Fatal("codex prompt should forbid foreground leader wait_for_task usage")
	}
	if !strings.Contains(p, "preserve the specific terminal status") {
		t.Fatal("codex prompt should preserve specific terminal statuses after wait timeouts")
	}
	if !strings.Contains(p, "full stored") {
		t.Fatal("codex prompt should not overpromise full unbounded task output")
	}
	if !strings.Contains(p, "get_task_state") {
		t.Fatal("codex prompt should mention the cheap task-state polling tool")
	}
	if !strings.Contains(p, "get_pool_state") {
		t.Fatal("codex prompt should mention the cheap pool-state polling tool")
	}
	if !(strings.Contains(p, "Reserve get_task_result") && strings.Contains(p, "terminal inspection")) &&
		!strings.Contains(p, "Use this after a task reaches a terminal state") {
		t.Fatal("codex prompt should reserve get_task_result for terminal inspection")
	}
	if !(strings.Contains(p, "coarse") && strings.Contains(p, "tight loop")) {
		t.Fatal("codex prompt should discourage tight polling loops")
	}
	if !(strings.Contains(p, "Reserve get_status") && strings.Contains(p, "explicit full status reports")) &&
		!strings.Contains(p, "Use this for explicit status reports, not routine scheduling checks") {
		t.Fatal("codex prompt should reserve get_status for explicit full status reports")
	}
}

func TestLeaderSkills_CodexNames(t *testing.T) {
	skills := LeaderSkills("codex")
	names := make(map[string]bool)
	for _, s := range skills {
		names[s.Name] = true
		if strings.Contains(s.Name, ":") {
			t.Fatalf("codex skill %q should use explicit skill-mention syntax names", s.Name)
		}
	}
	required := []string{"mt-status", "mt-plan", "mt-execute", "mt-plans"}
	for _, name := range required {
		if !names[name] {
			t.Errorf("missing required codex skill: %s", name)
		}
	}
}

func TestLeaderSkills_CodexFailFastWhenTeamUnavailable(t *testing.T) {
	for _, s := range LeaderSkills("codex") {
		if !strings.Contains(s.Content, "team MCP") {
			t.Fatalf("codex skill %q should mention team MCP availability", s.Name)
		}
		if !strings.Contains(strings.ToLower(s.Content), "do not fall back") {
			t.Fatalf("codex skill %q should explicitly forbid fallback behavior", s.Name)
		}
	}
}

func TestLeaderSkills_CodexUseBoundedWaits(t *testing.T) {
	for _, s := range LeaderSkills("codex") {
		if strings.Contains(s.Content, "wait_for_task") {
			if !strings.Contains(s.Content, "timeoutSec <= 90") && !strings.Contains(s.Content, "timeoutSec 60") {
				t.Fatalf("codex skill %q should constrain wait_for_task timeout", s.Name)
			}
			if !strings.Contains(s.Content, "Do NOT call wait_for_task directly from the main leader flow") && !strings.Contains(s.Content, "main leader should not call wait_for_task directly") {
				t.Fatalf("codex skill %q should keep wait_for_task off the main leader path", s.Name)
			}
			if !strings.Contains(s.Content, "terminal status") {
				t.Fatalf("codex skill %q should preserve specific terminal statuses after timeout recovery", s.Name)
			}
			if !strings.Contains(s.Content, "get_task_state") {
				t.Fatalf("codex skill %q should use get_task_state for routine polling", s.Name)
			}
			if !strings.Contains(s.Content, "get_pool_state") {
				t.Fatalf("codex skill %q should use get_pool_state for cheap capacity checks", s.Name)
			}
			if !(strings.Contains(s.Content, "Reserve get_task_result") && strings.Contains(s.Content, "terminal inspection")) &&
				!strings.Contains(s.Content, "call get_task_result only after the task reaches a terminal state") {
				t.Fatalf("codex skill %q should reserve get_task_result for terminal inspection", s.Name)
			}
			if !(strings.Contains(s.Content, "Reserve get_status") && strings.Contains(s.Content, "explicit full status reports")) {
				t.Fatalf("codex skill %q should reserve get_status for explicit full status reports", s.Name)
			}
			if !(strings.Contains(s.Content, "coarse") && strings.Contains(s.Content, "tight loop")) {
				t.Fatalf("codex skill %q should discourage tight polling loops", s.Name)
			}
			if strings.Contains(s.Name, "plan") && !strings.Contains(s.Content, "full stored") {
				t.Fatalf("codex skill %q should describe stored task output precisely", s.Name)
			}
		}
	}
}
