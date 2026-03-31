package team

import (
	"strings"
	"testing"
)

func TestLeaderSystemPrompt_NonEmpty(t *testing.T) {
	p := LeaderSystemPrompt()
	if p == "" {
		t.Fatal("leader system prompt is empty")
	}
}

func TestLeaderSystemPrompt_ContainsKeyTools(t *testing.T) {
	p := LeaderSystemPrompt()
	tools := []string{
		"spawn_worker", "kill_worker",
		"enqueue_task", "dispatch_task",
		"get_status", "get_task_result", "get_task_output",
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
	skills := LeaderSkills()
	if len(skills) != 4 {
		t.Fatalf("expected 4 skills, got %d", len(skills))
	}
}

func TestLeaderSkills_Names(t *testing.T) {
	skills := LeaderSkills()
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
	for _, s := range LeaderSkills() {
		if s.Content == "" {
			t.Errorf("skill %q has empty content", s.Name)
		}
		if !strings.Contains(s.Content, "---") {
			t.Errorf("skill %q missing YAML frontmatter", s.Name)
		}
	}
}
