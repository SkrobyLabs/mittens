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
	reviewCouncilContinue  = "continue"
	reviewCouncilConverged = "converged"
	reviewCouncilAskUser   = "ask_user"
	reviewCouncilReject    = "reject"

	ReviewCouncilHardCap = 8

	rejectedByCouncil       = "council"
	rejectedByReviewCouncil = "review_council"
)

func newReviewCouncilSeats() [2]CouncilSeatRecord {
	return [2]CouncilSeatRecord{{Seat: "A"}, {Seat: "B"}}
}

func reviewCouncilSeatIndex(seat string) int {
	if strings.TrimSpace(seat) == "B" {
		return 1
	}
	return 0
}

func reviewCouncilSeatForTurn(turn int) string {
	if turn%2 == 0 {
		return "B"
	}
	return "A"
}

func reviewCouncilTaskID(planID string, turn int) string {
	if turn <= 0 {
		turn = 1
	}
	return fmt.Sprintf("review_council_%s_t%d", planID, turn)
}

func reviewCouncilTurnNumberFromTaskID(planID, taskID string) int {
	prefix := "review_council_" + strings.TrimSpace(planID) + "_t"
	var turn int
	if _, err := fmt.Sscanf(strings.TrimSpace(taskID), prefix+"%d", &turn); err == nil && turn > 0 {
		return turn
	}
	return 0
}

func isReviewCouncilTask(task pool.Task) bool {
	return task.PlanID != "" && reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID) > 0
}

func pendingReviewCouncilQuestionsForPlan(pm *pool.PoolManager, planID string) []pool.Question {
	var questions []pool.Question
	for _, q := range pendingQuestionsForPlan(pm, planID) {
		// Use a distinct review_council category so operators can filter planning
		// and review questions separately without coupling future category reuse.
		if strings.TrimSpace(q.Category) != "review_council" || !q.Blocking {
			continue
		}
		questions = append(questions, q)
	}
	sort.Slice(questions, func(i, j int) bool {
		return questions[i].AskedAt.Before(questions[j].AskedAt)
	})
	return questions
}

func canExtendReviewCouncil(planState string, exec ExecutionRecord) bool {
	if exec.ReviewCouncilTurnsCompleted < exec.ReviewCouncilMaxTurns {
		return false
	}
	if exec.ReviewCouncilTurnsCompleted >= ReviewCouncilHardCap {
		return false
	}
	switch exec.ReviewCouncilFinalDecision {
	case reviewCouncilReject:
		return planState == planStateRejected && exec.RejectedBy == rejectedByReviewCouncil
	default:
		return false
	}
}

func decideReviewCouncilNext(bundle StoredPlan, artifact *adapter.ReviewCouncilTurnArtifact) (decision string, warnings []adapter.CouncilDisagreement) {
	for _, question := range artifact.QuestionsForUser {
		if question.Blocking {
			return reviewCouncilAskUser, nil
		}
	}
	if artifact.Turn >= 2 && artifact.AdoptedPriorVerdict && artifact.Stance == "converged" {
		if prev := previousReviewCouncilTurn(bundle); prev != nil && strings.TrimSpace(prev.Verdict) == strings.TrimSpace(artifact.Verdict) {
			if hasCriticalReviewCouncilDisagreement(artifact.Disagreements) && bundle.Execution.ReviewCouncilTurnsCompleted >= bundle.Execution.ReviewCouncilMaxTurns {
				return reviewCouncilReject, nil
			}
			if len(artifact.Disagreements) > 0 {
				return reviewCouncilConverged, append([]adapter.CouncilDisagreement(nil), artifact.Disagreements...)
			}
			return reviewCouncilConverged, nil
		}
		return reviewCouncilContinue, nil
	}
	if bundle.Execution.ReviewCouncilTurnsCompleted < bundle.Execution.ReviewCouncilMaxTurns {
		return reviewCouncilContinue, nil
	}
	prev := previousReviewCouncilTurn(bundle)
	if prev == nil || strings.TrimSpace(prev.Verdict) != strings.TrimSpace(artifact.Verdict) {
		return reviewCouncilReject, nil
	}
	if hasCriticalReviewCouncilDisagreement(artifact.Disagreements) {
		return reviewCouncilReject, nil
	}
	if len(artifact.Disagreements) > 0 {
		return reviewCouncilConverged, append([]adapter.CouncilDisagreement(nil), artifact.Disagreements...)
	}
	return reviewCouncilConverged, nil
}

func hasCriticalReviewCouncilDisagreement(items []adapter.CouncilDisagreement) bool {
	for _, item := range items {
		if strings.TrimSpace(item.Severity) == pool.SeverityCritical {
			return true
		}
	}
	return false
}

func previousReviewCouncilTurn(bundle StoredPlan) *adapter.ReviewCouncilTurnArtifact {
	turns := bundle.Execution.ReviewCouncilTurns
	if len(turns) < 2 {
		return nil
	}
	prev := turns[len(turns)-2].Artifact
	if prev == nil {
		return nil
	}
	return prev
}

func synthesizeBlockedReviewCouncilArtifact(seat string, turn int, reason string) *adapter.ReviewCouncilTurnArtifact {
	return &adapter.ReviewCouncilTurnArtifact{
		Seat:                strings.TrimSpace(seat),
		Turn:                turn,
		Stance:              "blocked",
		Verdict:             "fail",
		AdoptedPriorVerdict: false,
		Summary:             strings.TrimSpace(reason),
		SeatMemo:            strings.TrimSpace(reason),
		Disagreements: []adapter.CouncilDisagreement{{
			ID:       fmt.Sprintf("blocked-t%d", turn),
			Severity: pool.SeverityMajor,
			Category: "assumption",
			Title:    "Seat blocked",
			Impact:   strings.TrimSpace(reason),
		}},
	}
}

func buildReviewCouncilTurnPrompt(bundle StoredPlan, turn int) (string, error) {
	seat := reviewCouncilSeatForTurn(turn)
	prior := make([]adapter.ReviewCouncilTurnArtifact, 0, len(bundle.Execution.ReviewCouncilTurns))
	for _, item := range bundle.Execution.ReviewCouncilTurns {
		if item.Artifact == nil {
			continue
		}
		prior = append(prior, *item.Artifact)
	}
	tasksJSON, err := marshalReviewCouncilTasks(bundle.Plan.Tasks)
	if err != nil {
		return "", err
	}
	diffBase := strings.TrimSpace(bundle.Execution.Anchor.Commit)
	if diffBase == "" {
		diffBase = strings.TrimSpace(bundle.Plan.Anchor.Commit)
	}
	if diffBase == "" {
		diffBase = "HEAD~1"
	}
	return adapter.BuildReviewCouncilTurnPrompt(bundle.Plan.Title, bundle.Plan.Summary, tasksJSON, seat, turn, diffBase, prior), nil
}

func marshalReviewCouncilTasks(tasks []PlanTask) (string, error) {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal review council tasks: %w", err)
	}
	return string(data), nil
}
