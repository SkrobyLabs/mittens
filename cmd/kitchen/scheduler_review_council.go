package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func (s *Scheduler) reservedReviewCouncilWorkerIDs() map[string]string {
	reserved := make(map[string]string)
	if s == nil || s.plans == nil {
		return reserved
	}
	plans, err := s.plans.List()
	if err != nil {
		return reserved
	}
	for _, plan := range plans {
		bundle, err := s.plans.Get(plan.PlanID)
		if err != nil {
			continue
		}
		for _, seat := range bundle.Execution.ReviewCouncilSeats {
			workerID := strings.TrimSpace(seat.WorkerID)
			if workerID == "" {
				continue
			}
			reserved[workerID] = bundle.Plan.PlanID + ":" + seat.Seat
		}
	}
	return reserved
}

func (s *Scheduler) reservedWorkerIDs() map[string]string {
	reserved := s.reservedCouncilWorkerIDs()
	for workerID, reason := range s.reservedReviewCouncilWorkerIDs() {
		reserved[workerID] = reason
	}
	return reserved
}

func (s *Scheduler) workerCanRunReviewCouncilTask(worker pool.Worker, task pool.Task) (bool, bool) {
	if task.Role != "reviewer" || task.PlanID == "" {
		return false, false
	}
	turn := reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID)
	if turn <= 0 || s == nil || s.plans == nil {
		return false, false
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return false, true
	}
	keys := s.routeKeysForTask(task)
	if len(keys) == 0 {
		return s.workerCanRunTaskWithoutCouncilAffinity(worker, task), true
	}
	seat := bundle.Execution.ReviewCouncilSeats[reviewCouncilSeatIndex(reviewCouncilSeatForTurn(turn))]
	targetWorkerID := strings.TrimSpace(seat.WorkerID)
	if reserved, ok := s.reservedReviewCouncilWorkerIDs()[worker.ID]; ok && reserved != bundle.Plan.PlanID+":"+seat.Seat {
		return false, true
	}
	if targetWorkerID != "" {
		if worker.ID != targetWorkerID {
			return false, true
		}
		return workerMatchesAnyRouteKey(worker, keys), true
	}
	return workerMatchesAnyRouteKey(worker, keys), true
}

func (s *Scheduler) enqueueReviewCouncilTurn(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	turn := bundle.Execution.ReviewCouncilTurnsCompleted + 1
	if turn > bundle.Execution.ReviewCouncilMaxTurns {
		return nil
	}
	taskID := reviewCouncilTaskID(bundle.Plan.PlanID, turn)
	if _, exists := s.pm.Task(taskID); !exists {
		prompt, err := buildReviewCouncilTurnPrompt(bundle, turn)
		if err != nil {
			return err
		}
		if _, err := s.pm.EnqueueTask(pool.TaskSpec{
			ID:         taskID,
			PlanID:     bundle.Plan.PlanID,
			Prompt:     prompt,
			Complexity: string(implementationReviewComplexityForPlan(bundle.Plan)),
			Priority:   len(bundle.Plan.Tasks) + 1,
			Role:       "reviewer",
		}); err != nil {
			return err
		}
	}
	bundle.Plan.State = planStateImplementationReview
	bundle.Execution.State = planStateImplementationReview
	bundle.Execution.ActiveTaskIDs = []string{taskID}
	idx := reviewCouncilSeatIndex(reviewCouncilSeatForTurn(turn))
	seat := bundle.Execution.ReviewCouncilSeats[idx]
	if seat.Seat == "" {
		seat.Seat = reviewCouncilSeatForTurn(turn)
	}
	if workerID := strings.TrimSpace(seat.WorkerID); workerID != "" {
		if worker, ok := s.pm.Worker(workerID); ok && worker.Status != pool.WorkerDead && s.seatWorkerMatchesRoute(*worker, pool.Task{
			ID:         taskID,
			PlanID:     bundle.Plan.PlanID,
			Complexity: string(implementationReviewComplexityForPlan(bundle.Plan)),
			Role:       "reviewer",
		}) {
			if worker.Status == pool.WorkerIdle {
				if err := s.pm.DispatchTask(taskID, workerID); err != nil {
					return err
				}
			}
		} else {
			s.invalidateReviewCouncilSeat(&bundle, idx, turn, taskID)
			seat = bundle.Execution.ReviewCouncilSeats[idx]
		}
	}
	bundle.Execution.ReviewCouncilSeats[idx] = seat
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	return s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution)
}

