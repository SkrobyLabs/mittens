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
	resolution := s.routeResolutionForTask(task)
	if routeResolutionUnavailable(resolution) {
		return false, true
	}
	keys := resolution.Keys
	if len(keys) == 0 {
		return s.workerCanRunTaskWithoutCouncilAffinity(worker, task), true
	}
	seat := bundle.Execution.ReviewCouncilSeats[reviewCouncilSeatIndex(reviewCouncilSeatForTurn(turn))]
	targetWorkerID := strings.TrimSpace(seat.WorkerID)
	if reserved, ok := s.reservedReviewCouncilWorkerIDs()[worker.ID]; ok && reserved != bundle.Plan.PlanID+":"+seat.Seat {
		return false, true
	}
	// Review turns must run in a dedicated review workspace. Reusing arbitrary
	// idle workers risks diffing the root repo or a stale non-review checkout,
	// so only the previously reserved seat worker may be reused.
	if targetWorkerID == "" {
		return false, true
	}
	if strings.TrimSpace(worker.Role) != task.Role {
		return false, true
	}
	if s.pm != nil && !s.pm.WorkerHealthy(worker.ID, s.reapTimeout) {
		return false, true
	}
	if targetWorker, ok := s.refreshReviewCouncilSeatWorker(targetWorkerID); ok && s.reviewCouncilSeatWorkerUsable(*targetWorker, task) {
		if worker.ID != targetWorkerID {
			return false, true
		}
	} else if worker.ID == targetWorkerID {
		return false, true
	}
	return workerMatchesAnyRouteKey(worker, keys), true
}

func (s *Scheduler) refreshReviewCouncilSeatWorker(workerID string) (*pool.Worker, bool) {
	if s == nil || s.pm == nil {
		return nil, false
	}
	s.pm.MarkDeadIfStale(workerID, s.reapTimeout)
	return s.pm.Worker(workerID)
}

func (s *Scheduler) reviewCouncilSeatWorkerUsable(worker pool.Worker, task pool.Task) bool {
	if worker.Status == pool.WorkerDead {
		return false
	}
	if s.pm != nil && !s.pm.WorkerHealthy(worker.ID, s.reapTimeout) {
		return false
	}
	return s.workerWorkspaceCompatible(worker, task) && s.seatWorkerMatchesRoute(worker, task)
}

func reviewCouncilSeatWorkerIDs(seats [2]CouncilSeatRecord, exclude ...string) []string {
	excluded := make(map[string]struct{}, len(exclude))
	for _, workerID := range exclude {
		workerID = strings.TrimSpace(workerID)
		if workerID == "" {
			continue
		}
		excluded[workerID] = struct{}{}
	}
	workerIDs := make([]string, 0, len(seats))
	for _, seat := range seats {
		workerID := strings.TrimSpace(seat.WorkerID)
		if workerID == "" {
			continue
		}
		if _, ok := excluded[workerID]; ok {
			continue
		}
		workerIDs = append(workerIDs, workerID)
	}
	return workerIDs
}

