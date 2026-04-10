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

func TestBuildCouncilTurnPrompt(t *testing.T) {
	prompt := BuildCouncilTurnPrompt("Refine the parser plan.", nil, "A", 2, "Operator wants a safer rollout.")
	if !strings.Contains(prompt, "Produce a full candidate plan each turn") {
		t.Fatal("council prompt missing candidate-plan guidance")
	}
	if !strings.Contains(prompt, "have no substantive improvements to add") {
		t.Fatal("council prompt missing explicit adoption guidance")
	}
	if !strings.Contains(prompt, "Do not re-emit the prior plan verbatim") {
		t.Fatal("council prompt missing verbatim re-emission warning")
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

func TestExtractPlanArtifact_LenientJSONBody(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name: "fenced json",
			output: "<plan>\n```json\n" +
				"{\"title\":\"Fenced\",\"tasks\":[{\"id\":\"t1\",\"title\":\"Task\",\"prompt\":\"Do work\",\"complexity\":\"low\"}]}\n" +
				"```\n</plan>",
		},
		{
			name: "trailing commas",
			output: `<plan>{
  "title": "Trailing",
  "tasks": [
    {"id":"t1","title":"Task","prompt":"Do work","complexity":"low",},
  ],
}</plan>`,
		},
		{
			name: "case insensitive whitespace tag",
			output: `< Plan >
{"title":"Spaced","tasks":[{"id":"t1","title":"Task","prompt":"Do work","complexity":"low"}]}
</ PLAN >`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := ExtractPlanArtifact(tt.output)
			if err != nil {
				t.Fatalf("ExtractPlanArtifact: %v", err)
			}
			if len(plan.Tasks) != 1 || plan.Tasks[0].ID != "t1" {
				t.Fatalf("plan = %+v, want one valid task", plan)
			}
		})
	}
}

func TestExtractPlanArtifact_RejectsMalformedTagWithOtherJSONElsewhere(t *testing.T) {
	output := `schema example: {"title":"Example","tasks":[]}
<plan>{"title":"Broken","tasks":[{"id":"t1"}]}`
	if _, err := ExtractPlanArtifact(output); err == nil || !strings.Contains(err.Error(), "plan block not found") {
		t.Fatalf("err = %v, want missing plan block", err)
	}
}

func TestExtractPlanArtifact_RejectsSingleQuotedJSON(t *testing.T) {
	output := `<plan>{'title':'Broken','tasks':[{'id':'t1','title':'Task','prompt':'Do work','complexity':'low'}]}</plan>`
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

func TestExtractCouncilTurnArtifact_LenientJSONBody(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name: "fenced json",
			output: "<council_turn>\n```json\n" +
				"{\"seat\":\"A\",\"turn\":1,\"stance\":\"propose\",\"candidatePlan\":{\"title\":\"Council\",\"tasks\":[{\"id\":\"t1\",\"title\":\"Task\",\"prompt\":\"Do work\",\"complexity\":\"low\"}]}}\n" +
				"```\n</council_turn>",
		},
		{
			name: "trailing commas and spaced tag",
			output: `< council_turn >{
  "seat":"A",
  "turn":1,
  "stance":"propose",
  "candidatePlan":{
    "title":"Council",
    "tasks":[
      {"id":"t1","title":"Task","prompt":"Do work","complexity":"low",},
    ],
  },
}</ council_turn >`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artifact, err := ExtractCouncilTurnArtifact(tt.output)
			if err != nil {
				t.Fatalf("ExtractCouncilTurnArtifact: %v", err)
			}
			if artifact.CandidatePlan == nil || artifact.CandidatePlan.Title != "Council" {
				t.Fatalf("artifact = %+v, want candidate plan Council", artifact)
			}
		})
	}
}