func (s *Scheduler) onReviewCouncilTurnCompleted(task pool.Task) error {
	if s == nil || s.plans == nil || s.pm == nil || task.PlanID == "" {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(pool.WorkerStatePath(s.pm.StateDir(), task.WorkerID, pool.WorkerResultFile))
	if err != nil {
		return s.handleReviewCouncilArtifactFailure(task, bundle, fmt.Sprintf("read review council artifact: %v", err))
	}
	artifact, err := adapter.ExtractReviewCouncilTurnArtifact(string(raw))
	if err != nil {
		return s.handleReviewCouncilArtifactFailure(task, bundle, fmt.Sprintf("decode review council artifact: %v", err))
	}
	if err := validateReviewCouncilTurnAgainstTask(task, artifact); err != nil {
		return s.handleReviewCouncilArtifactFailure(task, bundle, err.Error())
	}
	return s.applyReviewCouncilTurnResult(task, bundle, artifact)
}

func validateReviewCouncilTurnAgainstTask(task pool.Task, artifact *adapter.ReviewCouncilTurnArtifact) error {
	if artifact == nil {
		return fmt.Errorf("review council artifact is nil")
	}
	turn := reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID)
	if turn > 0 && artifact.Turn != turn {
		return fmt.Errorf("review council artifact turn %d does not match task turn %d", artifact.Turn, turn)
	}
	seat := reviewCouncilSeatForTurn(turn)
	if seat != "" && artifact.Seat != seat {
		return fmt.Errorf("review council artifact seat %s does not match task seat %s", artifact.Seat, seat)
	}
	return nil
}

func (s *Scheduler) handleReviewCouncilArtifactFailure(task pool.Task, bundle StoredPlan, message string) error {
	if task.RetryCount < 1 {
		if err := s.pm.FailCompletedTask(task.ID, message); err != nil {
			return err
		}
		if err := s.pm.ReviveFailedTask(task.ID, false); err != nil {
			return err
		}
		bundle.Execution.ActiveTaskIDs = []string{task.ID}
		return s.plans.UpdateExecution(task.PlanID, bundle.Execution)
	}
	turn := reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID)
	artifact := synthesizeBlockedReviewCouncilArtifact(reviewCouncilSeatForTurn(turn), turn, message)
	return s.applyReviewCouncilTurnResult(task, bundle, artifact)
}

