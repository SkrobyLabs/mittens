package main

import (
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type PlanWorkflowStage string

const (
	planWorkflowStagePlanning             PlanWorkflowStage = "planning"
	planWorkflowStageReviewing            PlanWorkflowStage = "reviewing"
	planWorkflowStagePendingApproval      PlanWorkflowStage = "pending_approval"
	planWorkflowStageExecuting            PlanWorkflowStage = "executing"
	planWorkflowStageImplementationReview PlanWorkflowStage = "implementation_review"
	planWorkflowStageMerging              PlanWorkflowStage = "merging"
	planWorkflowStageCompleted            PlanWorkflowStage = "completed"
	planWorkflowStageTerminal             PlanWorkflowStage = "terminal"
)

type PlanExecutionDisposition string

const (
	planExecutionDispositionRunning   PlanExecutionDisposition = "running"
	planExecutionDispositionWaiting   PlanExecutionDisposition = "waiting"
	planExecutionDispositionSucceeded PlanExecutionDisposition = "succeeded"
	planExecutionDispositionFailed    PlanExecutionDisposition = "failed"
	planExecutionDispositionCanceled  PlanExecutionDisposition = "canceled"
)

type PlanMergeDisposition string

const (
	planMergeDispositionNone       PlanMergeDisposition = "none"
	planMergeDispositionPending    PlanMergeDisposition = "pending"
	planMergeDispositionFixing     PlanMergeDisposition = "fixing"
	planMergeDispositionInProgress PlanMergeDisposition = "in_progress"
	planMergeDispositionMerged     PlanMergeDisposition = "merged"
)

type PlanOperatorStatus string

const (
	planOperatorStatusNone             PlanOperatorStatus = "none"
	planOperatorStatusAwaitingApproval PlanOperatorStatus = "awaiting_approval"
	planOperatorStatusAwaitingAnswers  PlanOperatorStatus = "awaiting_answers"
	planOperatorStatusAttentionNeeded  PlanOperatorStatus = "attention_needed"
)

// PlanProjection is the canonical runtime view of a plan.
//
// Task state from pkg/pool is authoritative for execution truth. Persisted
// plan/execution fields contribute only workflow intent that tasks cannot
// express on their own, such as approval, review, dependency waiting, research
// mode, remediation, and legacy compatibility hints. Runtime consumers should
// prefer this projection over raw Plan.State / Execution.State.
type PlanProjection struct {
	State                string
	Phase                string
	WorkflowStage        PlanWorkflowStage
	ExecutionDisposition PlanExecutionDisposition
	MergeDisposition     PlanMergeDisposition
	OperatorStatus       PlanOperatorStatus
	ActiveTaskIDs        []string
	CompletedTaskIDs     []string
	FailedTaskIDs        []string
}

func projectPlanForKitchen(k *Kitchen, bundle StoredPlan) PlanProjection {
	var tasks []pool.Task
	pendingQuestions := 0
	if k != nil && k.pm != nil {
		tasks = k.pm.Tasks()
		pendingQuestions = len(pendingQuestionsForPlan(k.pm, bundle.Plan.PlanID))
	}
	return projectPlan(bundle, tasks, pendingQuestions)
}

func projectedPersistentPlanState(state string) string {
	if strings.TrimSpace(state) == "cancelled" {
		return planStateClosed
	}
	return strings.TrimSpace(state)
}

func projectedPersistentExecutionState(state string) string {
	if strings.TrimSpace(state) == "" {
		return ""
	}
	return strings.TrimSpace(state)
}

func normalizedStoredPlanStates(bundle StoredPlan) (string, string) {
	planState := strings.TrimSpace(bundle.Plan.State)
	execState := strings.TrimSpace(bundle.Execution.State)

	switch {
	case execState == "" && planState == planStateClosed:
		execState = "cancelled"
	case planState == "" && execState == "cancelled":
		planState = planStateClosed
	case execState == "" && planState != "":
		execState = planState
	case planState == "" && execState != "" && execState != "cancelled":
		planState = execState
	}

	return planState, execState
}

func executionStateHint(bundle StoredPlan) string {
	_, execState := normalizedStoredPlanStates(bundle)
	return execState
}

func shouldProjectImplementationReview(exec ExecutionRecord) bool {
	exec.State = planStateActive
	return shouldEnqueueImplementationReview(exec)
}

func hasLivePlanTask(tasks []pool.Task, planID string, match func(pool.Task) bool) bool {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return false
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.PlanID) != planID {
			continue
		}
		switch task.Status {
		case pool.TaskCompleted, pool.TaskFailed, pool.TaskCanceled:
			continue
		}
		if match == nil || match(task) {
			return true
		}
	}
	return false
}

