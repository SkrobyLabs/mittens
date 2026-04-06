package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PlanArtifact is the structured planner output Kitchen can ingest.
type PlanArtifact struct {
	Lineage   string                 `json:"lineage,omitempty"`
	Title     string                 `json:"title"`
	Summary   string                 `json:"summary,omitempty"`
	Ownership *PlanArtifactOwnership `json:"ownership,omitempty"`
	Tasks     []PlanArtifactTask     `json:"tasks"`
	Questions []PlanArtifactQuestion `json:"questions,omitempty"`
}

type PlanArtifactOwnership struct {
	Packages  []string `json:"packages,omitempty"`
	Exclusive bool     `json:"exclusive,omitempty"`
}

type PlanArtifactTask struct {
	ID               string                       `json:"id"`
	Title            string                       `json:"title"`
	Prompt           string                       `json:"prompt"`
	Complexity       string                       `json:"complexity"`
	Dependencies     []string                     `json:"dependencies,omitempty"`
	Outputs          *PlanArtifactOutputs         `json:"outputs,omitempty"`
	SuccessCriteria  *PlanArtifactSuccessCriteria `json:"successCriteria,omitempty"`
	ReviewComplexity string                       `json:"reviewComplexity,omitempty"`
}

type PlanArtifactOutputs struct {
	Files     []string `json:"files,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type PlanArtifactSuccessCriteria struct {
	Advisory   string   `json:"advisory,omitempty"`
	Verifiable []string `json:"verifiable,omitempty"`
}

type PlanArtifactQuestion struct {
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
}

const plannerSuffix = `

At the end of your response, output a plan block with valid JSON:
<plan>
{
  "title": "Short plan title",
  "summary": "Optional operator-facing summary",
  "lineage": "optional-lineage-slug (kebab-case, no slashes — used as a directory and git sub-ref name)",
  "ownership": {
    "packages": ["optional/package"],
    "exclusive": false
  },
  "tasks": [
    {
      "id": "t1",
      "title": "Task title",
      "prompt": "Worker-ready task prompt",
      "complexity": "low|medium|high",
      "dependencies": [],
      "outputs": {
        "files": ["optional/file.go"],
        "artifacts": ["optional-artifact"]
      },
      "successCriteria": {
        "advisory": "Optional plain-language success note",
        "verifiable": ["Optional concrete checks"]
      },
      "reviewComplexity": "low|medium|high"
    }
  ],
  "questions": [
    {
      "question": "Optional question for the operator",
      "context": "Why the answer matters"
    }
  ]
}
</plan>

Return only valid JSON inside <plan>. Use stable task IDs like t1, t2, t3.`

// BuildPlannerPrompt creates a prompt that instructs the planner AI to output a
// structured plan artifact. The caller's Execute() wraps this with BuildPrompt,
// which adds the handover instructions, so prior context is embedded here.
func BuildPlannerPrompt(taskPrompt, priorContext string) string {
	var b strings.Builder
	if priorContext != "" {
		b.WriteString("## Prior Context\n\n")
		b.WriteString(priorContext)
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString("## Planning Request\n\n")
	b.WriteString(taskPrompt)
	b.WriteString(plannerSuffix)
	return b.String()
}

// BuildPlanReviewPrompt creates a prompt that asks a reviewer to assess a
// structured execution plan and emit the standard <review> verdict block.
func BuildPlanReviewPrompt(planJSON, priorContext string, reviewRounds int) string {
	var b strings.Builder
	if priorContext != "" {
		b.WriteString("## Prior Context\n\n")
		b.WriteString(priorContext)
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString("## Plan Review Request\n\n")
	b.WriteString("Review the execution plan below for decomposition quality, dependency correctness, outputs, success criteria, and whether any operator questions are justified.\n\n")
	if reviewRounds > 1 {
		b.WriteString(fmt.Sprintf("Conduct %d internal review passes before producing your final verdict.\n\n", reviewRounds))
	}
	b.WriteString("### Plan JSON\n\n```json\n")
	b.WriteString(strings.TrimSpace(planJSON))
	b.WriteString("\n```\n")
	b.WriteString(reviewSuffix)
	return b.String()
}

