package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const (
	planStatePlanning                   = "planning"
	planStateReviewing                  = "reviewing"
	planStatePlanningFailed             = "planning_failed"
	planStatePendingApproval            = "pending_approval"
	planStateActive                     = "active"
	planStateCompleted                  = "completed"
	planStateImplementationReview       = "implementation_review"
	planStateImplementationReviewFailed = "implementation_review_failed"
	planStateMerged                     = "merged"
	planStateClosed                     = "closed"
	planStateRejected                   = "rejected"

	planReviewStatusPassed = "passed"
	planReviewStatusFailed = "failed"
)

func (k *Kitchen) SubmitIdea(idea string, lineage string, auto bool, review bool, reviewRounds int, maxReviewRevisions int, implReview bool) (*StoredPlan, error) {
	if k == nil || k.planStore == nil {
		return nil, fmt.Errorf("kitchen plan store not configured")
	}
	idea = strings.TrimSpace(idea)
	if idea == "" {
		return nil, fmt.Errorf("idea must not be empty")
	}
	review, reviewRounds, maxReviewRevisions, err := normalizePlanReviewRequest(review, reviewRounds, maxReviewRevisions)
	if err != nil {
		return nil, err
	}

	title := derivePlanTitle(idea)
	if lineage == "" {
		lineage = defaultLineage(title)
	}
	if err := validatePathComponent("lineage", lineage); err != nil {
		return nil, err
	}

	anchor, err := k.currentAnchor()
	if err != nil {
		return nil, err
	}
	planID := generatePlanID(title)
	planningTaskID := planTaskRuntimeID(planID, plannerTaskID)

	plan := PlanRecord{
		PlanID:  planID,
		Source:  "operator",
		Anchor:  anchor,
		Lineage: lineage,
		Title:   title,
		Summary: idea,
		State:   planStatePlanning,
	}

	execution := ExecutionRecord{
		State:               planStatePlanning,
		AutoApproved:        auto,
		ReviewRequested:     review,
		ReviewRounds:        reviewRounds,
		MaxReviewRevisions:  maxReviewRevisions,
		ImplReviewRequested: implReview,
		ActiveTaskIDs:       []string{planningTaskID},
		Branch:              lineageBranchName(lineage),
		Anchor:              anchor,
	}
	execution = appendPlanHistory(execution, PlanHistoryEntry{
		Type:    planHistoryPlanningStarted,
		Cycle:   1,
		TaskID:  planningTaskID,
		Summary: "Initial planner task queued.",
	})

	if _, err = k.planStore.Create(StoredPlan{
		Plan:      plan,
		Execution: execution,
	}); err != nil {
		return nil, err
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         planningTaskID,
		PlanID:     planID,
		Prompt:     buildIdeaPrompt(idea),
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		return nil, err
	}
	k.sendNotify(pool.Notification{Type: "plan_submitted", ID: planID, Message: plan.Title})
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return nil, err
	}
	return &bundle, nil
}

func normalizePlanReviewRequest(review bool, reviewRounds int, maxReviewRevisions int) (bool, int, int, error) {
	if reviewRounds < 0 {
		return false, 0, 0, fmt.Errorf("review rounds must be >= 0")
	}
	if maxReviewRevisions < -1 {
		return false, 0, 0, fmt.Errorf("max review revisions must be >= -1")
	}
	if reviewRounds > 0 {
		review = true
	}
	if !review {
		if maxReviewRevisions > 0 {
			return false, 0, 0, fmt.Errorf("max review revisions requires review")
		}
		return false, 0, 0, nil
	}
	if reviewRounds == 0 {
		reviewRounds = 1
	}
	if maxReviewRevisions < 0 {
		maxReviewRevisions = 1
	}
	return true, reviewRounds, maxReviewRevisions, nil
}

func reviewDraftPlan(plan PlanRecord, reviewRounds int) (string, []string) {
	findings := make([]string, 0, 4)
	if len(plan.Tasks) == 1 {
		findings = append(findings, "Draft plan has a single implementation task; split it later if the change spans multiple subsystems.")
	}

	hasOutputs := false
	hasSuccessCriteria := false
	for _, task := range plan.Tasks {
		if task.Outputs != nil && (len(task.Outputs.Files) > 0 || len(task.Outputs.Artifacts) > 0) {
			hasOutputs = true
		}
		if task.SuccessCriteria != nil && (strings.TrimSpace(task.SuccessCriteria.Advisory) != "" || len(task.SuccessCriteria.Verifiable) > 0) {
			hasSuccessCriteria = true
		}
	}
	if !hasOutputs {
		findings = append(findings, "Draft plan does not name concrete outputs yet.")
	}
	if !hasSuccessCriteria {
		findings = append(findings, "Draft plan does not define explicit success criteria yet.")
	}
	if reviewRounds > 1 {
		findings = append(findings, fmt.Sprintf("Review completed across %d deterministic rounds.", reviewRounds))
	}
	return planReviewStatusPassed, findings
}

func (k *Kitchen) ValidatePlan(plan PlanRecord) error {
	return validatePlanRecord(plan, func() *LineageManager {
		if k == nil {
			return nil
		}
		return k.lineageMgr
	}())
}