func (s *Scheduler) enqueueReviewCouncilTurn(bundle StoredPlan) error {
	if s == nil || s.pm == nil || s.plans == nil {
		return nil
	}
	turn := bundle.Execution.ReviewCouncilTurnsCompleted + 1
	if turn > bundle.Execution.ReviewCouncilMaxTurns {
		return nil
	}
	taskID := reviewCouncilTaskIDForExecution(bundle.Plan.PlanID, bundle.Execution, turn)
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
	seatTask := pool.Task{
		ID:         taskID,
		PlanID:     bundle.Plan.PlanID,
		Complexity: string(implementationReviewComplexityForPlan(bundle.Plan)),
		Role:       "reviewer",
	}
	if workerID := strings.TrimSpace(seat.WorkerID); workerID != "" {
		if worker, ok := s.refreshReviewCouncilSeatWorker(workerID); ok && s.reviewCouncilSeatWorkerUsable(*worker, seatTask) {
			if worker.Status == pool.WorkerIdle {
				if err := s.pm.DispatchTask(taskID, workerID); err != nil {
					return err
				}
			}
		} else {
			resolution := s.routeResolutionForTask(seatTask)
			if !routeResolutionTemporarilyExhausted(resolution) {
				s.invalidateReviewCouncilSeat(&bundle, idx, turn, taskID)
				seat = bundle.Execution.ReviewCouncilSeats[idx]
			}
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
	normalizeReviewCouncilArtifact(artifact)
	previousSeats := bundle.Execution.ReviewCouncilSeats
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
	autoRemediationHandled := false
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
		bundle.Execution.ActiveTaskIDs = nil
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryReviewCouncilConverged,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: fmt.Sprintf("Review council converged on %s.", artifact.Verdict),
		})
		if strings.TrimSpace(artifact.Verdict) == pool.ReviewPass {
			passFindings := reviewCouncilFollowupStrings(bundle.Execution.ReviewCouncilTurns, reviewSeverityAtMost(pool.SeverityMinor))
			bundle.Plan.State = planStateCompleted
			bundle.Execution.State = planStateCompleted
			bundle.Execution.CompletedAt = &now
			bundle.Execution.ImplReviewStatus = planReviewStatusPassed
			bundle.Execution.ImplReviewFindings = nil
			bundle.Execution.ImplReviewFollowups = passFindings
			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:    planHistoryImplReviewPassed,
				Cycle:   artifact.Turn,
				TaskID:  task.ID,
				Verdict: artifact.Verdict,
				Findings: append([]string(nil),
					passFindings...,
				),
			})
		} else {
			if handled, err := s.startAutoRemediationForReviewFailure(&bundle, task, reviewCouncilConverged, artifact); err != nil {
				return err
			} else if !handled {
				bundle.Plan.State = planStateImplementationReviewFailed
				bundle.Execution.State = planStateImplementationReviewFailed
				bundle.Execution.CompletedAt = nil
				bundle.Execution.ImplReviewStatus = planReviewStatusFailed
				bundle.Execution.ImplReviewFollowups = nil
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
			} else {
				autoRemediationHandled = true
			}
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
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryReviewCouncilRejected,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Review council rejected the implementation review.",
		})
		if handled, err := s.startAutoRemediationForReviewFailure(&bundle, task, reviewCouncilReject, rejectArtifact); err != nil {
			return err
		} else if !handled {
			bundle.Plan.State = planStateImplementationReviewFailed
			bundle.Execution.State = planStateImplementationReviewFailed
			bundle.Execution.ReviewCouncilFinalDecision = reviewCouncilReject
			bundle.Execution.ReviewCouncilWarnings = nil
			bundle.Execution.ReviewCouncilUnresolvedDisagreements = append([]adapter.CouncilDisagreement(nil), rejectArtifact.Disagreements...)
			bundle.Execution.ReviewCouncilSeats = newReviewCouncilSeats()
			bundle.Execution.RejectedBy = rejectedByReviewCouncil
			bundle.Execution.ImplReviewStatus = planReviewStatusFailed
			bundle.Execution.ImplReviewFollowups = nil
			bundle.Execution.ImplReviewFindings = reviewCouncilFindingsToStrings(rejectArtifact.Findings, rejectArtifact.Disagreements)
			if len(bundle.Execution.ImplReviewFindings) == 0 {
				bundle.Execution.ImplReviewFindings = []string{"Implementation review failed."}
			}
			bundle.Execution.ImplReviewedAt = &now
			bundle.Execution.ActiveTaskIDs = nil
			bundle.Execution.CompletedAt = nil
			bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
				Type:     planHistoryImplReviewFailed,
				Cycle:    artifact.Turn,
				TaskID:   task.ID,
				Verdict:  pool.ReviewFail,
				Findings: append([]string(nil), bundle.Execution.ImplReviewFindings...),
			})
		} else {
			autoRemediationHandled = true
		}
	}
	if autoRemediationHandled {
		latest, err := s.plans.Get(task.PlanID)
		if err != nil {
			return err
		}
		bundle = latest
	} else {
		if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
			return err
		}
		if err := s.plans.UpdateExecution(task.PlanID, bundle.Execution); err != nil {
			return err
		}
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
		s.killWorkerIDs(reviewCouncilSeatWorkerIDs(previousSeats, task.WorkerID)...)
		if err := s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage); err != nil {
			return err
		}
		if strings.TrimSpace(artifact.Verdict) == pool.ReviewPass {
			return s.syncPlanExecution(task.PlanID)
		}
		return s.syncPlanExecution(task.PlanID)
	case reviewCouncilReject:
		if s.notify != nil {
			s.notify(pool.Notification{Type: "plan_review_council_rejected", ID: task.PlanID, Message: bundle.Plan.Title})
			s.notify(pool.Notification{Type: "plan_impl_review_failed", ID: task.PlanID, Message: bundle.Plan.Title})
		}
		s.killWorkerIDs(reviewCouncilSeatWorkerIDs(previousSeats, task.WorkerID)...)
		return s.cleanupReviewCouncilTask(task, bundle.Plan.Lineage)
	default:
		return nil
	}
}

