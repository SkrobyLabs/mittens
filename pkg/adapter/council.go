package adapter

import (
	"fmt"
	"strings"
)

type CouncilTurnArtifact struct {
	Seat                 string                `json:"seat"`
	Turn                 int                   `json:"turn"`
	Stance               string                `json:"stance"`
	CandidatePlan        *PlanArtifact         `json:"candidatePlan"`
	AdoptedPriorPlan     bool                  `json:"adoptedPriorPlan,omitempty"`
	Disagreements        []CouncilDisagreement `json:"disagreements,omitempty"`
	QuestionsForUser     []CouncilUserQuestion `json:"questionsForUser,omitempty"`
	Strengths            []string              `json:"strengths,omitempty"`
	SeatMemo             string                `json:"seatMemo,omitempty"`
	RejectedAlternatives []string              `json:"rejectedAlternatives,omitempty"`
	Summary              string                `json:"summary,omitempty"`
}

type CouncilDisagreement struct {
	ID              string   `json:"id"`
	Severity        string   `json:"severity"`
	Category        string   `json:"category"`
	Title           string   `json:"title"`
	Impact          string   `json:"impact"`
	Blocking        bool     `json:"blocking,omitempty"`
	TaskIDs         []string `json:"taskIds,omitempty"`
	SuggestedChange string   `json:"suggestedChange,omitempty"`
}

type CouncilUserQuestion struct {
	ID            string `json:"id"`
	Question      string `json:"question"`
	WhyItMatters  string `json:"whyItMatters,omitempty"`
	Blocking      bool   `json:"blocking,omitempty"`
}

func validateCouncilTurnArtifact(artifact *CouncilTurnArtifact) error {
	if artifact == nil {
		return fmt.Errorf("council turn artifact is nil")
	}
	artifact.Seat = strings.TrimSpace(artifact.Seat)
	artifact.Stance = strings.TrimSpace(artifact.Stance)
	artifact.SeatMemo = strings.TrimSpace(artifact.SeatMemo)
	artifact.Summary = strings.TrimSpace(artifact.Summary)
	for i := range artifact.Strengths {
		artifact.Strengths[i] = strings.TrimSpace(artifact.Strengths[i])
	}
	for i := range artifact.RejectedAlternatives {
		artifact.RejectedAlternatives[i] = strings.TrimSpace(artifact.RejectedAlternatives[i])
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
		return fmt.Errorf("council seat must be A or B")
	}
	if artifact.Turn <= 0 {
		return fmt.Errorf("council turn must be > 0")
	}
	switch artifact.Stance {
	case "propose", "revise", "converged", "blocked":
	default:
		return fmt.Errorf("invalid council stance %q", artifact.Stance)
	}
	if artifact.CandidatePlan == nil {
		return fmt.Errorf("candidate plan must not be nil")
	}
	if err := validatePlanArtifact(artifact.CandidatePlan); err != nil {
		return fmt.Errorf("candidate plan: %w", err)
	}
	for _, item := range artifact.Disagreements {
		if item.ID == "" {
			return fmt.Errorf("council disagreement id must not be empty")
		}
		if item.Severity == "" {
			return fmt.Errorf("council disagreement %s severity must not be empty", item.ID)
		}
		if item.Category == "" {
			return fmt.Errorf("council disagreement %s category must not be empty", item.ID)
		}
		if item.Title == "" {
			return fmt.Errorf("council disagreement %s title must not be empty", item.ID)
		}
		if item.Impact == "" {
			return fmt.Errorf("council disagreement %s impact must not be empty", item.ID)
		}
	}
	for _, item := range artifact.QuestionsForUser {
		if item.ID == "" {
			return fmt.Errorf("council user question id must not be empty")
		}
		if item.Question == "" {
			return fmt.Errorf("council user question %s must not be empty", item.ID)
		}
	}
	return nil
}