func validatePlanRecord(plan PlanRecord, lineageMgr *LineageManager) error {
	if strings.TrimSpace(plan.Lineage) == "" {
		return fmt.Errorf("plan lineage must not be empty")
	}
	if err := validatePathComponent("lineage", plan.Lineage); err != nil {
		return err
	}
	if strings.TrimSpace(plan.Title) == "" {
		return fmt.Errorf("plan title must not be empty")
	}
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("plan must include at least one task")
	}

	tasks := make(map[string]PlanTask, len(plan.Tasks))
	for _, task := range plan.Tasks {
		if err := validatePathComponent("task ID", task.ID); err != nil {
			return err
		}
		if _, exists := tasks[task.ID]; exists {
			return fmt.Errorf("duplicate task ID %q", task.ID)
		}
		if strings.TrimSpace(task.Title) == "" {
			return fmt.Errorf("task %s title must not be empty", task.ID)
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return fmt.Errorf("task %s prompt must not be empty", task.ID)
		}
		if !isValidComplexity(task.Complexity) {
			return fmt.Errorf("task %s has invalid complexity %q", task.ID, task.Complexity)
		}
		if task.ReviewComplexity != "" && !isValidComplexity(task.ReviewComplexity) {
			return fmt.Errorf("task %s has invalid review complexity %q", task.ID, task.ReviewComplexity)
		}
		if task.TimeoutMinutes < 0 {
			return fmt.Errorf("task %s timeoutMinutes must be >= 0", task.ID)
		}
		tasks[task.ID] = task
	}

	for _, task := range plan.Tasks {
		for _, dep := range task.Dependencies {
			if err := validatePathComponent("dependency", dep.Task); err != nil {
				return err
			}
			if dep.Task == task.ID {
				return fmt.Errorf("task %s cannot depend on itself", task.ID)
			}
			if _, ok := tasks[dep.Task]; !ok {
				return fmt.Errorf("task %s depends on unknown task %q", task.ID, dep.Task)
			}
		}
	}
	if err := validateTaskGraph(plan.Tasks); err != nil {
		return err
	}

	if lineageMgr != nil {
		activePlan, err := lineageMgr.ActivePlan(plan.Lineage)
		switch {
		case err == nil && activePlan != "" && activePlan != plan.PlanID:
			return fmt.Errorf("lineage %s already has active plan %s", plan.Lineage, activePlan)
		case err != nil && !os.IsNotExist(err):
			return err
		}
	}
	return nil
}

