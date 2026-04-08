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

const councilTurnSuffix = `

At the end of your response, output a council turn block with valid JSON:
<council_turn>
{
  "seat": "A|B",
  "turn": 1,
  "stance": "propose|revise|converged|blocked",
  "candidatePlan": {
    "title": "Short plan title",
    "summary": "Optional operator-facing summary",
    "lineage": "optional-lineage-slug",
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
    "questions": []
  },
  "adoptedPriorPlan": false,
  "disagreements": [
    {
      "id": "d1",
      "severity": "major|critical",
      "category": "architecture|dependency|scope|assumption|overengineering",
      "title": "Short title",
      "impact": "Why this matters",
      "blocking": true,
      "taskIds": ["t1"],
      "suggestedChange": "Optional fix direction"
    }
  ],
  "questionsForUser": [
    {
      "id": "q1",
      "question": "Question text",
      "whyItMatters": "How the answer changes the plan",
      "blocking": true
    }
  ],
  "strengths": ["Short strengths"],
  "seatMemo": "Compact reasoning summary for fallback rehydration",
  "rejectedAlternatives": ["Discarded option and why"],
  "summary": "Overall seat summary"
}
</council_turn>

Return only valid JSON inside <council_turn>.`

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

func BuildCouncilTurnPrompt(idea string, prior []CouncilTurnArtifact, seat string, turn int, summary string) string {
	var b strings.Builder
	b.WriteString("## Planner Council Turn\n\n")
	b.WriteString("You are one seat in a two-seat planner council. Produce a full replacement candidate plan every turn.\n\n")
	b.WriteString("### Council Rules\n\n")
	b.WriteString("- Seats alternate A then B.\n")
	b.WriteString("- Explicit adoption only. Set `adoptedPriorPlan=true` only when you intentionally adopt the previous seat's plan.\n")
	b.WriteString("- Use `stance=converged` only when you adopt the prior plan and believe the council has converged.\n")
	b.WriteString("- Use `questionsForUser` only for genuinely blocking operator input.\n")
	b.WriteString("- `seatMemo` and `rejectedAlternatives` should help rehydration if your seat is replaced.\n\n")
	b.WriteString("### Current Turn\n\n")
	b.WriteString(fmt.Sprintf("Seat: %s\nTurn: %d\n\n", strings.TrimSpace(seat), turn))
	if strings.TrimSpace(summary) != "" {
		b.WriteString("### Operator Intent\n\n")
		b.WriteString(strings.TrimSpace(summary))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(idea) != "" {
		b.WriteString("### Idea\n\n")
		b.WriteString(strings.TrimSpace(idea))
		b.WriteString("\n\n")
	}
	if len(prior) > 0 {
		b.WriteString("### Prior Artifact Chain\n\n")
		for _, item := range prior {
			data, err := json.MarshalIndent(item, "", "  ")
			if err != nil {
				continue
			}
			b.WriteString("```json\n")
			b.Write(data)
			b.WriteString("\n```\n\n")
		}
	}
	b.WriteString(councilTurnSuffix)
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