func TestRepairJSONBody(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple_trailing_comma_in_object", in: `{"a":1,}`, want: `{"a":1}`},
		{name: "simple_trailing_comma_in_array", in: `[1,2,]`, want: `[1,2]`},
		{name: "string_containing_comma_brace", in: `{"a":"value,}","b":1,}`, want: `{"a":"value,}","b":1}`},
		{name: "string_with_escaped_quote", in: `{"a":"say \\\"hi\\\"","b":1,}`, want: `{"a":"say \\\"hi\\\"","b":1}`},
		{name: "string_with_escaped_backslash", in: `{"a":"c:\\\\temp","b":1,}`, want: `{"a":"c:\\\\temp","b":1}`},
		{name: "nested_objects_with_trailing_commas", in: `{"a":{"b":[1,2,],},"c":3,}`, want: `{"a":{"b":[1,2]},"c":3}`},
		{name: "no_change_when_clean", in: `{"a":1,"b":[2,3]}`, want: `{"a":1,"b":[2,3]}`},
		{name: "unicode_escape_in_string", in: `{"a":"\u2603","b":1,}`, want: `{"a":"\u2603","b":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repairJSONBody(tt.in); got != tt.want {
				t.Fatalf("repairJSONBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlanArtifactsEqual(t *testing.T) {
	base := &PlanArtifact{
		Lineage: "parser-errors",
		Title:   "Parser cleanup",
		Summary: "Normalize parser failures",
		Ownership: &PlanArtifactOwnership{
			Packages:  []string{"pkg/parser", "cmd/kitchen"},
			Exclusive: true,
		},
		Tasks: []PlanArtifactTask{
			{
				ID:               "t1",
				Title:            "Normalize errors",
				Prompt:           "  Add typed parser errors.  ",
				Complexity:       "medium",
				ReviewComplexity: "high",
				Dependencies:     []string{"t0", "t2"},
				Outputs:          &PlanArtifactOutputs{Files: []string{"parser/errors.go", "parser/errors_test.go"}},
				SuccessCriteria:  &PlanArtifactSuccessCriteria{Advisory: "  Tests green  ", Verifiable: []string{"go test ./parser/...", "go test ./cmd/kitchen/..."}},
			},
			{
				ID:         "t2",
				Title:      "Wire callers",
				Prompt:     "Update call sites.",
				Complexity: "low",
			},
		},
		Questions: []PlanArtifactQuestion{{Question: "ignored"}},
	}

	t.Run("nil", func(t *testing.T) {
		if !PlanArtifactsEqual(nil, nil) {
			t.Fatal("nil artifacts should compare equal")
		}
		if PlanArtifactsEqual(base, nil) {
			t.Fatal("non-nil artifact should not equal nil")
		}
	})

	t.Run("trimmed prompt unordered sets and questions ignored", func(t *testing.T) {
		other := &PlanArtifact{
			Lineage: " parser-errors ",
			Title:   "Parser cleanup",
			Summary: "Normalize parser failures",
			Ownership: &PlanArtifactOwnership{
				Packages:  []string{"cmd/kitchen", "pkg/parser"},
				Exclusive: true,
			},
			Tasks: []PlanArtifactTask{
				{
					ID:               "t1",
					Title:            "Normalize errors",
					Prompt:           "Add typed parser errors.",
					Complexity:       "medium",
					ReviewComplexity: "high",
					Dependencies:     []string{"t2", "t0"},
					Outputs:          &PlanArtifactOutputs{Files: []string{"parser/errors_test.go", "parser/errors.go"}},
					SuccessCriteria:  &PlanArtifactSuccessCriteria{Advisory: "Tests green", Verifiable: []string{"go test ./cmd/kitchen/...", "go test ./parser/..."}},
				},
				{
					ID:               "t2",
					Title:            "Wire callers",
					Prompt:           "Update call sites.",
					Complexity:       "low",
					ReviewComplexity: "",
					Outputs:          &PlanArtifactOutputs{},
					SuccessCriteria:  &PlanArtifactSuccessCriteria{},
				},
			},
			Questions: []PlanArtifactQuestion{{Question: "different and ignored"}},
		}
		if !PlanArtifactsEqual(base, other) {
			t.Fatalf("artifacts should compare equal:\nbase=%+v\nother=%+v", base, other)
		}
	})

	t.Run("reordered tasks not equal", func(t *testing.T) {
		other := &PlanArtifact{
			Lineage: base.Lineage,
			Title:   base.Title,
			Summary: base.Summary,
			Tasks:   []PlanArtifactTask{base.Tasks[1], base.Tasks[0]},
		}
		if PlanArtifactsEqual(base, other) {
			t.Fatal("reordered task list should not compare equal")
		}
	})

	t.Run("missing task not equal", func(t *testing.T) {
		other := &PlanArtifact{
			Lineage: base.Lineage,
			Title:   base.Title,
			Summary: base.Summary,
			Tasks:   []PlanArtifactTask{base.Tasks[0]},
		}
		if PlanArtifactsEqual(base, other) {
			t.Fatal("missing task should not compare equal")
		}
	})

	t.Run("nil and empty optional fields compare equal", func(t *testing.T) {
		left := &PlanArtifact{
			Title: "Same",
			Tasks: []PlanArtifactTask{{
				ID:         "t1",
				Title:      "Task",
				Prompt:     "Do work",
				Complexity: "medium",
			}},
		}
		right := &PlanArtifact{
			Title:     "Same",
			Ownership: &PlanArtifactOwnership{},
			Tasks: []PlanArtifactTask{{
				ID:               "t1",
				Title:            "Task",
				Prompt:           "Do work",
				Complexity:       "medium",
				Outputs:          &PlanArtifactOutputs{},
				SuccessCriteria:  &PlanArtifactSuccessCriteria{},
				ReviewComplexity: "",
			}},
		}
		if !PlanArtifactsEqual(left, right) {
			t.Fatalf("nil and empty optional fields should compare equal:\nleft=%+v\nright=%+v", left, right)
		}
	})
}