func (k *Kitchen) ApprovePlan(planID string) error {
	if k == nil || k.planStore == nil || k.pm == nil {
		return fmt.Errorf("kitchen is not fully configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	if bundle.Execution.State == planStateRejected {
		return fmt.Errorf("plan %s has been rejected", planID)
	}
	if bundle.Execution.State == planStatePlanning {
		return fmt.Errorf("plan %s is still planning", planID)
	}
	if bundle.Execution.State == planStateReviewing {
		return fmt.Errorf("plan %s is still under review", planID)
	}
	if bundle.Execution.State == planStatePlanningFailed {
		return fmt.Errorf("plan %s planning failed", planID)
	}
	if bundle.Execution.State == planStateActive {
		return nil
	}
	if pending := pendingQuestionsForPlan(k.pm, planID); len(pending) > 0 {
		return fmt.Errorf("plan %s has %d pending questions", planID, len(pending))
	}
	if err := k.ValidatePlan(bundle.Plan); err != nil {
		return err
	}
	if k.lineageMgr != nil {
		if err := k.lineageMgr.ActivatePlan(bundle.Plan.Lineage, planID); err != nil {
			return err
		}
	}

	activeTaskIDs := make([]string, 0, len(bundle.Plan.Tasks))
	for i, task := range bundle.Plan.Tasks {
		runtimeTaskID := planTaskRuntimeID(planID, task.ID)
		activeTaskIDs = append(activeTaskIDs, runtimeTaskID)

		if _, exists := k.pm.Task(runtimeTaskID); exists {
			continue
		}

		deps := make([]string, 0, len(task.Dependencies))
		for _, dep := range task.Dependencies {
			deps = append(deps, planTaskRuntimeID(planID, dep.Task))
		}
		if _, err := k.pm.EnqueueTask(pool.TaskSpec{
			ID:             runtimeTaskID,
			PlanID:         planID,
			Prompt:         task.Prompt,
			Complexity:     string(task.Complexity),
			Priority:       i + 1,
			DependsOn:      deps,
			TimeoutMinutes: task.TimeoutMinutes,
			Role:           "implementer",
		}); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	bundle.Plan.State = planStateActive
	bundle.Execution.State = planStateActive
	bundle.Execution.Approved = true
	bundle.Execution.Branch = lineageBranchName(bundle.Plan.Lineage)
	bundle.Execution.Anchor = bundle.Plan.Anchor
	bundle.Execution.ActiveTaskIDs = activeTaskIDs
	if bundle.Execution.ApprovedAt == nil {
		bundle.Execution.ApprovedAt = &now
	}
	if bundle.Execution.ActivatedAt == nil {
		bundle.Execution.ActivatedAt = &now
	}
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	k.sendNotify(pool.Notification{Type: "plan_approved", ID: planID, Message: bundle.Plan.Title})
	return nil
}

func (k *Kitchen) RejectPlan(planID string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen plan store not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	if bundle.Execution.State == planStateActive {
		return fmt.Errorf("plan %s is already active", planID)
	}
	if bundle.Execution.State == planStateRejected {
		return nil
	}
	bundle.Plan.State = planStateRejected
	bundle.Execution.State = planStateRejected
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	k.sendNotify(pool.Notification{Type: "plan_rejected", ID: planID, Message: bundle.Plan.Title})
	return nil
}

func (k *Kitchen) CancelPlan(planID string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen plan store not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	if bundle.Execution.State == "cancelled" || bundle.Plan.State == planStateClosed {
		return nil
	}
	if k.pm != nil {
		taskIDs := make([]string, 0, len(bundle.Plan.Tasks)+1)
		for _, task := range k.pm.Tasks() {
			if task.PlanID == planID {
				switch task.Status {
				case pool.TaskCompleted, pool.TaskFailed, pool.TaskCanceled, pool.TaskAccepted, pool.TaskRejected, pool.TaskEscalated:
					continue
				default:
					taskIDs = append(taskIDs, task.ID)
				}
			}
		}
		sort.Strings(taskIDs)
		for _, taskID := range taskIDs {
			if err := k.pm.CancelTask(taskID); err != nil {
				return err
			}
		}
	}
	now := time.Now().UTC()
	bundle.Plan.State = planStateClosed
	bundle.Execution.State = "cancelled"
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedAt = &now
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if k.lineageMgr != nil {
		_ = k.lineageMgr.ClearActivePlan(bundle.Plan.Lineage, planID)
	}
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	k.sendNotify(pool.Notification{Type: "plan_cancelled", ID: planID, Message: bundle.Plan.Title})
	return nil
}

func (k *Kitchen) DeletePlan(planID string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen plan store not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	if err := k.CancelPlan(planID); err != nil {
		return err
	}

	if len(bundle.Plan.Tasks) > 0 {
		gitMgr, err := k.gitManager()
		if err != nil {
			return err
		}
		for _, task := range bundle.Plan.Tasks {
			if err := gitMgr.DiscardChild(bundle.Plan.Lineage, planTaskRuntimeID(planID, task.ID)); err != nil {
				return err
			}
		}
	}

	if k.pm != nil {
		taskIDs := planTaskIDsForDeletion(bundle, k.pm.Tasks())
		for _, taskID := range taskIDs {
			if _, ok := k.pm.Task(taskID); !ok {
				continue
			}
			if err := k.pm.DeleteTask(taskID); err != nil {
				return err
			}
		}
	}

	if k.lineageMgr != nil {
		if err := k.lineageMgr.ClearActivePlan(bundle.Plan.Lineage, planID); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := k.planStore.Delete(planID); err != nil {
		return err
	}
	k.sendNotify(pool.Notification{Type: "plan_deleted", ID: planID, Message: bundle.Plan.Title})
	return nil
}

func planTaskIDsForDeletion(bundle StoredPlan, tasks []pool.Task) []string {
	ids := make(map[string]struct{}, len(bundle.Execution.ActiveTaskIDs)+len(bundle.Execution.CompletedTaskIDs)+len(bundle.Execution.FailedTaskIDs))
	for _, taskID := range bundle.Execution.ActiveTaskIDs {
		taskID = strings.TrimSpace(taskID)
		if taskID != "" {
			ids[taskID] = struct{}{}
		}
	}
	for _, taskID := range bundle.Execution.CompletedTaskIDs {
		taskID = strings.TrimSpace(taskID)
		if taskID != "" {
			ids[taskID] = struct{}{}
		}
	}
	for _, taskID := range bundle.Execution.FailedTaskIDs {
		taskID = strings.TrimSpace(taskID)
		if taskID != "" {
			ids[taskID] = struct{}{}
		}
	}
	for _, task := range tasks {
		if task.PlanID == bundle.Plan.PlanID {
			ids[task.ID] = struct{}{}
		}
	}
	result := make([]string, 0, len(ids))
	for taskID := range ids {
		result = append(result, taskID)
	}
	sort.Strings(result)
	return result
}

func (k *Kitchen) CancelTask(taskID string) error {
	if k == nil || k.pm == nil {
		return fmt.Errorf("kitchen pool manager not configured")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task ID must not be empty")
	}
	task, ok := k.pm.Task(taskID)
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	if err := k.pm.CancelTask(taskID); err != nil {
		return err
	}
	planID := strings.TrimSpace(task.PlanID)
	if planID == "" || k.planStore == nil {
		return nil
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	active, completed, failed := summarizePlanTasks(k.pm.Tasks(), planID)
	bundle.Execution.ActiveTaskIDs = active
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	bundle.Plan.State = planStateActive
	bundle.Execution.State = planStateActive
	bundle.Execution.CompletedAt = nil
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	return nil
}

func (k *Kitchen) RetryTask(taskID string, requireFreshWorker bool) error {
	if k == nil || k.pm == nil {
		return fmt.Errorf("kitchen pool manager not configured")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task ID must not be empty")
	}
	task, ok := k.pm.Task(taskID)
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	if err := k.pm.ReviveFailedTask(taskID, requireFreshWorker); err != nil {
		return err
	}
	planID := strings.TrimSpace(task.PlanID)
	if planID == "" || k.planStore == nil {
		return nil
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	active, completed, failed := summarizePlanTasks(k.pm.Tasks(), planID)
	bundle.Execution.ActiveTaskIDs = active
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	bundle.Plan.State = planStateActive
	bundle.Execution.State = planStateActive
	bundle.Execution.CompletedAt = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryManualRetried,
		Cycle:   planHistoryCycleForTask(planID, *task),
		TaskID:  task.ID,
		Summary: fmt.Sprintf("Manual retry requested (fresh worker required=%t).", requireFreshWorker),
	})
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	return nil
}

// FixConflicts creates a new conflict-resolution task for a previously
// failed task that has recorded merge-conflict information.
func (k *Kitchen) FixConflicts(taskID string) (string, error) {
	if k == nil || k.pm == nil || k.planStore == nil {
		return "", fmt.Errorf("kitchen is not fully configured")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", fmt.Errorf("task ID must not be empty")
	}
	task, ok := k.pm.Task(taskID)
	if !ok {
		return "", fmt.Errorf("task %s not found", taskID)
	}
	if task.Status != pool.TaskFailed {
		return "", fmt.Errorf("task %s is not failed (status: %s)", taskID, task.Status)
	}
	if task.Result == nil || task.Result.Conflict == nil {
		return "", fmt.Errorf("no conflict info recorded for task; was it failed by a merge conflict?")
	}

	planID := strings.TrimSpace(task.PlanID)
	if planID == "" {
		return "", fmt.Errorf("task %s has no associated plan", taskID)
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return "", err
	}

	// Find the original PlanTask entry for the failed task.
	var originalTask *PlanTask
	for i := range bundle.Plan.Tasks {
		if planTaskRuntimeID(planID, bundle.Plan.Tasks[i].ID) == taskID {
			originalTask = &bundle.Plan.Tasks[i]
			break
		}
	}
	if originalTask == nil {
		return "", fmt.Errorf("task %s not found in plan %s", taskID, planID)
	}

	prompt := buildConflictFixPrompt(originalTask.Prompt, task.Result.Conflict)

	// Generate a unique short ID for the new conflict-fix task.
	now := time.Now().UTC().Format("20060102T150405")
	newPlanTaskID := "conflict-fix-" + now
	newRuntimeTaskID := planTaskRuntimeID(planID, newPlanTaskID)

	newPlanTask := PlanTask{
		ID:               newPlanTaskID,
		Title:            "Fix conflicts: " + originalTask.Title,
		Prompt:           prompt,
		Complexity:       originalTask.Complexity,
		Outputs:          originalTask.Outputs,
		SuccessCriteria:  originalTask.SuccessCriteria,
		ReviewComplexity: originalTask.ReviewComplexity,
		TimeoutMinutes:   originalTask.TimeoutMinutes,
		// No dependencies: starts immediately.
	}

	if err := k.planStore.AddTask(planID, newPlanTask); err != nil {
		return "", fmt.Errorf("add conflict-fix task to plan: %w", err)
	}

	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:                 newRuntimeTaskID,
		PlanID:             planID,
		Prompt:             prompt,
		Complexity:         string(newPlanTask.Complexity),
		Priority:           1,
		TimeoutMinutes:     newPlanTask.TimeoutMinutes,
		Role:               "implementer",
		RequireFreshWorker: true,
	}); err != nil {
		return "", fmt.Errorf("enqueue conflict-fix task: %w", err)
	}

	// Reload bundle to apply the AddTask change before appending history.
	bundle, err = k.planStore.Get(planID)
	if err != nil {
		return "", err
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryConflictFixRequested,
		TaskID:  newRuntimeTaskID,
		Summary: "Conflict fix task created for: " + originalTask.Title,
	})
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return "", err
	}

	return newRuntimeTaskID, nil
}