func (s *Scheduler) startAutoRemediationForReviewFailure(bundle *StoredPlan, task pool.Task, decision string, artifact *adapter.ReviewCouncilTurnArtifact) (bool, error) {
	if s == nil || bundle == nil {
		return false, nil
	}
	if !autoRemediationEligible(decision, artifact) {
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryAutoRemediationSkipped,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: "Implementation review fail left terminal because it was not actionable for auto-remediation.",
		})
		return false, nil
	}
	if bundle.Execution.AutoRemediationAttempt >= AutoRemediationHardCap {
		bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
			Type:    planHistoryAutoRemediationSkipped,
			Cycle:   artifact.Turn,
			TaskID:  task.ID,
			Summary: fmt.Sprintf("Implementation review fail left terminal because auto-remediation hit its hard cap (%d).", AutoRemediationHardCap),
		})
		return false, nil
	}

	findings := reviewCouncilFindingsToStrings(artifact.Findings, artifact.Disagreements)
	if len(findings) == 0 {
		findings = []string{"Implementation review failed."}
	}
	attempt := bundle.Execution.AutoRemediationAttempt + 1
	planTaskID := autoRemediationPlanTaskID(attempt)
	runtimeTaskID := planTaskRuntimeID(bundle.Plan.PlanID, planTaskID)
	bundle.Execution.AutoRemediationAttempt = attempt
	bundle.Execution.AutoRemediationActive = true
	bundle.Execution.AutoRemediationPlanTaskID = planTaskID
	bundle.Execution.AutoRemediationTaskID = runtimeTaskID
	bundle.Execution.AutoRemediationSourceTaskID = task.ID
	bundle.Execution.AutoRemediationSource = newAutoRemediationSourceRecord(decision, task.ID, artifact)
	bundle.Execution.ImplReviewStatus = ""
	bundle.Execution.ImplReviewFindings = nil
	bundle.Execution.ImplReviewFollowups = nil
	bundle.Execution.ImplReviewedAt = nil
	bundle.Execution.ReviewCouncilMaxTurns = 0
	bundle.Execution.ReviewCouncilTurnsCompleted = 0
	bundle.Execution.ReviewCouncilAwaitingAnswers = false
	bundle.Execution.ReviewCouncilFinalDecision = ""
	bundle.Execution.ReviewCouncilSeats = [2]CouncilSeatRecord{}
	bundle.Execution.ReviewCouncilTurns = nil
	bundle.Execution.ReviewCouncilWarnings = nil
	bundle.Execution.ReviewCouncilUnresolvedDisagreements = nil
	bundle.Execution.RejectedBy = ""
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedAt = nil
	bundle.Plan.State = planStateActive
	bundle.Execution.State = planStateActive
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:     planHistoryImplReviewFailed,
		Cycle:    artifact.Turn,
		TaskID:   task.ID,
		Verdict:  pool.ReviewFail,
		Findings: append([]string(nil), findings...),
	})
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryAutoRemediationRequested,
		Cycle:   attempt,
		TaskID:  runtimeTaskID,
		Summary: fmt.Sprintf("Auto-remediation attempt %d/%d queued from implementation review findings.", attempt, AutoRemediationHardCap),
	})
	if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
		return true, err
	}
	if err := s.plans.UpdatePlan(bundle.Plan); err != nil {
		return true, err
	}
	return true, s.ensureAutoRemediationTask(bundle, false)
}

