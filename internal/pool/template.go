package pool

// NewFeaturePipeline returns a standard 7-stage feature pipeline.
// If implTasks is nil, a single default implementation task is used.
func NewFeaturePipeline(goal string, implTasks []StageTask) *Pipeline {
	if len(implTasks) == 0 {
		implTasks = []StageTask{
			{ID: "impl-default", PromptTmpl: "Implement the following based on the design:\n\nGoal: {{.Goal}}\n\n{{.PriorContext}}"},
		}
	}

	// Build review tasks matching the number of implementation tasks.
	reviewTasks := make([]StageTask, len(implTasks))
	for i := range implTasks {
		reviewTasks[i] = StageTask{
			ID:         implTasks[i].ID + "-review",
			PromptTmpl: "Review the implementation for correctness, style, and completeness:\n\nGoal: {{.Goal}}\n\n{{.PriorContext}}",
		}
	}

	return &Pipeline{
		Goal: goal,
		Stages: []Stage{
			{
				Name:        "design",
				Role:        "planner",
				Fan:         FanOut,
				AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "design", PromptTmpl: "Create a detailed design for: {{.Goal}}"},
				},
			},
			{
				Name:        "design-review",
				Role:        "reviewer",
				Fan:         Streaming,
				AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "design-review", PromptTmpl: "Review the design for completeness and correctness:\n\n{{.PriorContext}}"},
				},
			},
			{
				Name:        "meta-review",
				Role:        "planner",
				Fan:         FanIn,
				AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "meta-review-1", PromptTmpl: "Consolidate design reviews and produce a final design:\n\nGoal: {{.Goal}}\n\n{{.PriorContext}}"},
				},
			},
			{
				Name:        "implement",
				Role:        "implementer",
				Fan:         FanOut,
				AutoAdvance: true,
				Tasks:       implTasks,
			},
			{
				Name:        "impl-review",
				Role:        "reviewer",
				Fan:         Streaming,
				AutoAdvance: true,
				Tasks:       reviewTasks,
			},
			{
				Name:        "meta-review-2",
				Role:        "planner",
				Fan:         FanIn,
				AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "meta-review-2", PromptTmpl: "Consolidate implementation reviews, flag false positives:\n\nGoal: {{.Goal}}\n\n{{.PriorContext}}"},
				},
			},
			{
				Name:        "finalize",
				Role:        "planner",
				Fan:         FanIn,
				AutoAdvance: false,
				Tasks: []StageTask{
					{ID: "finalize", PromptTmpl: "Final merge check and summary for: {{.Goal}}\n\n{{.PriorContext}}"},
				},
			},
		},
	}
}