// FixLineageConflicts enqueues a fix-lineage-merge task that runs a
// worker inside a worktree pre-loaded with the in-progress lineage→base
// merge. The worker resolves conflicts, runs any tests the plan calls
// out, commits the merge, and scheduler finalisation fast-forwards the
// base branch onto the resolved commit.
func (k *Kitchen) FixLineageConflicts(lineage string) (string, error) {
	if k == nil || k.pm == nil || k.planStore == nil {
		return "", fmt.Errorf("kitchen is not fully configured")
	}
	lineage = strings.TrimSpace(lineage)
	if err := validatePathComponent("lineage", lineage); err != nil {
		return "", err
	}

	activePlanID, _ := k.lineageMgr.ActivePlan(lineage)
	if activePlanID == "" {
		return "", fmt.Errorf("lineage %s has no active plan", lineage)
	}
	bundle, err := k.planStore.Get(activePlanID)
	if err != nil {
		return "", err
	}
	baseBranch := k.baseBranchForLineage(lineage)

	// Check first — if it's actually clean we have nothing to do.
	gitMgr, err := k.gitManager()
	if err != nil {
		return "", err
	}
	clean, conflictFiles, err := gitMgr.MergeCheck(lineage, baseBranch)
	if err != nil {
		return "", err
	}
	if clean {
		return "", fmt.Errorf("merge from %s into %s is already clean", lineage, baseBranch)
	}

	fixTaskID := "fix-merge-" + time.Now().UTC().Format("20060102T150405")
	runtimeTaskID := planTaskRuntimeID(activePlanID, fixTaskID)
	prompt := buildLineageFixMergePrompt(baseBranch, lineage, conflictFiles, bundle.Plan.Title)

	// Register the fix task on the plan so it shows up alongside the
	// other tasks in the TUI's Tasks pane (buildTaskItems iterates
	// plan.Tasks). Without this the task exists only in the pool and
	// the operator has no way to track it.
	planTask := PlanTask{
		ID:               fixTaskID,
		Title:            fmt.Sprintf("Fix %s→%s merge conflicts", lineage, baseBranch),
		Prompt:           prompt,
		Complexity:       ComplexityMedium,
		ReviewComplexity: ComplexityMedium,
		Outputs: &PlanOutputs{
			Files: append([]string(nil), conflictFiles...),
		},
	}
	if err := k.planStore.AddTask(activePlanID, planTask); err != nil {
		return "", fmt.Errorf("add fix-merge task to plan: %w", err)
	}

	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:                 runtimeTaskID,
		PlanID:             activePlanID,
		Prompt:             prompt,
		Complexity:         string(ComplexityMedium),
		Priority:           1,
		TimeoutMinutes:     0,
		Role:               lineageFixMergeRole,
		RequireFreshWorker: true,
	}); err != nil {
		return "", fmt.Errorf("enqueue fix-lineage-merge task: %w", err)
	}

	// Reload to pick up the AddTask change, flip the plan back to
	// active (the fix task is a new pending work item), and append
	// history so the TUI reflects the new state immediately rather
	// than waiting for the scheduler's next syncPlanExecution pass.
	bundle, err = k.planStore.Get(activePlanID)
	if err != nil {
		return "", err
	}
	active, completed, failed := summarizePlanTasks(k.pm.Tasks(), activePlanID)
	bundle.Execution.ActiveTaskIDs = active
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	bundle.Plan.State = planStateActive
	bundle.Execution.State = planStateActive
	bundle.Execution.CompletedAt = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageFixMergeRequested,
		TaskID:  runtimeTaskID,
		Summary: fmt.Sprintf("Resolve lineage→%s merge conflicts in: %s", baseBranch, strings.Join(conflictFiles, ", ")),
	})
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return "", err
	}
	if err := k.planStore.UpdateExecution(activePlanID, bundle.Execution); err != nil {
		return "", err
	}
	k.sendNotify(pool.Notification{Type: "lineage_fix_merge_requested", ID: runtimeTaskID, Message: lineage})
	return runtimeTaskID, nil
}

