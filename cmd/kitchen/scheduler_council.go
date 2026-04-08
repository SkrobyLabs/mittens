package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func (s *Scheduler) reservedCouncilWorkerIDs() map[string]string {
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
		for _, seat := range bundle.Execution.CouncilSeats {
			workerID := strings.TrimSpace(seat.WorkerID)
			if workerID == "" {
				continue
			}
			reserved[workerID] = bundle.Plan.PlanID + ":" + seat.Seat
		}
	}
	return reserved
}

func (s *Scheduler) workerCanRunCouncilTask(worker pool.Worker, task pool.Task) (bool, bool) {
	if task.Role != plannerTaskRole || task.PlanID == "" {
		return false, false
	}
	turn := councilTurnNumberFromTaskID(task.PlanID, task.ID)
	if turn <= 0 || s == nil || s.plans == nil {
		return false, false
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return false, true
	}
	seat := bundle.Execution.CouncilSeats[councilSeatIndex(councilSeatForTurn(turn))]
	targetWorkerID := strings.TrimSpace(seat.WorkerID)
	if targetWorkerID != "" {
		return worker.ID == targetWorkerID, true
	}
	if _, reserved := s.reservedCouncilWorkerIDs()[worker.ID]; reserved {
		return false, true
	}
	return true, true
}

func (s *Scheduler) enqueueCouncilTurn(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	turn := bundle.Execution.CouncilTurnsCompleted + 1
	if turn > bundle.Execution.CouncilMaxTurns {
		return nil
	}
	taskID := councilTaskID(bundle.Plan.PlanID, turn)
	if _, exists := s.pm.Task(taskID); !exists {
		prompt, err := buildCouncilTurnPrompt(bundle, turn)
		if err != nil {
			return err
		}
		if _, err := s.pm.EnqueueTask(pool.TaskSpec{
			ID:         taskID,
			PlanID:     bundle.Plan.PlanID,
			Prompt:     prompt,
			Complexity: string(ComplexityMedium),
			Priority:   1,
			Role:       plannerTaskRole,
		}); err != nil {
			return err
		}
	}
	if turn == 1 && bundle.Execution.CouncilTurnsCompleted == 0 {
		bundle.Plan.State = planStatePlanning
		bundle.Execution.State = planStatePlanning
	} else {
		bundle.Plan.State = planStateReviewing
		bundle.Execution.State = planStateReviewing
	}
	bundle.Execution.ActiveTaskIDs = []string{taskID}
	idx := councilSeatIndex(councilSeatForTurn(turn))
	seat := bundle.Execution.CouncilSeats[idx]
	if seat.Seat == "" {
		seat.Seat = councilSeatForTurn(turn)
	}
	if workerID := strings.TrimSpace(seat.WorkerID); workerID != "" {
		if worker, ok := s.pm.Worker(workerID); ok && worker.Status != pool.WorkerDead {
			if worker.Status == pool.WorkerIdle {
				if err := s.pm.DispatchTask(taskID, workerID); err != nil {
					return err
				}
			}
		} else {
			now := time.Now().UTC()
			seat.WorkerID = ""
			seat.IdleSince = nil
			seat.Rehydrated = true
			seat.RehydratedAt = &now
			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:    planHistoryCouncilSeatRehydrated,
				Cycle:   turn,
				TaskID:  taskID,
				Summary: fmt.Sprintf("Council seat %s rehydrated.", seat.Seat),
			})
		}
	}
	bundle.Execution.CouncilSeats[idx] = seat
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	return s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution)
}

func (s *Scheduler) onCouncilTurnCompleted(task pool.Task) error {
	if s == nil || s.plans == nil || task.PlanID == "" {
		return nil
	}
	bundle, err := s.plans.Get(task.PlanID)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(pool.WorkerStatePath(s.pm.StateDir(), task.WorkerID, pool.WorkerPlanFile))
	if err != nil {
		return s.handleCouncilArtifactFailure(task, bundle, fmt.Sprintf("read council artifact: %v", err))
	}
	var artifact adapter.CouncilTurnArtifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return s.handleCouncilArtifactFailure(task, bundle, fmt.Sprintf("decode council artifact: %v", err))
	}
	if err := validateCouncilTurnAgainstTask(task, &artifact); err != nil {
		return s.handleCouncilArtifactFailure(task, bundle, err.Error())
	}
	planned := planFromArtifact(bundle.Plan, artifact.CandidatePlan)
	if err := validatePlanRecord(planned, s.lineages); err != nil {
		return s.handleCouncilArtifactFailure(task, bundle, fmt.Sprintf("validate candidate plan: %v", err))
	}
	return s.applyCouncilTurnResult(task, bundle, &artifact, planned)
}

