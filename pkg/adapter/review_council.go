package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ReviewFinding struct {
	ID          string `json:"id"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion,omitempty"`
	Severity    string `json:"severity"`
}

type ReviewCouncilTurnArtifact struct {
	Seat                 string                `json:"seat"`
	Turn                 int                   `json:"turn"`
	Stance               string                `json:"stance"`
	Verdict              string                `json:"verdict"`
	AdoptedPriorVerdict  bool                  `json:"adoptedPriorVerdict,omitempty"`
	Findings             []ReviewFinding       `json:"findings,omitempty"`
	Disagreements        []CouncilDisagreement `json:"disagreements,omitempty"`
	QuestionsForUser     []CouncilUserQuestion `json:"questionsForUser,omitempty"`
	Strengths            []string              `json:"strengths,omitempty"`
	SeatMemo             string                `json:"seatMemo,omitempty"`
	RejectedAlternatives []string              `json:"rejectedAlternatives,omitempty"`
	Summary              string                `json:"summary,omitempty"`
}

func validateReviewCouncilTurnArtifact(artifact *ReviewCouncilTurnArtifact) error {
	if artifact == nil {
		return fmt.Errorf("review council turn artifact is nil")
	}
	artifact.Seat = strings.TrimSpace(artifact.Seat)
	artifact.Stance = strings.TrimSpace(artifact.Stance)
	artifact.Verdict = strings.TrimSpace(artifact.Verdict)
	artifact.SeatMemo = strings.TrimSpace(artifact.SeatMemo)
	artifact.Summary = strings.TrimSpace(artifact.Summary)
	for i := range artifact.Strengths {
		artifact.Strengths[i] = strings.TrimSpace(artifact.Strengths[i])
	}
	for i := range artifact.RejectedAlternatives {
		artifact.RejectedAlternatives[i] = strings.TrimSpace(artifact.RejectedAlternatives[i])
	}
	for i := range artifact.Findings {
		item := &artifact.Findings[i]
		item.ID = strings.TrimSpace(item.ID)
		item.File = strings.TrimSpace(item.File)
		item.Category = strings.TrimSpace(item.Category)
		item.Description = strings.TrimSpace(item.Description)
		item.Suggestion = strings.TrimSpace(item.Suggestion)
		item.Severity = strings.TrimSpace(item.Severity)
	}
	for i := range artifact.Disagreements {
		item := &artifact.Disagreements[i]
		item.ID = strings.TrimSpace(item.ID)
		item.Severity = strings.TrimSpace(item.Severity)
		item.Category = strings.TrimSpace(item.Category)
		item.Title = strings.TrimSpace(item.Title)
		item.Impact = strings.TrimSpace(item.Impact)
		item.SuggestedChange = strings.TrimSpace(item.SuggestedChange)
		for j := range item.TaskIDs {
			item.TaskIDs[j] = strings.TrimSpace(item.TaskIDs[j])
		}
	}
	for i := range artifact.QuestionsForUser {
		item := &artifact.QuestionsForUser[i]
		item.ID = strings.TrimSpace(item.ID)
		item.Question = strings.TrimSpace(item.Question)
		item.WhyItMatters = strings.TrimSpace(item.WhyItMatters)
	}
	if artifact.Seat != "A" && artifact.Seat != "B" {
		return fmt.Errorf("review council seat must be A or B")
	}
	if artifact.Turn <= 0 {
		return fmt.Errorf("review council turn must be > 0")
	}
	switch artifact.Stance {
	case "propose", "revise", "converged", "blocked":
	default:
		return fmt.Errorf("invalid review council stance %q", artifact.Stance)
	}
	switch artifact.Verdict {
	case "pass", "fail":
	default:
		return fmt.Errorf("invalid review council verdict %q", artifact.Verdict)
	}
	for _, item := range artifact.Findings {
		if item.ID == "" {
			return fmt.Errorf("review finding id must not be empty")
		}
		if item.Category == "" {
			return fmt.Errorf("review finding %s category must not be empty", item.ID)
		}
		if item.Description == "" {
			return fmt.Errorf("review finding %s description must not be empty", item.ID)
		}
		if item.Severity == "" {
			return fmt.Errorf("review finding %s severity must not be empty", item.ID)
		}
		if item.Line < 0 {
			return fmt.Errorf("review finding %s line must be >= 0", item.ID)
		}
	}
	for _, item := range artifact.Disagreements {
		if item.ID == "" {
			return fmt.Errorf("review council disagreement id must not be empty")
		}
		if item.Severity == "" {
			return fmt.Errorf("review council disagreement %s severity must not be empty", item.ID)
		}
		if item.Category == "" {
			return fmt.Errorf("review council disagreement %s category must not be empty", item.ID)
		}
		if item.Title == "" {
			return fmt.Errorf("review council disagreement %s title must not be empty", item.ID)
		}
		if item.Impact == "" {
			return fmt.Errorf("review council disagreement %s impact must not be empty", item.ID)
		}
	}
	for _, item := range artifact.QuestionsForUser {
		if item.ID == "" {
			return fmt.Errorf("review council user question id must not be empty")
		}
		if item.Question == "" {
			return fmt.Errorf("review council user question %s must not be empty", item.ID)
		}
	}
	return nil
}