func projectedPlanState(bundle StoredPlan, tasks []pool.Task, active, completed, failed []string) string {
	planState, execState := normalizedStoredPlanStates(bundle)
	hintState := firstNonEmpty(execState, planState)

	if strings.TrimSpace(bundle.Plan.Mode) == "research" {
		switch hintState {
		case planStateResearchComplete, planStateCompleted, "cancelled", planStateClosed, planStateRejected, planStatePlanningFailed:
			return hintState
		}
		if strings.TrimSpace(bundle.Execution.ResearchOutput) != "" && len(active) == 0 && len(failed) == 0 {
			return planStateResearchComplete
		}
	}

	if len(active) > 0 {
		switch hintState {
		case planStatePlanningFailed, planStateImplementationFailed, planStateImplementationReviewFailed, planStateRejected, planStateClosed, "cancelled":
			switch {
			case hasLivePlanTask(tasks, bundle.Plan.PlanID, isLineageMergeTask):
				return planStateMerging
			case hasLivePlanTask(tasks, bundle.Plan.PlanID, isReviewCouncilTask):
				return planStateImplementationReview
			case hasLivePlanTask(tasks, bundle.Plan.PlanID, func(task pool.Task) bool { return task.Role == plannerTaskRole }):
				if bundle.Execution.CouncilTurnsCompleted > 0 || strings.TrimSpace(bundle.Execution.CouncilFinalDecision) != "" {
					return planStateReviewing
				}
				return planStatePlanning
			default:
				return planStateActive
			}
		}
	}

	switch hintState {
	case planStatePlanning,
		planStateReviewing,
		planStatePendingApproval,
		planStateMerging,
		planStateImplementationReview,
		planStateResearchComplete,
		planStatePlanningFailed,
		planStateImplementationReviewFailed,
		planStateMerged,
		planStateClosed,
		planStateRejected,
		planStateWaitingOnDependency,
		"cancelled":
		return hintState
	}

	if len(active) == 0 && len(failed) == 0 {
		if hasCanceledPlanTrackedTask(tasks, bundle) {
			return planStateActive
		}
		if bundle.Execution.AutoRemediationActive {
			if strings.TrimSpace(bundle.Execution.AutoRemediationTaskID) != "" && containsTrimmedString(completed, bundle.Execution.AutoRemediationTaskID) {
				exec := bundle.Execution
				completeAutoRemediationState(&exec)
				if shouldProjectImplementationReview(exec) {
					return planStateImplementationReview
				}
				return planStateCompleted
			}
			return planStateActive
		}
		if shouldProjectImplementationReview(bundle.Execution) {
			return planStateImplementationReview
		}
		return planStateCompleted
	}
	if len(active) == 0 && len(failed) > 0 {
		if allTasksAreConflictFailed(tasks, failed) {
			return planStateActive
		}
		return planStateImplementationFailed
	}
	return planStateActive
}

func projectedPlanPhase(bundle StoredPlan, state string, pendingQuestions int) string {
	switch state {
	case planStatePlanning:
		return "planning"
	case planStateReviewing:
		return "reviewing"
	case planStateImplementationReview:
		return "reviewing_implementation"
	case planStatePendingApproval:
		if pendingQuestions > 0 {
			return "awaiting_questions"
		}
		return "awaiting_approval"
	case planStateActive:
		if bundle.Execution.AutoRemediationActive {
			if reviewRemediationMode(bundle.Execution.AutoRemediationSource) == reviewRemediationModeManual {
				return "remediating_review_findings"
			}
			return "auto_remediating_implementation_review"
		}
		return "executing"
	case planStateMerging:
		return "merging"
	case planStateWaitingOnDependency:
		return "waiting_on_dependency"
	case "cancelled":
		return "cancelled"
	case "":
		return firstNonEmpty(strings.TrimSpace(bundle.Plan.State), strings.TrimSpace(bundle.Execution.State))
	default:
		return state
	}
}