func buildLineageFixMergePrompt(baseBranch, lineage string, files []string, planTitle string) string {
	var sb strings.Builder
	sb.WriteString("You are catching the Kitchen lineage branch up to the base branch so a later merge will be a trivial fast-forward.\n\n")
	sb.WriteString("## Context\n")
	sb.WriteString("- Lineage: `")
	sb.WriteString(lineage)
	sb.WriteString("` (the accumulated work from plan: ")
	sb.WriteString(planTitle)
	sb.WriteString(")\n- Base: `")
	sb.WriteString(baseBranch)
	sb.WriteString("` (has drifted since the lineage started)\n")
	sb.WriteString("\nYour working directory sits on a throwaway fix branch forked from the lineage, with `git merge --no-ff --no-commit ")
	sb.WriteString(baseBranch)
	sb.WriteString("` already in progress — `git status` shows the conflicting files with their `<<<<<<<` / `=======` / `>>>>>>>` markers. HEAD is the fix branch, NOT the lineage branch itself (Kitchen will fast-forward the lineage onto your resolution commit when you finish).\n\n")
	sb.WriteString("## Conflicting files\n")
	for _, f := range files {
		sb.WriteString("- ")
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	sb.WriteString("\n## Your job\n")
	sb.WriteString("1. Resolve every conflict marker. Keep both sides' intent whenever that is what each change was trying to achieve — do not drop the lineage's work and do not drop the base's work unless one strictly supersedes the other.\n")
	sb.WriteString("2. Run the repository's standard verification (build, tests, linters — look at the project instructions if you are unsure what's standard) to make sure the resolved tree is healthy.\n")
	sb.WriteString("3. Once it passes, `git add` the resolved files and commit with a message like `Merge ")
	sb.WriteString(baseBranch)
	sb.WriteString(" into ")
	sb.WriteString(lineage)
	sb.WriteString(" (conflict resolution)`.\n")
	sb.WriteString("4. Do NOT touch the base branch, do not amend, do not rebase — a single resolution commit on your fix branch is enough. Kitchen fast-forwards the lineage branch onto your commit once the task completes; the base branch is left untouched and the operator still runs the normal `kitchen merge` to deliver the lineage.\n")
	return sb.String()
}

func buildConflictFixPrompt(originalPrompt string, info *pool.ConflictInfo) string {
	var sb strings.Builder
	sb.WriteString("You are re-implementing a task that previously failed due to merge conflicts.\n\n")
	sb.WriteString("## Original task goal\n")
	sb.WriteString(strings.TrimSpace(originalPrompt))
	sb.WriteString("\n\n## Conflict context\n")
	sb.WriteString("The following files had merge conflicts when your previous attempt was merged into the lineage branch:\n")
	for _, f := range info.ConflictingFiles {
		sb.WriteString("- ")
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	sb.WriteString("\n## What the lineage branch changed in those files\n")
	sb.WriteString("The diff below shows the changes that were already present in the lineage branch (made by other concurrent tasks) that conflicted with your previous implementation:\n\n")
	sb.WriteString("```diff\n")
	sb.WriteString(info.LineageDiff)
	sb.WriteString("\n```\n\n")
	sb.WriteString("## Your job\n")
	sb.WriteString("Re-implement the original task goal above. Your implementation MUST be compatible with the lineage changes shown in the diff. Do not revert or duplicate the lineage changes — work alongside them to achieve the original goal.")
	return sb.String()
}

func planHistoryCycleForTask(planID string, task pool.Task) int {
	if isPlanReviewTask(task) {
		return reviewCycleForTask(planID, task.ID)
	}
	if strings.HasPrefix(task.ID, planTaskRuntimeID(planID, plannerTaskID)) || strings.HasPrefix(task.ID, planTaskRuntimeID(planID, planRevisionTaskID+"-")) {
		return plannerCycleForTask(planID, task.ID)
	}
	return 1
}

func (k *Kitchen) Replan(planID, reason string) (string, error) {
	if k == nil || k.planStore == nil {
		return "", fmt.Errorf("kitchen plan store not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return "", err
	}
	review, rounds, maxRevisions, err := normalizePlanReviewRequest(
		bundle.Execution.ReviewRequested,
		bundle.Execution.ReviewRounds,
		bundle.Execution.MaxReviewRevisions,
	)
	if err != nil {
		return "", err
	}

	newPlan := bundle.Plan
	newPlan.PlanID = ""
	newPlan.State = planStatePendingApproval
	newPlan.Source = "replan"
	newPlan.CreatedAt = time.Time{}
	newPlan.UpdatedAt = time.Time{}
	if reason = strings.TrimSpace(reason); reason != "" {
		if newPlan.Summary != "" {
			newPlan.Summary += "\n\n"
		}
		newPlan.Summary += "Replan requested: " + reason
	}
	if len(newPlan.Tasks) == 0 {
		replanned, err := k.SubmitIdea(newPlan.Summary, newPlan.Lineage, false, review, rounds, maxRevisions, bundle.Execution.ImplReviewRequested)
		if err != nil {
			return "", err
		}
		// Replan supersedes the old plan: delete it once the
		// successor is safely persisted so the operator doesn't have
		// to clean up the previous record by hand.
		if err := k.DeletePlan(planID); err != nil {
			return "", fmt.Errorf("delete superseded plan %s: %w", planID, err)
		}
		k.sendNotify(pool.Notification{Type: "plan_replanned", ID: replanned.Plan.PlanID, Message: replanned.Plan.Title})
		return replanned.Plan.PlanID, nil
	}
	if err := k.ValidatePlan(newPlan); err != nil {
		return "", err
	}

	newPlanID, err := k.planStore.Create(StoredPlan{
		Plan: newPlan,
		Execution: ExecutionRecord{
			State:        planStatePendingApproval,
			AutoApproved: false,
			Branch:       lineageBranchName(newPlan.Lineage),
			Anchor:       newPlan.Anchor,
		},
		Affinity: AffinityRecord{
			PreferredProviders: append([]PoolKey(nil), bundle.Affinity.PreferredProviders...),
		},
	})
	if err != nil {
		return "", err
	}
	// Replan supersedes the old plan: delete it once the successor is
	// safely persisted so the operator doesn't have to clean up the
	// previous record by hand.
	if err := k.DeletePlan(planID); err != nil {
		return "", fmt.Errorf("delete superseded plan %s: %w", planID, err)
	}
	k.sendNotify(pool.Notification{Type: "plan_replanned", ID: newPlanID, Message: newPlan.Title})
	return newPlanID, nil
}

func (k *Kitchen) ListPlans(includeCompleted bool) ([]PlanRecord, error) {
	if k == nil || k.planStore == nil {
		return nil, fmt.Errorf("kitchen plan store not configured")
	}
	plans, err := k.planStore.List()
	if err != nil {
		return nil, err
	}
	if includeCompleted {
		return plans, nil
	}

	filtered := make([]PlanRecord, 0, len(plans))
	for _, plan := range plans {
		switch plan.State {
		case planStateCompleted, planStateMerged, planStateClosed, planStateRejected:
			continue
		default:
			filtered = append(filtered, plan)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})
	return filtered, nil
}

func (k *Kitchen) GetPlan(planID string) (StoredPlan, error) {
	if k == nil || k.planStore == nil {
		return StoredPlan{}, fmt.Errorf("kitchen plan store not configured")
	}
	return k.planStore.Get(planID)
}

func (k *Kitchen) ListQuestions() []pool.Question {
	if k == nil || k.pm == nil {
		return nil
	}
	questions := k.pm.PendingQuestions()
	sort.Slice(questions, func(i, j int) bool {
		return questions[i].AskedAt.Before(questions[j].AskedAt)
	})
	return questions
}

func (k *Kitchen) InvalidateAffinity(planID, reason string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen plan store not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "operator_invalidated_affinity"
	}
	bundle.Affinity.PlannerWorkerID = ""
	bundle.Affinity.PreferredProviders = nil
	bundle.Affinity.Invalidated = true
	bundle.Affinity.InvalidationReason = reason
	bundle.Affinity.InvalidatedAt = &now
	return k.planStore.UpdateAffinity(planID, bundle.Affinity)
}

func (k *Kitchen) QueueSnapshot() map[string]any {
	if k == nil || k.pm == nil {
		return map[string]any{}
	}
	taskSummaries := k.pm.TaskSummaries()
	sort.Slice(taskSummaries, func(i, j int) bool {
		if taskSummaries[i].Priority == taskSummaries[j].Priority {
			return taskSummaries[i].ID < taskSummaries[j].ID
		}
		return taskSummaries[i].Priority < taskSummaries[j].Priority
	})
	return map[string]any{
		"tasks":            taskSummaries,
		"aliveWorkers":     k.pm.AliveWorkers(),
		"maxWorkers":       k.pm.MaxWorkers(),
		"pendingQuestions": len(k.pm.PendingQuestions()),
	}
}

const (
	evidenceTierCompact = "compact"
	evidenceTierRich    = "rich"
)

func (k *Kitchen) Evidence(planID string) (map[string]any, error) {
	return k.EvidenceWithTier(planID, evidenceTierRich)
}

func normalizeEvidenceTier(raw string) (string, error) {
	tier := strings.TrimSpace(strings.ToLower(raw))
	if tier == "" {
		return evidenceTierRich, nil
	}
	switch tier {
	case evidenceTierCompact, evidenceTierRich:
		return tier, nil
	default:
		return "", fmt.Errorf("tier must be compact or rich")
	}
}

func (k *Kitchen) EvidenceWithTier(planID, tier string) (map[string]any, error) {
	tier, err := normalizeEvidenceTier(tier)
	if err != nil {
		return nil, err
	}
	bundle, err := k.GetPlan(planID)
	if err != nil {
		return nil, err
	}
	progress, err := k.planProgress(bundle)
	if err != nil {
		return nil, err
	}
	if tier == evidenceTierCompact {
		return k.compactEvidence(bundle, progress)
	}
	var tasks []pool.Task
	for _, task := range k.pm.Tasks() {
		if task.PlanID == planID {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	lineages := []LineageState(nil)
	if k.lineageMgr != nil {
		lineages, _ = k.lineageMgr.List()
	}
	return map[string]any{
		"tier":            evidenceTierRich,
		"plan":            bundle.Plan,
		"execution":       bundle.Execution,
		"affinity":        bundle.Affinity,
		"progress":        progress,
		"history":         append([]PlanHistoryEntry(nil), bundle.Execution.History...),
		"tasks":           tasks,
		"questions":       k.ListQuestions(),
		"queue":           k.QueueSnapshot(),
		"workers":         k.pm.Workers(),
		"runtimeActivity": k.runtimeActivityForWorkers(tasksToWorkers(k.pm.Workers(), tasks)),
		"lineages":        lineages,
		"generatedAt":     time.Now().UTC(),
	}, nil
}

func (k *Kitchen) compactEvidence(bundle StoredPlan, progress PlanProgress) (map[string]any, error) {
	anchorCommit := strings.TrimSpace(bundle.Plan.Anchor.Commit)
	baseBranch := k.baseBranchForLineage(bundle.Plan.Lineage)
	currentRef := baseBranch
	if strings.TrimSpace(bundle.Plan.Lineage) != "" {
		lineageRef := lineageBranchName(bundle.Plan.Lineage)
		if _, err := runGit(k.repoPath, "rev-parse", lineageRef); err == nil {
			currentRef = lineageRef
		}
	}

	currentHead := ""
	commitsSinceAnchor := 0
	if strings.TrimSpace(k.repoPath) != "" && strings.TrimSpace(currentRef) != "" {
		if head, err := runGit(k.repoPath, "rev-parse", currentRef); err == nil {
			currentHead = strings.TrimSpace(head)
		}
		if anchorCommit != "" {
			if count, err := runGit(k.repoPath, "rev-list", "--count", anchorCommit+".."+currentRef); err == nil {
				_, _ = fmt.Sscanf(strings.TrimSpace(count), "%d", &commitsSinceAnchor)
			}
		}
	}

	activeSet := make(map[string]bool, len(progress.ActiveTaskIDs))
	for _, id := range progress.ActiveTaskIDs {
		activeSet[id] = true
	}
	completedSet := make(map[string]bool, len(progress.CompletedTaskIDs))
	for _, id := range progress.CompletedTaskIDs {
		completedSet[id] = true
	}
	failedSet := make(map[string]bool, len(progress.FailedTaskIDs))
	for _, id := range progress.FailedTaskIDs {
		failedSet[id] = true
	}

	pendingCount := 0
	for _, task := range bundle.Plan.Tasks {
		runtimeID := planTaskRuntimeID(bundle.Plan.PlanID, task.ID)
		if activeSet[runtimeID] || completedSet[runtimeID] || failedSet[runtimeID] {
			continue
		}
		pendingCount++
	}

	return map[string]any{
		"tier":               evidenceTierCompact,
		"planId":             bundle.Plan.PlanID,
		"lineage":            bundle.Plan.Lineage,
		"state":              bundle.Execution.State,
		"phase":              progress.Phase,
		"anchorCommit":       anchorCommit,
		"baseBranch":         baseBranch,
		"currentHead":        currentHead,
		"commitsSinceAnchor": commitsSinceAnchor,
		"taskCounts": map[string]any{
			"total":     len(bundle.Plan.Tasks),
			"active":    len(progress.ActiveTaskIDs),
			"completed": len(progress.CompletedTaskIDs),
			"failed":    len(progress.FailedTaskIDs),
			"pending":   pendingCount,
		},
		"generatedAt": time.Now().UTC(),
	}, nil
}

func (k *Kitchen) runtimeActivityForWorkers(workers []pool.Worker) map[string]*pool.WorkerActivity {
	if k == nil || k.hostAPI == nil || len(workers) == 0 {
		return nil
	}
	activity := make(map[string]*pool.WorkerActivity)
	for _, worker := range workers {
		if worker.Status == pool.WorkerDead {
			continue
		}
		item, err := k.hostAPI.GetWorkerActivity(context.Background(), worker.ID)
		if err != nil || item == nil {
			continue
		}
		activity[worker.ID] = item
	}
	if len(activity) == 0 {
		return nil
	}
	return activity
}

func tasksToWorkers(workers []pool.Worker, tasks []pool.Task) []pool.Worker {
	if len(tasks) == 0 || len(workers) == 0 {
		return workers
	}
	active := make(map[string]bool)
	for _, task := range tasks {
		if strings.TrimSpace(task.WorkerID) != "" {
			active[task.WorkerID] = true
		}
	}
	if len(active) == 0 {
		return workers
	}
	filtered := make([]pool.Worker, 0, len(active))
	for _, worker := range workers {
		if active[worker.ID] {
			filtered = append(filtered, worker)
		}
	}
	return filtered
}

func (k *Kitchen) baseBranchForLineage(lineage string) string {
	if k != nil && k.lineageMgr != nil && k.planStore != nil {
		if activePlan, err := k.lineageMgr.ActivePlan(lineage); err == nil && activePlan != "" {
			if bundle, err := k.planStore.Get(activePlan); err == nil && strings.TrimSpace(bundle.Plan.Anchor.Branch) != "" {
				return bundle.Plan.Anchor.Branch
			}
		}
	}
	anchor, err := k.currentAnchor()
	if err == nil && strings.TrimSpace(anchor.Branch) != "" {
		return anchor.Branch
	}
	return "main"
}

func (k *Kitchen) currentAnchor() (PlanAnchor, error) {
	if strings.TrimSpace(k.repoPath) == "" {
		return PlanAnchor{}, fmt.Errorf("repo path not configured")
	}
	commit, err := runGit(k.repoPath, "rev-parse", "HEAD")
	if err != nil {
		return PlanAnchor{}, err
	}
	branch, err := runGit(k.repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return PlanAnchor{}, err
	}
	return PlanAnchor{
		Commit:    strings.TrimSpace(commit),
		Branch:    strings.TrimSpace(branch),
		Timestamp: time.Now().UTC(),
	}, nil
}

func derivePlanTitle(idea string) string {
	line := strings.TrimSpace(strings.SplitN(idea, "\n", 2)[0])
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return "Kitchen plan"
	}
	runes := []rune(line)
	if len(runes) > 72 {
		return string(runes[:72]) + "..."
	}
	return line
}

// sanitizeLineageSlug normalises an AI-generated lineage string into a
// value that satisfies validatePathComponent. Planners frequently return
// git-branch-style names like "feat/kitchen-headless" because that's
// what lineages look like in git, but kitchen uses the lineage as both
// a directory name and a sub-component of a git ref, so slashes and
// backslashes must be collapsed. Returns empty string when the input
// is empty, "." / "..", or collapses to nothing after sanitisation,
// so callers can keep the existing lineage instead of failing the
// whole planning run.
func sanitizeLineageSlug(raw string) string {
	slug := nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), "-")
	slug = strings.Trim(slug, "-.")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-.")
	}
	if slug == "" || slug == "." || slug == ".." {
		return ""
	}
	return slug
}