const reviewCouncilTurnSuffix = `

At the end of your response, output a review council turn block with valid JSON:
<review_council_turn>
{
  "seat": "A|B",
  "turn": 1,
  "stance": "propose|revise|converged|blocked",
  "verdict": "pass|fail",
  "adoptedPriorVerdict": false,
  "findings": [
    {
      "id": "f1",
      "file": "path/to/file.go",
      "line": 12,
      "category": "correctness|coverage|resilience|api|security",
      "description": "Concrete finding",
      "suggestion": "Specific fix",
      "severity": "minor|major|critical"
    }
  ],
  "disagreements": [
    {
      "id": "d1",
      "severity": "major|critical",
      "category": "correctness|coverage|resilience|api|security",
      "title": "Short title",
      "impact": "Why this matters",
      "blocking": true,
      "suggestedChange": "Optional fix direction"
    }
  ],
  "questionsForUser": [
    {
      "id": "q1",
      "question": "Question text",
      "whyItMatters": "How the answer changes the review",
      "blocking": true
    }
  ],
  "strengths": ["Short strengths"],
  "seatMemo": "Compact reasoning summary for fallback rehydration",
  "rejectedAlternatives": ["Discarded option and why"],
  "summary": "Overall seat summary"
}
</review_council_turn>

Return only valid JSON inside <review_council_turn>.`

func BuildReviewCouncilTurnPrompt(planTitle, planSummary, tasksJSON, seat string, turn int, diffBase string, prior []ReviewCouncilTurnArtifact) string {
	var b strings.Builder
	b.WriteString("## Review Council Turn\n\n")
	b.WriteString("You are one seat in a two-seat implementation review council. Review the implemented code and produce a full review artifact each turn.\n\n")
	b.WriteString("### Council Rules\n\n")
	b.WriteString("- Seats alternate A then B.\n")
	b.WriteString("- Review the actual code diff before deciding.\n")
	b.WriteString("- Convergence requires verdict agreement across seats: pass/pass or fail/fail.\n")
	b.WriteString("- Use `adoptedPriorVerdict=true` only when you intentionally adopt the prior seat's verdict after reviewing its artifact.\n")
	b.WriteString("- Use `stance=converged` only when you are adopting the prior verdict and believe the council has converged.\n")
	b.WriteString("- If you adopt the prior verdict but still disagree on the outcome, keep `stance=revise` or `propose`; do not claim convergence.\n")
	b.WriteString("- Use `questionsForUser` only for genuinely blocking operator input.\n")
	b.WriteString("- This replaces the legacy single-pass plan-level implementation review. Per-task inline reviews are unchanged.\n\n")
	b.WriteString("### Current Turn\n\n")
	b.WriteString(fmt.Sprintf("Seat: %s\nTurn: %d\n\n", strings.TrimSpace(seat), turn))
	b.WriteString("### Plan Title\n\n")
	b.WriteString(strings.TrimSpace(planTitle))
	b.WriteString("\n\n")
	if strings.TrimSpace(planSummary) != "" {
		b.WriteString("### Plan Summary\n\n")
		b.WriteString(strings.TrimSpace(planSummary))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(tasksJSON) != "" {
		b.WriteString("### Task List\n\n```json\n")
		b.WriteString(strings.TrimSpace(tasksJSON))
		b.WriteString("\n```\n\n")
	}
	base := strings.TrimSpace(diffBase)
	if base == "" {
		base = "HEAD~1"
	}
	b.WriteString("### Diff Command\n\n")
	b.WriteString(fmt.Sprintf("Run `git diff %s` to inspect the implementation under review.\n\n", base))
	if len(prior) > 0 {
		b.WriteString("### Prior Turn Chain\n\n")
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
	b.WriteString(reviewCouncilTurnSuffix)
	return b.String()
}

func ExtractReviewCouncilTurnArtifact(output string) (*ReviewCouncilTurnArtifact, error) {
	body, err := extractTaggedJSON(output, "review_council_turn")
	if err != nil {
		return nil, err
	}
	var artifact ReviewCouncilTurnArtifact
	if err := json.Unmarshal([]byte(body), &artifact); err != nil {
		return nil, fmt.Errorf("decode review council turn JSON: %w", err)
	}
	if err := validateReviewCouncilTurnArtifact(&artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}