func (s *Scheduler) ensureAutoRemediationTask(bundle *StoredPlan, recovered bool) error {
	if s == nil || s.pm == nil || s.plans == nil || bundle == nil || !bundle.Execution.AutoRemediationActive {
		return nil
	}
	if bundle.Execution.AutoRemediationSource == nil {
		return nil
	}
	planTask := autoRemediationPlanTask(*bundle, bundle.Execution.AutoRemediationAttempt)
	runtimeTaskID := planTaskRuntimeID(bundle.Plan.PlanID, planTask.ID)
	bundle.Execution.AutoRemediationPlanTaskID = planTask.ID
	bundle.Execution.AutoRemediationTaskID = runtimeTaskID

	changed := false
	if !planHasTask(bundle.Plan, planTask.ID) {
		if err := s.plans.AddTask(bundle.Plan.PlanID, planTask); err != nil {
			return err
		}
		changed = true
	}
	task, exists := s.pm.Task(runtimeTaskID)
	if !exists {
		if _, err := s.pm.EnqueueTask(pool.TaskSpec{
			ID:                 runtimeTaskID,
			PlanID:             bundle.Plan.PlanID,
			Prompt:             planTask.Prompt,
			Complexity:         string(planTask.Complexity),
			Priority:           1,
			TimeoutMinutes:     planTask.TimeoutMinutes,
			Role:               "implementer",
			RequireFreshWorker: true,
		}); err != nil {
			return err
		}
		changed = true
	} else if task.Status == pool.TaskCompleted {
		// A stale persisted remediation intent can point at a task that already
		// completed in a previous cycle. Reconcile immediately instead of
		// reasserting planStateActive with no runnable work left.
		return s.syncPlanExecution(bundle.Plan.PlanID)
	} else if task.Status == pool.TaskCanceled {
		if err := s.pm.ReviveCanceledTask(runtimeTaskID, false); err != nil {
			return err
		}
		changed = true
	}
	latest, err := s.plans.Get(bundle.Plan.PlanID)
	if err != nil {
		return err
	}
	active, completed, failed := summarizePlanTasks(s.pm.Tasks(), bundle.Plan.PlanID)
	latest.Plan.State = planStateActive
	latest.Execution.State = planStateActive
	latest.Execution.ActiveTaskIDs = active
	latest.Execution.CompletedTaskIDs = completed
	latest.Execution.FailedTaskIDs = failed
	latest.Execution.CompletedAt = nil
	latest.Execution.AutoRemediationActive = true
	latest.Execution.AutoRemediationPlanTaskID = planTask.ID
	latest.Execution.AutoRemediationTaskID = runtimeTaskID
	if changed && recovered {
		latest.Execution = appendPlanHistory(latest.Execution, PlanHistoryEntry{
			Type:    planHistoryAutoRemediationRecovered,
			Cycle:   latest.Execution.AutoRemediationAttempt,
			TaskID:  runtimeTaskID,
			Summary: "Recovered auto-remediation task after partial persistence.",
		})
	}
	if err := s.plans.UpdatePlan(latest.Plan); err != nil {
		return err
	}
	if err := s.plans.UpdateExecution(bundle.Plan.PlanID, latest.Execution); err != nil {
		return err
	}
	*bundle = latest
	return nil
}

func reviewCouncilFindingsToStrings(findings []adapter.ReviewFinding, disagreements []adapter.CouncilDisagreement) []string {
	return reviewCouncilFindingsToStringsFiltered(findings, disagreements, nil)
}