func defaultLineage(title string) string {
	slug := nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "idea"
	}
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	if slug == "" {
		return "idea"
	}
	return slug
}

func buildIdeaPrompt(idea string) string {
	return "Create an execution plan for the following idea in this repository. Break the work into concrete worker-ready tasks, define outputs and success criteria, and include operator questions only when an answer is genuinely needed to plan safely.\n\nIdea:\n" + strings.TrimSpace(idea)
}

func planTaskRuntimeID(planID, taskID string) string {
	return planID + "-" + taskID
}

func pendingQuestionsForPlan(pm *pool.PoolManager, planID string) []pool.Question {
	if pm == nil || strings.TrimSpace(planID) == "" {
		return nil
	}
	var questions []pool.Question
	for _, q := range pm.PendingQuestions() {
		if strings.TrimSpace(q.TaskID) == "" {
			continue
		}
		task, ok := pm.Task(q.TaskID)
		if !ok || task.PlanID != planID {
			continue
		}
		questions = append(questions, q)
	}
	sort.Slice(questions, func(i, j int) bool {
		return questions[i].AskedAt.Before(questions[j].AskedAt)
	})
	return questions
}

func buildPlanReviewPrompt(plan PlanRecord, reviewRounds int) (string, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal plan for review: %w", err)
	}
	return adapter.BuildPlanReviewPrompt(string(data), plan.Summary, reviewRounds), nil
}

