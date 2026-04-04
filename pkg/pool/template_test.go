package pool

import (
	"testing"
)

func TestNewFeaturePipeline(t *testing.T) {
	pipe := NewFeaturePipeline("build feature X", nil)

	if pipe.Goal != "build feature X" {
		t.Errorf("goal = %q", pipe.Goal)
	}
	if len(pipe.Stages) != 7 {
		t.Fatalf("stages = %d, want 7", len(pipe.Stages))
	}

	expected := []struct {
		name        string
		role        string
		fan         FanMode
		autoAdvance bool
	}{
		{"design", "planner", FanOut, true},
		{"design-review", "reviewer", Streaming, true},
		{"meta-review", "planner", FanIn, true},
		{"implement", "implementer", FanOut, true},
		{"impl-review", "reviewer", Streaming, true},
		{"meta-review-2", "planner", FanIn, true},
		{"finalize", "planner", FanIn, false},
	}

	for i, exp := range expected {
		s := pipe.Stages[i]
		if s.Name != exp.name {
			t.Errorf("stage %d: name = %q, want %q", i, s.Name, exp.name)
		}
		if s.Role != exp.role {
			t.Errorf("stage %d: role = %q, want %q", i, s.Role, exp.role)
		}
		if s.Fan != exp.fan {
			t.Errorf("stage %d: fan = %q, want %q", i, s.Fan, exp.fan)
		}
		if s.AutoAdvance != exp.autoAdvance {
			t.Errorf("stage %d: autoAdvance = %v, want %v", i, s.AutoAdvance, exp.autoAdvance)
		}
		if len(s.Tasks) == 0 {
			t.Errorf("stage %d: no tasks", i)
		}
	}
}

func TestNewFeaturePipeline_CustomImplTasks(t *testing.T) {
	implTasks := []StageTask{
		{ID: "impl-api", PromptTmpl: "implement API: {{.Goal}}"},
		{ID: "impl-ui", PromptTmpl: "implement UI: {{.Goal}}"},
		{ID: "impl-db", PromptTmpl: "implement DB: {{.Goal}}"},
	}

	pipe := NewFeaturePipeline("multi-component feature", implTasks)

	// Stage 3 (implement) should have the custom tasks.
	if len(pipe.Stages[3].Tasks) != 3 {
		t.Fatalf("implement stage tasks = %d, want 3", len(pipe.Stages[3].Tasks))
	}
	if pipe.Stages[3].Tasks[0].ID != "impl-api" {
		t.Errorf("impl task 0 id = %q, want impl-api", pipe.Stages[3].Tasks[0].ID)
	}
	if pipe.Stages[3].Tasks[2].ID != "impl-db" {
		t.Errorf("impl task 2 id = %q, want impl-db", pipe.Stages[3].Tasks[2].ID)
	}

	// Stage 4 (impl-review) should have matching review tasks.
	if len(pipe.Stages[4].Tasks) != 3 {
		t.Fatalf("review stage tasks = %d, want 3", len(pipe.Stages[4].Tasks))
	}
	if pipe.Stages[4].Tasks[0].ID != "impl-api-review" {
		t.Errorf("review task 0 id = %q, want impl-api-review", pipe.Stages[4].Tasks[0].ID)
	}
}
