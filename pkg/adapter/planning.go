package adapter

import (
	"encoding/json"
	"fmt"
	"sort"
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

When adoptedPriorPlan=true and stance=converged on turn 2+, candidatePlan may be null to indicate exact adoption of the prior plan.

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
	b.WriteString("You are one seat in a two-seat planner council. Produce a full candidate plan each turn, integrating any improvements over the prior seat's plan.\n\n")
	b.WriteString("### Council Rules\n\n")
	b.WriteString("- Seats alternate A then B.\n")
	b.WriteString("- If after reviewing the prior plan you have no substantive improvements to add, set `adoptedPriorPlan=true` and `stance=converged`. Do not re-emit the prior plan verbatim with `adoptedPriorPlan=false`.\n")
	b.WriteString("- When adopting a prior plan on turn 2+, `candidatePlan` may be null to indicate an exact adoption.\n")
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
	body, err := extractTaggedJSON(output, "plan")
	if err != nil {
		return nil, err
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

// PlanArtifactsEqual reports whether two plan artifacts are structurally equal
// for council convergence purposes.
func PlanArtifactsEqual(a, b *PlanArtifact) bool {
	if a == nil || b == nil {
		return a == b
	}
	if strings.TrimSpace(a.Title) != strings.TrimSpace(b.Title) ||
		strings.TrimSpace(a.Summary) != strings.TrimSpace(b.Summary) ||
		strings.TrimSpace(a.Lineage) != strings.TrimSpace(b.Lineage) {
		return false
	}
	if !planArtifactOwnershipEqual(a.Ownership, b.Ownership) {
		return false
	}
	if len(a.Tasks) != len(b.Tasks) {
		return false
	}
	for i := range a.Tasks {
		if !planArtifactTaskEqual(a.Tasks[i], b.Tasks[i]) {
			return false
		}
	}
	return true
}

func planArtifactOwnershipEqual(a, b *PlanArtifactOwnership) bool {
	if planArtifactOwnershipEmpty(a) && planArtifactOwnershipEmpty(b) {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Exclusive == b.Exclusive &&
		stringSetEqual(a.Packages, b.Packages)
}

func planArtifactOwnershipEmpty(item *PlanArtifactOwnership) bool {
	return item == nil || (!item.Exclusive && len(nonEmptySortedCopy(item.Packages)) == 0)
}

func planArtifactTaskEqual(a, b PlanArtifactTask) bool {
	if strings.TrimSpace(a.ID) != strings.TrimSpace(b.ID) ||
		strings.TrimSpace(a.Title) != strings.TrimSpace(b.Title) ||
		strings.TrimSpace(a.Prompt) != strings.TrimSpace(b.Prompt) ||
		strings.TrimSpace(a.Complexity) != strings.TrimSpace(b.Complexity) ||
		strings.TrimSpace(a.ReviewComplexity) != strings.TrimSpace(b.ReviewComplexity) {
		return false
	}
	if !stringSetEqual(a.Dependencies, b.Dependencies) {
		return false
	}
	if !planArtifactOutputsEqual(a.Outputs, b.Outputs) {
		return false
	}
	return planArtifactSuccessCriteriaEqual(a.SuccessCriteria, b.SuccessCriteria)
}

func planArtifactOutputsEqual(a, b *PlanArtifactOutputs) bool {
	if planArtifactOutputsEmpty(a) && planArtifactOutputsEmpty(b) {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return stringSetEqual(a.Files, b.Files) &&
		stringSetEqual(a.Artifacts, b.Artifacts)
}

func planArtifactOutputsEmpty(item *PlanArtifactOutputs) bool {
	return item == nil || (len(nonEmptySortedCopy(item.Files)) == 0 && len(nonEmptySortedCopy(item.Artifacts)) == 0)
}

func planArtifactSuccessCriteriaEqual(a, b *PlanArtifactSuccessCriteria) bool {
	if planArtifactSuccessCriteriaEmpty(a) && planArtifactSuccessCriteriaEmpty(b) {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return strings.TrimSpace(a.Advisory) == strings.TrimSpace(b.Advisory) &&
		stringSetEqual(a.Verifiable, b.Verifiable)
}

func planArtifactSuccessCriteriaEmpty(item *PlanArtifactSuccessCriteria) bool {
	return item == nil || (strings.TrimSpace(item.Advisory) == "" && len(nonEmptySortedCopy(item.Verifiable)) == 0)
}

func stringSetEqual(a, b []string) bool {
	left := nonEmptySortedCopy(a)
	right := nonEmptySortedCopy(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func nonEmptySortedCopy(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