func (s *Scheduler) applyReviewCouncilTurnResult(task pool.Task, bundle StoredPlan, artifact *adapter.ReviewCouncilTurnArtifact) error {
	now := time.Now().UTC()
	bundle.Execution.ReviewCouncilTurnsCompleted = artifact.Turn
	bundle.Execution.ReviewCouncilTurns = append(bundle.Execution.ReviewCouncilTurns, ReviewCouncilTurnRecord{
		Seat:       artifact.Seat,
		Turn:       artifact.Turn,
		Artifact:   artifact,
		OccurredAt: now,
	})
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = appendUniqueIDs(bundle.Execution.CompletedTaskIDs, task.ID)
	bundle.Execution.FailedTaskIDs = nil
	bundle.Execution.ReviewCouncilAwaitingAnswers = false
	idx := reviewCouncilSeatIndex(artifact.Seat)
	seat := bundle.Execution.ReviewCouncilSeats[idx]
	if seat.Seat == "" {
		seat.Seat = artifact.Seat
	}
	seat.WorkerID = task.WorkerID
	seat.IdleSince = &now
	if worker, ok := s.pm.Worker(task.WorkerID); ok {
		seat.PoolKey = PoolKey{Provider: worker.Provider, Model: worker.Model, Adapter: worker.Adapter}
	}
	bundle.Execution.ReviewCouncilSeats[idx] = seat
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryReviewCouncilTurnCompleted,
		Cycle:   artifact.Turn,
		TaskID:  task.ID,
		Summary: firstNonEmpty(strings.TrimSpace(artifact.Summary), "Review council turn completed."),
	})

	decision, warnings := decideReviewCouncilNext(bundle, artifact)
	switch decision {
	case reviewCouncilAskUser:
		if err := s.seedReviewCouncilQuestions(task, artifact.QuestionsForUser); err != nil {
			return err
		}
		bundle.Plan.State = planStateImplementationReview
		bundle.Execution.State = planStateImplementationReview
		bundle.Execution.ReviewCouncilAwaitingAnswers = true
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryReviewCouncilWaitingAnswers,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Review council waiting for operator answers.",
		})
	case reviewCouncilContinue:
		bundle.Plan.State = planStateImplementationReview
		bundle.Execution.State = planStateImplementationReview
	case reviewCouncilConverged:
		bundle.Execution.ReviewCouncilFinalDecision = reviewCouncilConverged
		bundle.Execution.ReviewCouncilWarnings = append([]adapter.CouncilDisagreement(nil), warnings...)
		bundle.Execution.ReviewCouncilUnresolvedDisagreements = nil
		bundle.Execution.ReviewCouncilSeats = newReviewCouncilSeats()
		bundle.Execution.ImplReviewedAt = &now
		bundle.Plan.State = planStateCompleted
		bundle.Execution.State = planStateCompleted
		bundle.Execution.CompletedAt = &now
		bundle.Execution.ActiveTaskIDs = nil
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryReviewCouncilConverged,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: fmt.Sprintf("Review council converged on %s.", artifact.Verdict),
		})
		if strings.TrimSpace(artifact.Verdict) == pool.ReviewPass {
			bundle.Execution.ImplReviewStatus = planReviewStatusPassed
			bundle.Execution.ImplReviewFindings = nil
			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:    planHistoryImplReviewPassed,
				Cycle:   artifact.Turn,
				TaskID:  task.ID,
				Verdict: artifact.Verdict,
			})
		} else {
			bundle.Execution.ImplReviewStatus = planReviewStatusFailed
			bundle.Execution.ImplReviewFindings = reviewCouncilFindingsToStrings(artifact.Findings, artifact.Disagreements)
			if len(bundle.Execution.ImplReviewFindings) == 0 {
				bundle.Execution.ImplReviewFindings = []string{"Implementation review failed."}
			}
			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:     planHistoryImplReviewFailed,
				Cycle:    artifact.Turn,
				TaskID:   task.ID,
				Verdict:  artifact.Verdict,
				Findings: append([]string(nil), bundle.Execution.ImplReviewFindings...),
			})
		}
	case reviewCouncilReject:
		rejectArtifact := artifact
		if prev := previousReviewCouncilTurn(bundle); prev != nil && strings.TrimSpace(prev.Verdict) != strings.TrimSpace(artifact.Verdict) {
			rejectArtifact = &adapter.ReviewCouncilTurnArtifact{
				Seat:                artifact.Seat,
				Turn:                artifact.Turn,
				Stance:              artifact.Stance,
				Verdict:             pool.ReviewFail,
				AdoptedPriorVerdict: artifact.AdoptedPriorVerdict,
				Findings:            append([]adapter.ReviewFinding(nil), artifact.Findings...),
				Disagreements: append(append([]adapter.CouncilDisagreement(nil), artifact.Disagreements...), adapter.CouncilDisagreement{
					ID:       fmt.Sprintf("hard-cap-t%d", artifact.Turn),
					Severity: pool.SeverityMajor,
					Category: "correctness",
					Title:    "Review council hit hard cap without verdict agreement",
					Impact:   "The council reached its maximum turns without verdict agreement, so the implementation must be rejected conservatively.",
				}),
				QuestionsForUser:     append([]adapter.CouncilUserQuestion(nil), artifact.QuestionsForUser...),
				Strengths:            append([]string(nil), artifact.Strengths...),
				SeatMemo:             artifact.SeatMemo,
				RejectedAlternatives: append([]string(nil), artifact.RejectedAlternatives...),
				Summary:              artifact.Summary,
			}
		}
		bundle.Plan.State = planStateRejected
		bundle.Execution.State = planStateRejected
		bundle.Execution.ReviewCouncilFinalDecision = reviewCouncilReject
		bundle.Execution.ReviewCouncilWarnings = nil
		bundle.Execution.ReviewCouncilUnresolvedDisagreements = append([]adapter.CouncilDisagreement(nil), rejectArtifact.Disagreements...)
		bundle.Execution.ReviewCouncilSeats = newReviewCouncilSeats()
		bundle.Execution.RejectedBy = rejectedByReviewCouncil
		bundle.Execution.ImplReviewStatus = planReviewStatusFailed
		bundle.Execution.ImplReviewFindings = reviewCouncilFindingsToStrings(rejectArtifact.Findings, rejectArtifact.Disagreements)
		if len(bundle.Execution.ImplReviewFindings) == 0 {
			bundle.Execution.ImplReviewFindings = []string{"Implementation review failed."}
		}
		bundle.Execution.ImplReviewedAt = &now
		bundle.Execution.ActiveTaskIDs = nil
		bundle.Execution.CompletedAt = nil
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryReviewCouncilRejected,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Review council rejected the implementation review.",
		})
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:     planHistoryImplReviewFailed,
			Cycle:    artifact.Turn,
			TaskID:   task.ID,
			Verdict:  pool.ReviewFail,
			Findings: append([]string(nil), bundle.Execution.ImplReviewFindings...),
		})
	}
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	switch decision {
	case reviewCouncilAskUser:
		return nil
	case reviewCouncilContinue:
		if s.notify != nil {
			s.notify(pool.Notification{Type: "plan_review_council_turn_completed", ID: task.PlanID, Message: fmt.Sprintf("Seat %s: %s", artifact.Seat, artifact.Verdict)})
		}
		return s.enqueueReviewCouncilTurn(bundle)
	case reviewCouncilConverged:
		if s.notify != nil {
			s.notify(pool.Notification{Type: "plan_review_council_converged", ID: task.PlanID, Message: artifact.Verdict})
			if strings.TrimSpace(artifact.Verdict) == pool.ReviewPass {
				s.notify(pool.Notification{Type: "plan_impl_review_passed", ID: task.PlanID, Message: bundle.Plan.Title})
			} else {
				s.notify(pool.Notification{Type: "plan_impl_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
			}
		}
		if err := s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage); err != nil {
			return err
		}
		if strings.TrimSpace(artifact.Verdict) == pool.ReviewPass {
			return s.syncPlanExecution(task.PlanID)
		}
		return nil
	case reviewCouncilReject:
		if s.notify != nil {
			s.notify(pool.Notification{Type: "plan_review_council_rejected", ID: task.PlanID, Message: bundle.Plan.Title})
			s.notify(pool.Notification{Type: "plan_impl_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
		}
		return s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage)
	default:
		return nil
	}
}

func reviewCouncilFindingsToStrings(findings []adapter.ReviewFinding, disagreements []adapter.CouncilDisagreement) []string {
	out := make([]string, 0, len(findings)+len(disagreements))
	for _, finding := range findings {
		parts := make([]string, 0, 3)
		if sev := strings.TrimSpace(finding.Severity); sev != "" {
			parts = append(parts, "["+sev+"]")
		}
		location := strings.TrimSpace(finding.File)
		if location != "" && finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		if location != "" {
			parts = append(parts, location)
		}
		if category := strings.TrimSpace(finding.Category); category != "" {
			parts = append(parts, category)
		}
		prefix := strings.Join(parts, " ")
		desc := strings.TrimSpace(finding.Description)
		if prefix != "" && desc != "" {
			out = append(out, prefix+" - "+desc)
		} else if prefix != "" {
			out = append(out, prefix)
		} else if desc != "" {
			out = append(out, desc)
		}
	}
	for _, item := range disagreements {
		label := "[disagreement"
		if sev := strings.TrimSpace(item.Severity); sev != "" {
			label += "/" + sev
		}
		label += "]"
		text := strings.TrimSpace(item.Title)
		if impact := strings.TrimSpace(item.Impact); impact != "" {
			if text != "" {
				text += " - " + impact
			} else {
				text = impact
			}
		}
		out = append(out, strings.TrimSpace(label+" "+text))
	}
	return out
}

func (s *Scheduler) seedReviewCouncilQuestions(task pool.Task, questions []adapter.CouncilUserQuestion) error {
	if s == nil || s.pm == nil || task.WorkerID == "" || len(questions) == 0 {
		return nil
	}
	for _, question := range questions {
		text := strings.TrimSpace(question.Question)
		if text == "" {
			continue
		}
		if _, err := s.pm.AskQuestion(task.WorkerID, pool.Question{
			TaskID:   task.ID,
			Question: text,
			Category: "review_council",
			Context:  strings.TrimSpace(question.WhyItMatters),
			Blocking: question.Blocking,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) onReviewCouncilTurnFailed(task pool.Task, class FailureClass) error {
	if s == nil || s.plans == nil || task.PlanID == "" {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	switch class {
	case FailureEnvironment, FailureInfrastructure, FailureAuth:
		if err := s.pm.ReviveFailedTask(task.ID, false); err != nil {
			return err
		}
		bundle.Plan.State = planStateImplementationReview
		bundle.Execution.State = planStateImplementationReview
		bundle.Execution.ActiveTaskIDs = []string{task.ID}
		if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
			return err
		}
		return s.plans.UpdateExecution(task.PlanID, bundle.Execution)
	case FailurePlan, FailureCapability, FailureTimeout:
		turn := reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID)
		msg := "review council seat blocked"
		if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
			msg = strings.TrimSpace(task.Result.Error)
		}
		return s.applyReviewCouncilTurnResult(task, bundle, synthesizeBlockedReviewCouncilArtifact(reviewCouncilSeatForTurn(turn), turn, msg))
	default:
		message := "review council task failed"
		if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
			message = strings.TrimSpace(task.Result.Error)
		}
		return s.markReviewCouncilFailed(task, message)
	}
}

func (s *Scheduler) markReviewCouncilFailed(task pool.Task, message string) error {
	if s == nil || s.plans == nil || task.PlanID == "" {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	bundle.Plan.State = planStateCompleted
	bundle.Execution.State = planStateCompleted
	bundle.Execution.CompletedAt = &now
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.ImplReviewStatus = planReviewStatusFailed
	bundle.Execution.ImplReviewFindings = []string{strings.TrimSpace(message)}
	bundle.Execution.ImplReviewedAt = &now
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     planHistoryImplReviewFailed,
		Cycle:    reviewCouncilTurnNumberFromTaskID(task.PlanID, task.ID),
		TaskID:   task.ID,
		Verdict:  pool.ReviewFail,
		Findings: append([]string(nil), bundle.Execution.ImplReviewFindings...),
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_impl_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
	}
	return s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage)
}