func latestReviewCouncilFindings(bundle StoredPlan) []string {
	for i := len(bundle.Execution.ReviewCouncilTurns) - 1; i >= 0; i-- {
		artifact := bundle.Execution.ReviewCouncilTurns[i].Artifact
		if artifact == nil {
			continue
		}
		findings := reviewCouncilFindingsToStrings(artifact.Findings, artifact.Disagreements)
		if len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func mergeReviewCouncilFailureFindings(bundle StoredPlan, message string) []string {
	findings := append([]string(nil), bundle.Execution.ImplReviewFindings...)
	if len(findings) == 0 {
		findings = latestReviewCouncilFindings(bundle)
	}
	message = strings.TrimSpace(message)
	if len(findings) == 0 {
		if message == "" {
			return []string{"Implementation review failed."}
		}
		return []string{message}
	}
	if message != "" {
		present := false
		for _, finding := range findings {
			if strings.TrimSpace(finding) == message {
				present = true
				break
			}
		}
		if !present {
			findings = append(findings, message)
		}
	}
	return findings
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
	case FailureEnvironment, FailureInfrastructure:
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
	case FailureAuth:
		if handled, err := s.retryAuthFailedTask(&task, bundle); handled || err != nil {
			return err
		}
		return s.markReviewCouncilFailed(task, s.taskFailureSummary(task))
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
	bundle.Plan.State = planStateImplementationReviewFailed
	bundle.Execution.State = planStateImplementationReviewFailed
	bundle.Execution.CompletedAt = nil
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.FailedTaskIDs = appendUniqueIDs(bundle.Execution.FailedTaskIDs, task.ID)
	bundle.Execution.ImplReviewStatus = planReviewStatusFailed
	bundle.Execution.ImplReviewFollowups = nil
	bundle.Execution.ImplReviewFindings = mergeReviewCouncilFailureFindings(bundle, message)
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
		nextTaskID := reviewCouncilTaskIDForExecution(bundle.Plan.PlanID, bundle.Execution, nextTurn)
		task, exists := s.pm.Task(nextTaskID)
		if bundle.Execution.ReviewCouncilFinalDecision == "" &&
			bundle.Execution.ReviewCouncilTurnsCompleted < bundle.Execution.ReviewCouncilMaxTurns &&
			!bundle.Execution.ReviewCouncilAwaitingAnswers &&
			exists && task.Status == pool.TaskCompleted {
			if err := s.onTaskCompleted(nextTaskID); err != nil {
				return err
			}
			bundle, err = s.plans.Get(plan.PlanID)
			if err != nil {
				continue
			}
			if bundle.Execution.State != planStateImplementationReview {
				continue
			}
			nextTurn = bundle.Execution.ReviewCouncilTurnsCompleted + 1
			nextTaskID = reviewCouncilTaskIDForExecution(bundle.Plan.PlanID, bundle.Execution, nextTurn)
			task, exists = s.pm.Task(nextTaskID)
		}
		missingRecordedActiveTask := containsString(bundle.Execution.ActiveTaskIDs, nextTaskID) && !exists
		if bundle.Execution.ReviewCouncilFinalDecision == "" &&
			bundle.Execution.ReviewCouncilTurnsCompleted < bundle.Execution.ReviewCouncilMaxTurns &&
			!bundle.Execution.ReviewCouncilAwaitingAnswers &&
			(len(bundle.Execution.ActiveTaskIDs) == 0 || missingRecordedActiveTask) {
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
			seatTask := pool.Task{
				ID:         reviewCouncilTaskIDForExecution(bundle.Plan.PlanID, bundle.Execution, bundle.Execution.ReviewCouncilTurnsCompleted+1),
				PlanID:     bundle.Plan.PlanID,
				Complexity: string(implementationReviewComplexityForPlan(bundle.Plan)),
				Role:       "reviewer",
			}
			worker, ok := s.refreshReviewCouncilSeatWorker(workerID)
			if ok && s.reviewCouncilSeatWorkerUsable(*worker, seatTask) {
				continue
			}
			resolution := s.routeResolutionForTask(seatTask)
			if !routeResolutionTemporarilyExhausted(resolution) {
				s.invalidateReviewCouncilSeat(&bundle, i, bundle.Execution.ReviewCouncilTurnsCompleted+1, "")
				changedSeats = true
			}
		}
		if changedSeats {
			if err := s.plans.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
				return err
			}
		}
	}
	return nil
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
	nextTaskID := reviewCouncilTaskIDForExecution(bundle.Plan.PlanID, bundle.Execution, nextTurn)
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