func planReviewRuntimeID(planID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return planTaskRuntimeID(planID, fmt.Sprintf("%s-%d", planReviewTaskID, attempt))
}

func planRevisionRuntimeID(planID string, revision int) string {
	if revision <= 0 {
		revision = 1
	}
	return planTaskRuntimeID(planID, fmt.Sprintf("%s-%d", planRevisionTaskID, revision))
}

func planImplReviewRuntimeID(planID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return planTaskRuntimeID(planID, fmt.Sprintf("%s-%d", implReviewTaskID, attempt))
}

func implReviewRuntimeID(planID string) string {
	return planImplReviewRuntimeID(planID, 1)
}

func planReviewFindings(verdict, feedback, severity string) []string {
	var findings []string
	if verdict == pool.ReviewFail && strings.TrimSpace(severity) != "" {
		findings = append(findings, "Severity: "+strings.TrimSpace(severity))
	}
	for _, line := range strings.Split(strings.TrimSpace(feedback), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			findings = append(findings, line)
		}
	}
	if len(findings) == 0 {
		if verdict == pool.ReviewPass {
			findings = append(findings, "Plan review passed.")
		} else {
			findings = append(findings, "Plan review failed.")
		}
	}
	return findings
}

func buildPlanRevisionPrompt(plan PlanRecord, findings []string) (string, error) {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal plan for revision: %w", err)
	}
	return adapter.BuildPlanRevisionPrompt(string(data), strings.Join(findings, "\n"), plan.Summary), nil
}