func (s *Scheduler) recoverReviewCouncilPlansOnStartup() error {
	if s == nil || s.plans == nil || s.pm == nil {
		return nil
	}
	plans, err := s.plans.List()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		bundle, err := s.plans.Get(plan.PlanID)
		if err != nil {
			continue
		}
		if bundle.Execution.State != planStateImplementationReview {
			continue
		}
		if recovered, err := s.recoverLegacyImplementationReviewTerminalTask(bundle); err != nil {
			return err
		} else if recovered {
			continue
		}
		if err := s.migrateLegacyImplementationReviewPlan(&bundle); err != nil {
			return err
		}
		if bundle.Execution.ReviewCouncilAwaitingAnswers && len(pendingReviewCouncilQuestionsForPlan(s.pm, bundle.Plan.PlanID)) == 0 {
			if err := s.enqueueReviewCouncilResumeIfReady(bundle); err != nil {
				return err
			}
			bundle, err = s.plans.Get(plan.PlanID)
			if err != nil {
				continue
			}
		}
		nextTurn := bundle.Execution.ReviewCouncilTurnsCompleted + 1
		nextTaskID := reviewCouncilTaskID(bundle.Plan.PlanID, nextTurn)
		if bundle.Execution.ReviewCouncilFinalDecision == "" &&
			bundle.Execution.ReviewCouncilTurnsCompleted < bundle.Execution.ReviewCouncilMaxTurns &&
			!bundle.Execution.ReviewCouncilAwaitingAnswers &&
			len(bundle.Execution.ActiveTaskIDs) == 0 {
			task, exists := s.pm.Task(nextTaskID)
			if !exists {
				if err := s.enqueueReviewCouncilTurn(bundle); err != nil {
					return err
				}
				bundle, err = s.plans.Get(plan.PlanID)
				if err != nil {
					continue
				}
			} else if task.Status == pool.TaskCanceled {
				if err := s.pm.ReviveCanceledTask(nextTaskID, false); err != nil {
					return err
				}
				if err := s.enqueueReviewCouncilTurn(bundle); err != nil {
					return err
				}
				bundle, err = s.plans.Get(plan.PlanID)
				if err != nil {
					continue
				}
			}
		}
		changedSeats := false
		for i := range bundle.Execution.ReviewCouncilSeats {
			workerID := strings.TrimSpace(bundle.Execution.ReviewCouncilSeats[i].WorkerID)
			if workerID == "" {
				continue
			}
			worker, ok := s.pm.Worker(workerID)
			if ok && worker.Status != pool.WorkerDead && s.seatWorkerMatchesRoute(*worker, pool.Task{
				ID:         reviewCouncilTaskID(bundle.Plan.PlanID, bundle.Execution.ReviewCouncilTurnsCompleted+1),
				PlanID:     bundle.Plan.PlanID,
				Complexity: string(implementationReviewComplexityForPlan(bundle.Plan)),
				Role:       "reviewer",
			}) {
				continue
			}
			s.invalidateReviewCouncilSeat(&bundle, i, bundle.Execution.ReviewCouncilTurnsCompleted+1, "")
			changedSeats = true
		}
		if changedSeats {
			if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) recoverLegacyImplementationReviewTerminalTask(bundle StoredPlan) (bool, error) {
	if s == nil || s.pm == nil || bundle.Execution.ReviewCouncilMaxTurns > 0 || bundle.Execution.ReviewCouncilTurnsCompleted > 0 {
		return false, nil
	}
	for _, task := range s.pm.Tasks() {
		if task.PlanID != bundle.Plan.PlanID || !strings.HasPrefix(task.ID, planTaskRuntimeID(bundle.Plan.PlanID, "impl-review-")) {
			continue
		}
		switch task.Status {
		case pool.TaskCompleted:
			return true, s.finalizeLegacyImplementationReviewCompleted(task, bundle)
		case pool.TaskFailed:
			return true, s.finalizeLegacyImplementationReviewFailed(task, bundle)
		}
	}
	return false, nil
}

func (s *Scheduler) migrateLegacyImplementationReviewPlan(bundle *StoredPlan) error {
	if s == nil || bundle == nil || bundle.Execution.ReviewCouncilMaxTurns > 0 || bundle.Execution.ReviewCouncilTurnsCompleted > 0 {
		return nil
	}
	legacyActive := false
	for _, task := range s.pm.Tasks() {
		if task.PlanID != bundle.Plan.PlanID {
			continue
		}
		if strings.HasPrefix(task.ID, planTaskRuntimeID(bundle.Plan.PlanID, "impl-review-")) {
			legacyActive = true
			if task.Status != pool.TaskCompleted && task.Status != pool.TaskFailed && task.Status != pool.TaskCanceled {
				if err := s.pm.CancelTask(task.ID); err != nil {
					return err
				}
			}
			if err := s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage); err != nil {
				return err
			}
			_ = s.pm.DeleteTask(task.ID)
		}
	}
	if !legacyActive && len(bundle.Execution.ActiveTaskIDs) == 0 {
		return nil
	}
	bundle.Execution.ReviewCouncilMaxTurns = 4
	bundle.Execution.ReviewCouncilTurnsCompleted = 0
	bundle.Execution.ReviewCouncilAwaitingAnswers = false
	bundle.Execution.ReviewCouncilFinalDecision = ""
	bundle.Execution.ReviewCouncilSeats = newReviewCouncilSeats()
	bundle.Execution.ReviewCouncilTurns = nil
	bundle.Execution.ReviewCouncilWarnings = nil
	bundle.Execution.ReviewCouncilUnresolvedDisagreements = nil
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryReviewCouncilStarted,
		Summary: "Review council started.",
	})
	if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) finalizeLegacyImplementationReviewCompleted(task pool.Task, bundle StoredPlan) error {
	if s == nil || s.pm == nil {
		return nil
	}
	raw, err := os.ReadFile(pool.WorkerStatePath(s.pm.StateDir(), task.WorkerID, pool.WorkerResultFile))
	if err != nil {
		return s.finalizeLegacyImplementationReviewFailed(pool.Task{
			ID:       task.ID,
			PlanID:   task.PlanID,
			Result:   &pool.TaskResult{Error: fmt.Sprintf("read impl review output: %v", err)},
			WorkerID: task.WorkerID,
		}, bundle)
	}
	verdict, feedback, severity := adapter.ExtractReviewVerdict(string(raw))
	if verdict == "" {
		verdict = pool.ReviewFail
		feedback = "implementation review verdict not found in output"
		if severity == "" {
			severity = pool.SeverityMajor
		}
	}
	now := time.Now().UTC()
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = appendUniqueIDs(bundle.Execution.CompletedTaskIDs, task.ID)
	bundle.Execution.ImplReviewedAt = &now
	bundle.Plan.State = planStateCompleted
	bundle.Execution.State = planStateCompleted
	bundle.Execution.CompletedAt = &now
	if verdict == pool.ReviewPass {
		bundle.Execution.ImplReviewStatus = planReviewStatusPassed
		bundle.Execution.ImplReviewFindings = nil
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryImplReviewPassed,
			Cycle:   1,
			TaskID:  task.ID,
			Verdict: verdict,
		})
	} else {
		findings := []string{}
		if strings.TrimSpace(severity) != "" {
			findings = append(findings, "Severity: "+strings.TrimSpace(severity))
		}
		for _, line := range strings.Split(strings.TrimSpace(feedback), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				findings = append(findings, line)
			}
		}
		if len(findings) == 0 {
			findings = []string{"Implementation review failed."}
		}
		bundle.Execution.ImplReviewStatus = planReviewStatusFailed
		bundle.Execution.ImplReviewFindings = findings
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:     planHistoryImplReviewFailed,
			Cycle:    1,
			TaskID:   task.ID,
			Verdict:  verdict,
			Findings: append([]string(nil), findings...),
		})
	}
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		if verdict == pool.ReviewPass {
			s.notify(pool.Notification{Type: "plan_impl_review_passed", ID: task.PlanID, Message: bundle.Plan.Title})
		} else {
			s.notify(pool.Notification{Type: "plan_impl_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
		}
	}
	if err := s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage); err != nil {
		return err
	}
	return s.pm.DeleteTask(task.ID)
}