func validateCouncilTurnAgainstTask(task pool.Task, artifact *adapter.CouncilTurnArtifact) error {
	if artifact == nil {
		return fmt.Errorf("council artifact is nil")
	}
	turn := councilTurnNumberFromTaskID(task.PlanID, task.ID)
	if turn > 0 && artifact.Turn != turn {
		return fmt.Errorf("council artifact turn %d does not match task turn %d", artifact.Turn, turn)
	}
	seat := councilSeatForTurn(turn)
	if seat != "" && artifact.Seat != seat {
		return fmt.Errorf("council artifact seat %s does not match task seat %s", artifact.Seat, seat)
	}
	return nil
}

func (s *Scheduler) handleCouncilArtifactFailure(task pool.Task, bundle StoredPlan, message string) error {
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
	turn := councilTurnNumberFromTaskID(task.PlanID, task.ID)
	artifact := synthesizeBlockedCouncilArtifact(bundle.Plan, councilSeatForTurn(turn), turn, message)
	return s.applyCouncilTurnResult(task, bundle, artifact, bundle.Plan)
}

func (s *Scheduler) applyCouncilTurnResult(task pool.Task, bundle StoredPlan, artifact *adapter.CouncilTurnArtifact, planned PlanRecord) error {
	previousLineage := strings.TrimSpace(bundle.Plan.Lineage)
	now := time.Now().UTC()
	bundle.Plan = planned
	bundle.Execution.CouncilTurnsCompleted = artifact.Turn
	bundle.Execution.CouncilTurns = append(bundle.Execution.CouncilTurns, CouncilTurnRecord{
		Seat:       artifact.Seat,
		Turn:       artifact.Turn,
		Artifact:   artifact,
		OccurredAt: now,
	})
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = appendUniqueIDs(bundle.Execution.CompletedTaskIDs, task.ID)
	bundle.Execution.FailedTaskIDs = nil
	bundle.Execution.CouncilAwaitingAnswers = false
	idx := councilSeatIndex(artifact.Seat)
	seat := bundle.Execution.CouncilSeats[idx]
	if seat.Seat == "" {
		seat.Seat = artifact.Seat
	}
	seat.WorkerID = task.WorkerID
	seat.IdleSince = &now
	if worker, ok := s.pm.Worker(task.WorkerID); ok {
		seat.PoolKey = PoolKey{Provider: worker.Provider}
	}
	bundle.Execution.CouncilSeats[idx] = seat
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryCouncilTurnCompleted,
		Cycle:   artifact.Turn,
		TaskID:  task.ID,
		Summary: firstNonEmpty(strings.TrimSpace(artifact.Summary), strings.TrimSpace(bundle.Plan.Title)),
	})

	decision, warnings := decideCouncilNext(bundle, artifact)
	switch decision {
	case councilAskUser:
		if err := s.seedCouncilQuestions(task, artifact.QuestionsForUser); err != nil {
			return err
		}
		bundle.Plan.State = planStateReviewing
		bundle.Execution.State = planStateReviewing
		bundle.Execution.CouncilAwaitingAnswers = true
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryCouncilWaitingAnswers,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Council waiting for operator answers.",
		})
	case councilContinue:
		bundle.Plan.State = planStateReviewing
		bundle.Execution.State = planStateReviewing
	case councilConverged:
		bundle.Plan.State = planStatePendingApproval
		bundle.Execution.State = planStatePendingApproval
		bundle.Execution.CouncilFinalDecision = councilConverged
		bundle.Execution.CouncilWarnings = append([]adapter.CouncilDisagreement(nil), warnings...)
		bundle.Execution.CouncilUnresolvedDisagreements = nil
		bundle.Execution.CouncilSeats = newCouncilSeats()
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryCouncilConverged,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Council converged.",
		})
	case councilReject:
		bundle.Plan.State = planStateRejected
		bundle.Execution.State = planStateRejected
		bundle.Execution.CouncilFinalDecision = councilReject
		bundle.Execution.CouncilWarnings = nil
		bundle.Execution.CouncilUnresolvedDisagreements = append([]adapter.CouncilDisagreement(nil), artifact.Disagreements...)
		bundle.Execution.RejectedBy = "council"
		bundle.Execution.CouncilSeats = newCouncilSeats()
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryCouncilRejected,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Council rejected the plan.",
		})
	}
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
		return err
	}
	newLineage := strings.TrimSpace(bundle.Plan.Lineage)
	if s.lineages != nil && previousLineage != "" && newLineage != "" && previousLineage != newLineage {
		_ = s.lineages.ClearActivePlan(previousLineage, task.PlanID)
		if err := s.lineages.ActivatePlan(newLineage, task.PlanID); err != nil {
			return err
		}
	}
	switch decision {
	case councilAskUser:
		return nil
	case councilContinue:
		return s.enqueueCouncilTurn(bundle)
	case councilConverged:
		if s.notify != nil {
			s.notify(pool.Notification{Type: "plan_ready", ID: task.PlanID, Message: bundle.Plan.Title})
		}
		if canAutoApproveCouncil(bundle.Execution) && s.activatePlan != nil && len(pendingQuestionsForPlan(s.pm, task.PlanID)) == 0 {
			return s.activatePlan(task.PlanID)
		}
		return nil
	case councilReject:
		if s.notify != nil {
			s.notify(pool.Notification{Type: "plan_rejected", ID: task.PlanID, Message: bundle.Plan.Title})
		}
		return nil
	default:
		return nil
	}
}

