package adapter

import (
	"strings"
	"testing"
)

func TestBuildPlannerPrompt(t *testing.T) {
	prompt := BuildPlannerPrompt("Plan a parser cleanup rollout.", "Existing parser work is on a feature branch.")
	if !strings.Contains(prompt, "## Planning Request") {
		t.Fatal("planner prompt missing Planning Request header")
	}
	if !strings.Contains(prompt, "Plan a parser cleanup rollout.") {
		t.Fatal("planner prompt missing task prompt")
	}
	if !strings.Contains(prompt, "Existing parser work is on a feature branch.") {
		t.Fatal("planner prompt missing prior context")
	}
	if !strings.Contains(prompt, "<plan>") {
		t.Fatal("planner prompt missing plan block instructions")
	}
}

func TestBuildPlanReviewPrompt(t *testing.T) {
	prompt := BuildPlanReviewPrompt(`{"title":"Parser cleanup","tasks":[{"id":"t1","title":"Do work","prompt":"Do work","complexity":"medium"}]}`, "Prior planning context", 2)
	if !strings.Contains(prompt, "## Plan Review Request") {
		t.Fatal("review prompt missing Plan Review Request header")
	}
	if !strings.Contains(prompt, "Prior planning context") {
		t.Fatal("review prompt missing prior context")
	}
	if !strings.Contains(prompt, "Conduct 2 internal review passes") {
		t.Fatal("review prompt missing review rounds guidance")
	}
	if !strings.Contains(prompt, "```json") {
		t.Fatal("review prompt missing JSON fence")
	}
	if !strings.Contains(prompt, "<review>") {
		t.Fatal("review prompt missing review block instructions")
	}
}

func TestBuildPlanRevisionPrompt(t *testing.T) {
	prompt := BuildPlanRevisionPrompt(`{"title":"Parser cleanup","tasks":[{"id":"t1","title":"Do work","prompt":"Do work","complexity":"medium"}]}`, "Split the work into smaller tasks.", "Original operator intent")
	if !strings.Contains(prompt, "## Plan Revision Request") {
		t.Fatal("revision prompt missing Plan Revision Request header")
	}
	if !strings.Contains(prompt, "Split the work into smaller tasks.") {
		t.Fatal("revision prompt missing reviewer feedback")
	}
	if !strings.Contains(prompt, "Original operator intent") {
		t.Fatal("revision prompt missing prior context")
	}
	if !strings.Contains(prompt, "<plan>") {
		t.Fatal("revision prompt missing plan artifact instructions")
	}
}

func TestExtractPlanArtifact_Valid(t *testing.T) {
	output := `Planning complete.

<plan>
{
  "title": "Typed parser errors",
  "summary": "Split parser error cleanup into two safe tasks.",
  "lineage": "parser-errors",
  "tasks": [
    {
      "id": "t1",
      "title": "Add typed parser errors",
      "prompt": "Introduce typed parser errors and update callers.",
      "complexity": "medium",
      "outputs": {
        "files": ["parser/errors.go"]
      },
      "successCriteria": {
        "verifiable": ["go test ./parser/..."]
      },
      "reviewComplexity": "medium"
    }
  ],
  "questions": [
    {
      "question": "Should lexer and parser errors share one public type?"
    }
  ]
}
</plan>`

	plan, err := ExtractPlanArtifact(output)
	if err != nil {
		t.Fatalf("ExtractPlanArtifact: %v", err)
	}
	if plan.Title != "Typed parser errors" {
		t.Fatalf("title = %q", plan.Title)
	}
	if plan.Lineage != "parser-errors" {
		t.Fatalf("lineage = %q", plan.Lineage)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(plan.Tasks))
	}
	if plan.Tasks[0].ID != "t1" {
		t.Fatalf("task id = %q, want t1", plan.Tasks[0].ID)
	}
	if len(plan.Questions) != 1 || plan.Questions[0].Question == "" {
		t.Fatalf("questions = %+v, want one populated question", plan.Questions)
	}
}

func TestExtractPlanArtifact_UsesLastPlanBlock(t *testing.T) {
	output := `Prompt echo.
<plan>{"title":"Old","tasks":[{"id":"t1","title":"Old","prompt":"Old","complexity":"low"}]}</plan>

Fresh output.
<plan>{"title":"New","tasks":[{"id":"t1","title":"New","prompt":"Do new work","complexity":"medium"}]}</plan>`

	plan, err := ExtractPlanArtifact(output)
	if err != nil {
		t.Fatalf("ExtractPlanArtifact: %v", err)
	}
	if plan.Title != "New" {
		t.Fatalf("title = %q, want New", plan.Title)
	}
}

func TestExtractPlanArtifact_Missing(t *testing.T) {
	if _, err := ExtractPlanArtifact("No plan artifact here."); err == nil {
		t.Fatal("expected missing plan artifact error")
	}
}

func TestExtractPlanArtifact_InvalidJSON(t *testing.T) {
	output := `<plan>{"title":"Broken","tasks":[}</plan>`
	if _, err := ExtractPlanArtifact(output); err == nil || !strings.Contains(err.Error(), "decode plan JSON") {
		t.Fatalf("err = %v, want decode failure", err)
	}
}

func TestExtractPlanArtifact_RejectsMissingRequiredFields(t *testing.T) {
	output := `<plan>{"title":"  ","tasks":[{"id":"t1","title":"Task","prompt":"Do work","complexity":"low"}]}</plan>`
	if _, err := ExtractPlanArtifact(output); err == nil || !strings.Contains(err.Error(), "plan title") {
		t.Fatalf("err = %v, want title validation failure", err)
	}
}