func projectedMergeDisposition(bundle StoredPlan, state string, active, completed, failed []string) PlanMergeDisposition {
	switch state {
	case planStateMerged:
		return planMergeDispositionMerged
	case planStateMerging:
		return planMergeDispositionInProgress
	case planStateCompleted:
		if strings.TrimSpace(bundle.Plan.Lineage) != "" {
			return planMergeDispositionPending
		}
	}
	for _, taskID := range append(append([]string(nil), active...), failed...) {
		if strings.HasPrefix(strings.TrimSpace(taskID), bundle.Plan.PlanID+"-fix-merge-") {
			return planMergeDispositionFixing
		}
	}
	for _, taskID := range completed {
		if strings.HasPrefix(strings.TrimSpace(taskID), bundle.Plan.PlanID+"-fix-merge-") {
			return planMergeDispositionFixing
		}
	}
	return planMergeDispositionNone
}

func projectedOperatorStatus(bundle StoredPlan, state string, pendingQuestions int) PlanOperatorStatus {
	switch state {
	case planStatePendingApproval:
		if pendingQuestions > 0 {
			return planOperatorStatusAwaitingAnswers
		}
		return planOperatorStatusAwaitingApproval
	case planStateReviewing:
		if bundle.Execution.CouncilAwaitingAnswers && pendingQuestions > 0 {
			return planOperatorStatusAwaitingAnswers
		}
	case planStateImplementationReview:
		if bundle.Execution.ReviewCouncilAwaitingAnswers && pendingQuestions > 0 {
			return planOperatorStatusAwaitingAnswers
		}
	case planStatePlanningFailed, planStateImplementationFailed, planStateImplementationReviewFailed, planStateResearchComplete:
		return planOperatorStatusAttentionNeeded
	case planStateCompleted:
		if strings.TrimSpace(bundle.Plan.Lineage) != "" || strings.TrimSpace(bundle.Plan.Mode) == "research" {
			return planOperatorStatusAttentionNeeded
		}
	}
	return planOperatorStatusNone
}

func projectedWorkflowStage(state string) PlanWorkflowStage {
	switch state {
	case planStatePlanning:
		return planWorkflowStagePlanning
	case planStateReviewing:
		return planWorkflowStageReviewing
	case planStatePendingApproval:
		return planWorkflowStagePendingApproval
	case planStateImplementationReview:
		return planWorkflowStageImplementationReview
	case planStateMerging:
		return planWorkflowStageMerging
	case planStateCompleted, planStateResearchComplete, planStateMerged:
		return planWorkflowStageCompleted
	case planStateClosed, "cancelled", planStateRejected, planStatePlanningFailed, planStateImplementationFailed, planStateImplementationReviewFailed:
		return planWorkflowStageTerminal
	default:
		return planWorkflowStageExecuting
	}
}

func projectedExecutionDisposition(state string) PlanExecutionDisposition {
	switch state {
	case planStateCompleted, planStateMerged, planStateResearchComplete:
		return planExecutionDispositionSucceeded
	case planStatePlanningFailed, planStateImplementationFailed, planStateImplementationReviewFailed, planStateRejected:
		return planExecutionDispositionFailed
	case planStatePendingApproval, planStateWaitingOnDependency:
		return planExecutionDispositionWaiting
	case "cancelled", planStateClosed:
		return planExecutionDispositionCanceled
	default:
		return planExecutionDispositionRunning
	}
}

func projectPlan(bundle StoredPlan, tasks []pool.Task, pendingQuestions int) PlanProjection {
	active, completed, failed := summarizeRelevantPlanTasks(tasks, bundle)
	state := projectedPlanState(bundle, tasks, active, completed, failed)
	proj := PlanProjection{
		State:                state,
		Phase:                projectedPlanPhase(bundle, state, pendingQuestions),
		WorkflowStage:        projectedWorkflowStage(state),
		ExecutionDisposition: projectedExecutionDisposition(state),
		MergeDisposition:     projectedMergeDisposition(bundle, state, active, completed, failed),
		OperatorStatus:       projectedOperatorStatus(bundle, state, pendingQuestions),
		ActiveTaskIDs:        append([]string(nil), active...),
		CompletedTaskIDs:     append([]string(nil), completed...),
		FailedTaskIDs:        append([]string(nil), failed...),
	}
	sort.Strings(proj.ActiveTaskIDs)
	sort.Strings(proj.CompletedTaskIDs)
	sort.Strings(proj.FailedTaskIDs)
	return proj
}