func appendUniqueIDs(existing []string, ids ...string) []string {
	seen := make(map[string]bool, len(existing)+len(ids))
	result := make([]string, 0, len(existing)+len(ids))
	for _, id := range existing {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

func planFromArtifact(existing PlanRecord, artifact *adapter.PlanArtifact) PlanRecord {
	plan := existing
	if artifact == nil {
		return plan
	}
	if lineage := sanitizeLineageSlug(artifact.Lineage); lineage != "" {
		plan.Lineage = lineage
	}
	if title := strings.TrimSpace(artifact.Title); title != "" {
		plan.Title = title
	}
	if summary := strings.TrimSpace(artifact.Summary); summary != "" {
		plan.Summary = summary
	}
	if artifact.Ownership != nil {
		plan.Ownership = PlanOwnership{
			Packages:  append([]string(nil), artifact.Ownership.Packages...),
			Exclusive: artifact.Ownership.Exclusive,
		}
	}
	plan.Tasks = make([]PlanTask, 0, len(artifact.Tasks))
	for _, task := range artifact.Tasks {
		planned := PlanTask{
			ID:               task.ID,
			Title:            task.Title,
			Prompt:           task.Prompt,
			Complexity:       Complexity(task.Complexity),
			Dependencies:     make([]PlanDependency, 0, len(task.Dependencies)),
			ReviewComplexity: Complexity(task.ReviewComplexity),
		}
		for _, dep := range task.Dependencies {
			planned.Dependencies = append(planned.Dependencies, PlanDependency{Task: dep})
		}
		if planned.ReviewComplexity == "" {
			planned.ReviewComplexity = planned.Complexity
		}
		if task.Outputs != nil {
			planned.Outputs = &PlanOutputs{
				Files:     append([]string(nil), task.Outputs.Files...),
				Artifacts: append([]string(nil), task.Outputs.Artifacts...),
			}
		}
		if task.SuccessCriteria != nil {
			planned.SuccessCriteria = &PlanSuccessCriteria{
				Advisory:   task.SuccessCriteria.Advisory,
				Verifiable: append([]string(nil), task.SuccessCriteria.Verifiable...),
			}
		}
		plan.Tasks = append(plan.Tasks, planned)
	}
	return plan
}

const (
	plannerTaskID       = "plan"
	plannerTaskRole     = "planner"
	planReviewTaskID    = "plan-review"
	planRevisionTaskID  = "plan-revise"
	lineageFixMergeRole = "lineage-fix-merge"
	implReviewTaskID    = "impl-review"
)

func isValidComplexity(value Complexity) bool {
	for _, complexity := range allComplexities {
		if value == complexity {
			return true
		}
	}
	return false
}

func validateTaskGraph(tasks []PlanTask) error {
	graph := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		graph[task.ID] = dependencyTaskIDs(task.Dependencies)
	}

	visiting := make(map[string]bool, len(graph))
	visited := make(map[string]bool, len(graph))
	var walk func(string) error
	walk = func(taskID string) error {
		if visiting[taskID] {
			return fmt.Errorf("task graph contains a cycle involving %s", taskID)
		}
		if visited[taskID] {
			return nil
		}
		visiting[taskID] = true
		for _, dep := range graph[taskID] {
			if err := walk(dep); err != nil {
				return err
			}
		}
		visiting[taskID] = false
		visited[taskID] = true
		return nil
	}

	for taskID := range graph {
		if err := walk(taskID); err != nil {
			return err
		}
	}
	return nil
}
