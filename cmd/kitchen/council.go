package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const (
	councilContinue      = "continue"
	councilConverged     = "converged"
	councilAutoConverged = "auto_converged"
	councilAskUser       = "ask_user"
	councilReject        = "reject"

	CouncilHardCap = 8
)

func newCouncilSeats() [2]CouncilSeatRecord {
	return [2]CouncilSeatRecord{{Seat: "A"}, {Seat: "B"}}
}

func councilSeatIndex(seat string) int {
	if strings.TrimSpace(seat) == "B" {
		return 1
	}
	return 0
}

func councilSeatForTurn(turn int) string {
	if turn%2 == 0 {
		return "B"
	}
	return "A"
}

func councilTaskID(planID string, turn int) string {
	if turn <= 0 {
		turn = 1
	}
	return fmt.Sprintf("council_%s_t%d", planID, turn)
}

func councilTurnNumberFromTaskID(planID, taskID string) int {
	prefix := "council_" + strings.TrimSpace(planID) + "_t"
	var turn int
	if _, err := fmt.Sscanf(strings.TrimSpace(taskID), prefix+"%d", &turn); err == nil && turn > 0 {
		return turn
	}
	return 0
}

func canAutoApproveCouncil(exec ExecutionRecord) bool {
	return exec.AutoApproved &&
		exec.CouncilFinalDecision == councilConverged &&
		len(exec.CouncilWarnings) == 0
}

func canExtendCouncil(planState string, exec ExecutionRecord) bool {
	if exec.CouncilTurnsCompleted < exec.CouncilMaxTurns {
		return false
	}
	if exec.CouncilTurnsCompleted >= CouncilHardCap {
		return false
	}
	switch exec.CouncilFinalDecision {
	case councilReject:
		return planState == planStateRejected && exec.RejectedBy == rejectedByCouncil
	case councilConverged:
		return planState == planStatePendingApproval && len(exec.CouncilWarnings) > 0 && exec.RejectedBy == ""
	default:
		return false
	}
}

func pendingCouncilQuestionsForPlan(pm *pool.PoolManager, planID string) []pool.Question {
	var questions []pool.Question
	for _, q := range pendingQuestionsForPlan(pm, planID) {
		if strings.TrimSpace(q.Category) != "council" || !q.Blocking {
			continue
		}
		questions = append(questions, q)
	}
	sort.Slice(questions, func(i, j int) bool {
		return questions[i].AskedAt.Before(questions[j].AskedAt)
	})
	return questions
}

func decideCouncilNext(bundle StoredPlan, artifact *adapter.CouncilTurnArtifact) (decision string, warnings []adapter.CouncilDisagreement) {
	for _, question := range artifact.QuestionsForUser {
		if question.Blocking {
			return councilAskUser, nil
		}
	}
	if artifact.Turn >= 2 && artifact.AdoptedPriorPlan && artifact.Stance == "converged" {
		return councilConverged, nil
	}
	if artifact.Turn >= 2 {
		if prev := previousCouncilCandidatePlan(bundle); prev != nil && adapter.PlanArtifactsEqual(prev, artifact.CandidatePlan) {
			var autoWarnings []adapter.CouncilDisagreement
			for _, item := range artifact.Disagreements {
				if strings.TrimSpace(item.Severity) == pool.SeverityCritical {
					autoWarnings = nil
					goto continueNormalFlow
				}
				autoWarnings = append(autoWarnings, item)
			}
			return councilAutoConverged, autoWarnings
		}
	}
continueNormalFlow:
	if bundle.Execution.CouncilTurnsCompleted < bundle.Execution.CouncilMaxTurns {
		return councilContinue, nil
	}
	for _, item := range artifact.Disagreements {
		if strings.TrimSpace(item.Severity) == pool.SeverityCritical {
			return councilReject, nil
		}
	}
	if len(artifact.Disagreements) > 0 {
		return councilConverged, append([]adapter.CouncilDisagreement(nil), artifact.Disagreements...)
	}
	return councilConverged, nil
}

func previousCouncilCandidatePlan(bundle StoredPlan) *adapter.PlanArtifact {
	turns := bundle.Execution.CouncilTurns
	if len(turns) < 2 {
		return nil
	}
	prev := turns[len(turns)-2].Artifact
	if prev == nil {
		return nil
	}
	return prev.CandidatePlan
}

func synthesizeBlockedCouncilArtifact(plan PlanRecord, seat string, turn int, reason string) *adapter.CouncilTurnArtifact {
	candidate := planToArtifact(plan)
	return &adapter.CouncilTurnArtifact{
		Seat:             strings.TrimSpace(seat),
		Turn:             turn,
		Stance:           "blocked",
		CandidatePlan:    candidate,
		AdoptedPriorPlan: false,
		SeatMemo:         strings.TrimSpace(reason),
		Summary:          strings.TrimSpace(reason),
		Disagreements: []adapter.CouncilDisagreement{{
			ID:       fmt.Sprintf("blocked-t%d", turn),
			Severity: pool.SeverityMajor,
			Category: "assumption",
			Title:    "Seat blocked",
			Impact:   strings.TrimSpace(reason),
			Blocking: false,
		}},
	}
}

func planToArtifact(plan PlanRecord) *adapter.PlanArtifact {
	artifact := &adapter.PlanArtifact{
		Lineage: plan.Lineage,
		Title:   plan.Title,
		Summary: plan.Summary,
		Ownership: &adapter.PlanArtifactOwnership{
			Packages:  append([]string(nil), plan.Ownership.Packages...),
			Exclusive: plan.Ownership.Exclusive,
		},
		Tasks: make([]adapter.PlanArtifactTask, 0, len(plan.Tasks)),
	}
	for _, task := range plan.Tasks {
		item := adapter.PlanArtifactTask{
			ID:               task.ID,
			Title:            task.Title,
			Prompt:           task.Prompt,
			Complexity:       string(task.Complexity),
			ReviewComplexity: string(task.ReviewComplexity),
		}
		for _, dep := range task.Dependencies {
			if dep.Task != "" {
				item.Dependencies = append(item.Dependencies, dep.Task)
			}
		}
		if task.Outputs != nil {
			item.Outputs = &adapter.PlanArtifactOutputs{
				Files:     append([]string(nil), task.Outputs.Files...),
				Artifacts: append([]string(nil), task.Outputs.Artifacts...),
			}
		}
		if task.SuccessCriteria != nil {
			item.SuccessCriteria = &adapter.PlanArtifactSuccessCriteria{
				Advisory:   task.SuccessCriteria.Advisory,
				Verifiable: append([]string(nil), task.SuccessCriteria.Verifiable...),
			}
		}
		artifact.Tasks = append(artifact.Tasks, item)
	}
	return artifact
}

func buildCouncilTurnPrompt(bundle StoredPlan, turn int) (string, error) {
	seat := councilSeatForTurn(turn)
	prior := make([]adapter.CouncilTurnArtifact, 0, len(bundle.Execution.CouncilTurns))
	for _, item := range bundle.Execution.CouncilTurns {
		if item.Artifact == nil {
			continue
		}
		prior = append(prior, *item.Artifact)
	}
	idea := strings.TrimSpace(bundle.Plan.Summary)
	if idea == "" {
		idea = strings.TrimSpace(bundle.Plan.Title)
	}
	return adapter.BuildCouncilTurnPrompt(idea, prior, seat, turn, bundle.Plan.Summary), nil
}

func marshalCouncilTurns(turns []CouncilTurnRecord) string {
	if len(turns) == 0 {
		return "[]"
	}
	data, err := json.Marshal(turns)
	if err != nil {
		return "[]"
	}
	return string(data)
}