func (s *Scheduler) finalizeLegacyImplementationReviewFailed(task pool.Task, bundle StoredPlan) error {
	now := time.Now().UTC()
	msg := "implementation review task failed"
	if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
		msg = strings.TrimSpace(task.Result.Error)
	}
	bundle.Plan.State = planStateCompleted
	bundle.Execution.State = planStateCompleted
	bundle.Execution.CompletedAt = &now
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.ImplReviewStatus = planReviewStatusFailed
	bundle.Execution.ImplReviewFindings = []string{msg}
	bundle.Execution.ImplReviewedAt = &now
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     planHistoryImplReviewFailed,
		Cycle:    1,
		TaskID:   task.ID,
		Verdict:  pool.ReviewFail,
		Findings: append([]string(nil), bundle.Execution.ImplReviewFindings...),
	})
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	if s.notify != nil {
		s.notify(pool.Notification{Type: "plan_impl_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
	}
	if err := s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage); err != nil {
		return err
	}
	return s.pm.DeleteTask(task.ID)
}

func (s *Scheduler) enqueueReviewCouncilResumeIfReady(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	if bundle.Execution.State != planStateImplementationReview || !bundle.Execution.ReviewCouncilAwaitingAnswers {
		return nil
	}
	if len(pendingReviewCouncilQuestionsForPlan(s.pm, bundle.Plan.PlanID)) > 0 {
		return nil
	}
	nextTurn := bundle.Execution.ReviewCouncilTurnsCompleted + 1
	nextTaskID := reviewCouncilTaskID(bundle.Plan.PlanID, nextTurn)
	if _, exists := s.pm.Task(nextTaskID); exists {
		return nil
	}
	bundle.Execution.ReviewCouncilAwaitingAnswers = false
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryReviewCouncilResumed,
		Cycle:   nextTurn,
		TaskID:  nextTaskID,
		Summary: "Review council resumed after operator answers.",
	})
	return s.enqueueReviewCouncilTurn(bundle)
}

func (s *Scheduler) invalidateReviewCouncilSeat(bundle *StoredPlan, idx, turn int, taskID string) {
	if bundle == nil || idx < 0 || idx >= len(bundle.Execution.ReviewCouncilSeats) {
		return
	}
	now := time.Now().UTC()
	seat := bundle.Execution.ReviewCouncilSeats[idx]
	seat.WorkerID = ""
	seat.IdleSince = nil
	seat.Rehydrated = true
	seat.RehydratedAt = &now
	bundle.Execution.ReviewCouncilSeats[idx] = seat
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryReviewCouncilSeatRehydrated,
		Cycle:   turn,
		TaskID:  taskID,
		Summary: fmt.Sprintf("Review council seat %s rehydrated.", seat.Seat),
	})
}

func (s *Scheduler) cleanupReviewCouncilTask(task pool.Task, lineage string) error {
	if s.git != nil && strings.TrimSpace(lineage) != "" {
		if err := s.git.DiscardChild(lineage, task.ID); err != nil {
			return err
		}
	}
	s.killWorkerForDiscardedWorktree(task.WorkerID, task.ID)
	return nil
}