// BuildPlanRevisionPrompt creates a prompt that asks a planner to revise a plan
// using reviewer feedback while emitting a fresh <plan> JSON artifact.
func BuildPlanRevisionPrompt(planJSON, reviewFeedback, priorContext string) string {
	var b strings.Builder
	if priorContext != "" {
		b.WriteString("## Prior Context\n\n")
		b.WriteString(priorContext)
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString("## Plan Revision Request\n\n")
	b.WriteString("Revise the execution plan below using the reviewer feedback. Keep sound parts, fix the issues raised, and return a complete replacement plan.\n\n")
	b.WriteString("### Reviewer Feedback\n\n")
	b.WriteString(strings.TrimSpace(reviewFeedback))
	b.WriteString("\n\n### Current Plan JSON\n\n```json\n")
	b.WriteString(strings.TrimSpace(planJSON))
	b.WriteString("\n```\n")
	b.WriteString(plannerSuffix)
	return b.String()
}

// ExtractPlanArtifact parses the final <plan> JSON block from adapter output.
func ExtractPlanArtifact(output string) (*PlanArtifact, error) {
	end := strings.LastIndex(output, "</plan>")
	if end < 0 {
		return nil, fmt.Errorf("plan block not found")
	}
	start := strings.LastIndex(output[:end], "<plan>")
	if start < 0 {
		return nil, fmt.Errorf("plan block not found")
	}

	body := strings.TrimSpace(output[start+len("<plan>") : end])
	if body == "" {
		return nil, fmt.Errorf("plan block is empty")
	}

	var plan PlanArtifact
	if err := json.Unmarshal([]byte(body), &plan); err != nil {
		return nil, fmt.Errorf("decode plan JSON: %w", err)
	}
	if err := validatePlanArtifact(&plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func validatePlanArtifact(plan *PlanArtifact) error {
	if plan == nil {
		return fmt.Errorf("plan artifact is nil")
	}
	plan.Lineage = strings.TrimSpace(plan.Lineage)
	plan.Title = strings.TrimSpace(plan.Title)
	plan.Summary = strings.TrimSpace(plan.Summary)
	for i := range plan.Questions {
		plan.Questions[i].Question = strings.TrimSpace(plan.Questions[i].Question)
		plan.Questions[i].Context = strings.TrimSpace(plan.Questions[i].Context)
	}

	if plan.Title == "" {
		return fmt.Errorf("plan title must not be empty")
	}
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("plan must include at least one task")
	}

	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		task.ID = strings.TrimSpace(task.ID)
		task.Title = strings.TrimSpace(task.Title)
		task.Prompt = strings.TrimSpace(task.Prompt)
		task.Complexity = strings.TrimSpace(task.Complexity)
		task.ReviewComplexity = strings.TrimSpace(task.ReviewComplexity)
		for j := range task.Dependencies {
			task.Dependencies[j] = strings.TrimSpace(task.Dependencies[j])
		}
		if task.Outputs != nil {
			for j := range task.Outputs.Files {
				task.Outputs.Files[j] = strings.TrimSpace(task.Outputs.Files[j])
			}
			for j := range task.Outputs.Artifacts {
				task.Outputs.Artifacts[j] = strings.TrimSpace(task.Outputs.Artifacts[j])
			}
		}
		if task.SuccessCriteria != nil {
			task.SuccessCriteria.Advisory = strings.TrimSpace(task.SuccessCriteria.Advisory)
			for j := range task.SuccessCriteria.Verifiable {
				task.SuccessCriteria.Verifiable[j] = strings.TrimSpace(task.SuccessCriteria.Verifiable[j])
			}
		}

		switch {
		case task.ID == "":
			return fmt.Errorf("task %d id must not be empty", i+1)
		case task.Title == "":
			return fmt.Errorf("task %s title must not be empty", task.ID)
		case task.Prompt == "":
			return fmt.Errorf("task %s prompt must not be empty", task.ID)
		case task.Complexity == "":
			return fmt.Errorf("task %s complexity must not be empty", task.ID)
		}
	}
	return nil
}