func (s *Scheduler) seedCouncilQuestions(task pool.Task, questions []adapter.CouncilUserQuestion) error {
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
			Category: "council",
			Context:  strings.TrimSpace(question.WhyItMatters),
			Blocking: question.Blocking,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) recoverCouncilPlansOnStartup() error {
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
		if bundle.Execution.State != planStateReviewing {
			continue
		}

		if bundle.Execution.CouncilAwaitingAnswers && len(pendingCouncilQuestionsForPlan(s.pm, bundle.Plan.PlanID)) == 0 {
			if err := s.enqueueCouncilResumeIfReady(bundle); err != nil {
				return err
			}
			bundle, err = s.plans.Get(plan.PlanID)
			if err != nil {
				continue
			}
		}

		nextTurn := bundle.Execution.CouncilTurnsCompleted + 1
		nextTaskID := councilTaskID(bundle.Plan.PlanID, nextTurn)
		if bundle.Execution.CouncilFinalDecision == "" &&
			bundle.Execution.CouncilTurnsCompleted < bundle.Execution.CouncilMaxTurns &&
			!bundle.Execution.CouncilAwaitingAnswers &&
			len(bundle.Execution.ActiveTaskIDs) == 0 {
			if _, exists := s.pm.Task(nextTaskID); !exists {
				if err := s.enqueueCouncilTurn(bundle); err != nil {
					return err
				}
				bundle, err = s.plans.Get(plan.PlanID)
				if err != nil {
					continue
				}
			}
		}

		changedSeats := false
		for i := range bundle.Execution.CouncilSeats {
			workerID := strings.TrimSpace(bundle.Execution.CouncilSeats[i].WorkerID)
			if workerID == "" {
				continue
			}
			worker, ok := s.pm.Worker(workerID)
			if ok && worker.Status != pool.WorkerDead {
				continue
			}
			bundle.Execution.CouncilSeats[i].WorkerID = ""
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

func (s *Scheduler) enqueueCouncilResumeIfReady(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	if bundle.Execution.State != planStateReviewing || !bundle.Execution.CouncilAwaitingAnswers {
		return nil
	}
	if len(pendingCouncilQuestionsForPlan(s.pm, bundle.Plan.PlanID)) > 0 {
		return nil
	}
	nextTurn := bundle.Execution.CouncilTurnsCompleted + 1
	nextTaskID := councilTaskID(bundle.Plan.PlanID, nextTurn)
	if _, exists := s.pm.Task(nextTaskID); exists {
		return nil
	}
	bundle.Execution.CouncilAwaitingAnswers = false
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryCouncilResumed,
		Cycle:   nextTurn,
		TaskID:  nextTaskID,
		Summary: "Council resumed after operator answers.",
	})
	return s.enqueueCouncilTurn(bundle)
}

func (s *Scheduler) onCouncilTurnFailed(task pool.Task, class FailureClass) error {
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
		if bundle.Execution.CouncilTurnsCompleted == 0 {
			bundle.Plan.State = planStatePlanning
			bundle.Execution.State = planStatePlanning
		} else {
			bundle.Plan.State = planStateReviewing
			bundle.Execution.State = planStateReviewing
		}
		bundle.Execution.ActiveTaskIDs = []string{task.ID}
		if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
			return err
		}
		return s.plans.UpdateExecution(task.PlanID, bundle.Execution)
	case FailureCapability, FailureTimeout:
		turn := councilTurnNumberFromTaskID(task.PlanID, task.ID)
		msg := "council seat blocked"
		if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
			msg = strings.TrimSpace(task.Result.Error)
		}
		return s.applyCouncilTurnResult(task, bundle, synthesizeBlockedCouncilArtifact(bundle.Plan, councilSeatForTurn(turn), turn, msg), bundle.Plan)
	default:
		message := "planner task failed"
		if task.Result != nil && strings.TrimSpace(task.Result.Error) != "" {
			message = task.Result.Error
		}
		return s.markPlanningFailed(task, message)
	}
}
