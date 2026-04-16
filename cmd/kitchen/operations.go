package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func repoLineageHasPendingChanges(repoPath, baseBranch, lineageBranch string) (bool, error) {
	mergeBase, err := runGit(repoPath, "merge-base", baseBranch, lineageBranch)
	if err != nil {
		return false, err
	}
	_, err = runGit(repoPath, "diff", "--quiet", strings.TrimSpace(mergeBase)+".."+lineageBranch)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

func (k *Kitchen) StatusSnapshot() (map[string]any, error) {
	return k.StatusSnapshotWithLimit(-1)
}

func (k *Kitchen) StatusSnapshotWithLimit(historyLimit int) (map[string]any, error) {
	if k == nil {
		return nil, fmt.Errorf("kitchen not configured")
	}
	historyLimit, snapshotMeta := k.resolveSnapshotHistoryLimit(historyLimit)

	anchor, err := k.currentAnchor()
	if err != nil {
		return nil, err
	}

	workers := []pool.Worker(nil)
	if k.pm != nil {
		workers = k.pm.Workers()
		sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	}

	lineages := []LineageState(nil)
	if k.lineageMgr != nil {
		lineages, err = k.lineageMgr.List()
		if err != nil {
			return nil, err
		}
	}

	providers := map[string]HealthEntry{}
	if k.health != nil {
		providers = k.health.Snapshot()
	}

	plans := []PlanProgress(nil)
	if k.planStore != nil {
		plans, err = k.OpenPlanProgressWithLimit(historyLimit)
		if err != nil {
			return nil, err
		}
	}

	return map[string]any{
		"repoPath":        k.repoPath,
		"anchor":          anchor,
		"queue":           k.QueueSnapshot(),
		"workers":         workers,
		"runtimeActivity": k.runtimeActivityForWorkers(workers),
		"plans":           plans,
		"snapshot":        snapshotMeta,
		"lineages":        lineages,
		"providers":       providers,
		"generatedAt":     time.Now().UTC(),
	}, nil
}

func (k *Kitchen) ResetProviderKey(key string) error {
	if k == nil || k.health == nil {
		return fmt.Errorf("provider health is not configured")
	}
	provider, model, ok := strings.Cut(strings.TrimSpace(key), "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return fmt.Errorf("provider key must be in provider/model form")
	}
	return k.health.Reset(strings.TrimSpace(provider) + "/" + strings.TrimSpace(model))
}

func (k *Kitchen) MergeLineage(lineage string) (map[string]any, error) {
	return k.MergeLineageWithOptions(lineage, false)
}

func (k *Kitchen) MergeLineageWithOptions(lineage string, allowFallback bool) (map[string]any, error) {
	_ = allowFallback
	if k == nil {
		return nil, fmt.Errorf("kitchen not configured")
	}

	var activePlanID string
	if k.lineageMgr != nil {
		activePlanID, _ = k.lineageMgr.ActivePlan(lineage)
	}
	if activePlanID != "" {
		if blocked := k.blockingTasksForPlan(activePlanID); len(blocked) > 0 {
			return nil, fmt.Errorf("lineage %s has unfinished tasks: %s", lineage, strings.Join(blocked, ", "))
		}
		bundle, err := k.planStore.Get(activePlanID)
		if err != nil {
			return nil, err
		}
		if executionHasBlockingImplementationReviewFailure(bundle.Execution) {
			return nil, fmt.Errorf("lineage %s failed post-implementation review: %s", lineage, strings.Join(bundle.Execution.ImplReviewFindings, "; "))
		}
	}

	baseBranch := k.baseBranchForLineage(lineage)
	lineageBranch := lineageBranchName(lineage)
	hasChanges, err := repoLineageHasPendingChanges(k.repoPath, baseBranch, lineageBranch)
	if err != nil {
		return nil, err
	}
	if !hasChanges {
		return nil, fmt.Errorf("lineage %s has no changes to merge into %s", lineage, baseBranch)
	}
	if activePlanID == "" {
		return nil, fmt.Errorf("lineage %s has no active plan to merge", lineage)
	}
	bundle, err := k.planStore.Get(activePlanID)
	if err != nil {
		return nil, err
	}
	taskID, err := k.enqueueLineageMergeTask(activePlanID, bundle, lineage, baseBranch)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":     "merge_queued",
		"baseBranch": baseBranch,
		"mode":       "squash",
		"planId":     activePlanID,
		"newTaskId":  taskID,
	}, nil
}

func (k *Kitchen) enqueueLineageMergeTask(activePlanID string, bundle StoredPlan, lineage, baseBranch string) (string, error) {
	lineageBranch := lineageBranchName(lineage)
	logicalTaskID := "merge-" + time.Now().UTC().Format("20060102T150405")
	runtimeTaskID := planTaskRuntimeID(activePlanID, logicalTaskID)
	prompt, _, err := buildSquashCommitMessageTaskPrompt(k.repoPath, lineageBranch, baseBranch, bundle.Plan.Title, bundle.Plan.Summary)
	if err != nil {
		return "", err
	}

	planTask := PlanTask{
		ID:               logicalTaskID,
		Title:            fmt.Sprintf("Merge %s into %s", lineage, baseBranch),
		Prompt:           prompt,
		Complexity:       ComplexityMedium,
		ReviewComplexity: ComplexityMedium,
	}
	if err := k.planStore.AddTask(activePlanID, planTask); err != nil {
		return "", fmt.Errorf("add merge task to plan: %w", err)
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:                 runtimeTaskID,
		PlanID:             activePlanID,
		Prompt:             prompt,
		Complexity:         string(ComplexityMedium),
		Priority:           1,
		Role:               "implementer",
		RequireFreshWorker: true,
	}); err != nil {
		return "", fmt.Errorf("enqueue lineage merge task: %w", err)
	}

	bundle, err = k.planStore.Get(activePlanID)
	if err != nil {
		return "", err
	}
	_, completed, failed := summarizeRelevantPlanTasks(k.pm.Tasks(), bundle)
	bundle.Execution.ActiveTaskIDs = []string{runtimeTaskID}
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	bundle.Plan.State = planStateMerging
	bundle.Execution.State = planStateMerging
	bundle.Execution.CompletedAt = nil
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageMergeRequested,
		TaskID:  runtimeTaskID,
		Summary: fmt.Sprintf("Generating squash merge message for %s→%s.", lineage, baseBranch),
	})
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return "", err
	}
	if err := k.planStore.UpdateExecution(activePlanID, bundle.Execution); err != nil {
		return "", err
	}
	k.sendNotify(pool.Notification{Type: "plan_merging", ID: activePlanID, Message: lineage})
	return runtimeTaskID, nil
}

func (k *Kitchen) completeLineageMerge(planID, taskID, commitMsg, commitMessageSource string) error {
	if k == nil {
		return fmt.Errorf("kitchen not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	lineage := strings.TrimSpace(bundle.Plan.Lineage)
	if lineage == "" {
		return fmt.Errorf("plan %s has no lineage", planID)
	}
	baseBranch := k.baseBranchForLineage(lineage)
	gitMgr, err := k.gitManager()
	if err != nil {
		return err
	}
	if err := gitMgr.MergeLineage(lineage, baseBranch, "squash", commitMsg); err != nil {
		return err
	}
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageMergeCompleted,
		TaskID:  taskID,
		Summary: fmt.Sprintf("Merged %s into %s using %s commit message.", lineage, baseBranch, firstNonEmpty(strings.TrimSpace(commitMessageSource), "generated")),
	})
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	if err := k.markPlanMerged(planID); err != nil {
		return err
	}
	k.sendNotify(pool.Notification{Type: "plan_merged", ID: planID, Message: lineage})
	k.activateWaitingPlans()
	return nil
}

func (k *Kitchen) restoreCompletedPlanAfterMergeAttempt(planID, taskID, summary string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen not configured")
	}
	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	bundle.Plan.State = planStateCompleted
	bundle.Execution.State = planStateCompleted
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedAt = &now
	bundle.Execution = appendPlanHistory(bundle.Execution, PlanHistoryEntry{
		Type:    planHistoryLineageMergeFailed,
		TaskID:  taskID,
		Summary: strings.TrimSpace(summary),
	})
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	return k.planStore.UpdateExecution(planID, bundle.Execution)
}

func (k *Kitchen) ReapplyLineage(lineage string) (map[string]any, error) {
	if k == nil {
		return nil, fmt.Errorf("kitchen not configured")
	}

	var activePlanID string
	if k.lineageMgr != nil {
		activePlanID, _ = k.lineageMgr.ActivePlan(lineage)
	}
	if activePlanID != "" {
		if blocked := k.blockingTasksForPlan(activePlanID); len(blocked) > 0 {
			return nil, fmt.Errorf("lineage %s has active tasks: %s — complete or cancel them before reapply", lineage, strings.Join(blocked, ", "))
		}
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		return nil, err
	}

	baseBranch := k.baseBranchForLineage(lineage)
	lineageBranch := lineageBranchName(lineage)
	lineageSHABefore, err := runGit(k.repoPath, "rev-parse", lineageBranch)
	if err != nil {
		return nil, err
	}
	lineageSHABefore = strings.TrimSpace(lineageSHABefore)

	clean, conflicts, err := gitMgr.ReapplyLineageOnBase(lineage, baseBranch)
	if err != nil {
		return nil, err
	}
	if !clean {
		if activePlanID == "" {
			return map[string]any{
				"status":     "conflicts",
				"conflicts":  conflicts,
				"baseBranch": baseBranch,
			}, nil
		}
		bundle, err := k.planStore.Get(activePlanID)
		if err != nil {
			return nil, err
		}
		newTaskID, err := k.enqueueLineageFixMergeTask(activePlanID, bundle, lineage, baseBranch, conflicts)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"status":     "fix-merge-queued",
			"conflicts":  conflicts,
			"baseBranch": baseBranch,
			"newTaskId":  newTaskID,
		}, nil
	}

	lineageSHANow, err := runGit(k.repoPath, "rev-parse", lineageBranch)
	if err != nil {
		return nil, err
	}
	lineageSHANow = strings.TrimSpace(lineageSHANow)
	if lineageSHANow == lineageSHABefore {
		return map[string]any{
			"status":     "up-to-date",
			"baseBranch": baseBranch,
		}, nil
	}

	if activePlanID != "" && k.planStore != nil {
		bundle, err := k.planStore.Get(activePlanID)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		bundle.Plan.Anchor.Commit = lineageSHANow
		bundle.Plan.Anchor.Timestamp = now
		if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
			return nil, err
		}
	}

	resp := map[string]any{
		"status":     "reapplied",
		"baseBranch": baseBranch,
		"newAnchor":  lineageSHANow,
	}
	if activePlanID != "" {
		resp["planId"] = activePlanID
	}
	return resp, nil
}

func executionHasBlockingImplementationReviewFailure(execution ExecutionRecord) bool {
	return execution.ImplReviewRequested && strings.TrimSpace(execution.ImplReviewStatus) == planReviewStatusFailed
}

func (k *Kitchen) PreviewMergeLineage(lineage string) (map[string]any, error) {
	if k == nil {
		return nil, fmt.Errorf("kitchen not configured")
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		return nil, err
	}

	baseBranch := k.baseBranchForLineage(lineage)
	previewHead, err := gitMgr.PreviewMergeLineage(lineage, baseBranch, "squash")
	if err != nil {
		return nil, err
	}
	currentHead, err := runGit(k.repoPath, "rev-parse", baseBranch)
	if err != nil {
		return nil, err
	}

	resp := map[string]any{
		"status":      "preview",
		"baseBranch":  baseBranch,
		"mode":        "squash",
		"currentHead": strings.TrimSpace(currentHead),
		"previewHead": strings.TrimSpace(previewHead),
	}
	if k.lineageMgr != nil {
		if activePlanID, err := k.lineageMgr.ActivePlan(lineage); err == nil && activePlanID != "" {
			resp["planId"] = activePlanID
		}
	}
	return resp, nil
}

func (k *Kitchen) CleanWorktrees() ([]string, error) {
	if k == nil {
		return nil, fmt.Errorf("kitchen not configured")
	}

	activeTasks := k.activeWorktreeTaskSet()
	orphaned, err := findOrphanWorktrees(k.paths.WorktreesDir, activeTasks)
	if err != nil {
		return nil, err
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		return nil, err
	}
	if err := gitMgr.CleanOrphans(activeTasks); err != nil {
		return nil, err
	}
	sort.Strings(orphaned)
	return orphaned, nil
}

func (k *Kitchen) activeWorktreeTaskSet() map[string]bool {
	active := make(map[string]bool)
	if k == nil || k.pm == nil {
		return active
	}
	for _, task := range k.pm.Tasks() {
		if kitchenTaskNeedsWorktree(task.Status) {
			active[task.ID] = true
		}
	}
	return active
}

func kitchenTaskNeedsWorktree(status string) bool {
	switch status {
	case pool.TaskQueued, pool.TaskDispatched:
		return true
	default:
		return false
	}
}

func (k *Kitchen) blockingTasksForPlan(planID string) []string {
	if k == nil || k.pm == nil {
		return nil
	}
	if k.planStore != nil {
		if bundle, err := k.planStore.Get(planID); err == nil {
			active, _, failed := summarizeRelevantPlanTasks(k.pm.Tasks(), bundle)
			blocked := make([]string, 0, len(active)+len(failed))
			for _, taskID := range active {
				if task, ok := k.pm.Task(taskID); ok {
					blocked = append(blocked, fmt.Sprintf("%s=%s", task.ID, task.Status))
					continue
				}
				blocked = append(blocked, fmt.Sprintf("%s=%s", taskID, pool.TaskQueued))
			}
			for _, taskID := range failed {
				if task, ok := k.pm.Task(taskID); ok {
					blocked = append(blocked, fmt.Sprintf("%s=%s", task.ID, task.Status))
					continue
				}
				blocked = append(blocked, fmt.Sprintf("%s=%s", taskID, pool.TaskFailed))
			}
			sort.Strings(blocked)
			return blocked
		}
	}

	var blocked []string
	for _, task := range k.pm.Tasks() {
		if task.PlanID != planID {
			continue
		}
		switch task.Status {
		case pool.TaskCompleted:
			continue
		default:
			blocked = append(blocked, fmt.Sprintf("%s=%s", task.ID, task.Status))
		}
	}
	sort.Strings(blocked)
	return blocked
}

func (k *Kitchen) markPlanMerged(planID string) error {
	if k == nil || k.planStore == nil {
		return fmt.Errorf("kitchen plan store not configured")
	}

	bundle, err := k.planStore.Get(planID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	var completed []string
	var failed []string
	if k.pm != nil {
		for _, task := range k.pm.Tasks() {
			if task.PlanID != planID {
				continue
			}
			switch task.Status {
			case pool.TaskCompleted:
				completed = append(completed, task.ID)
			default:
				failed = append(failed, task.ID)
			}
		}
		sort.Strings(completed)
		sort.Strings(failed)
	}

	bundle.Plan.State = planStateMerged
	bundle.Execution.State = planStateMerged
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.CompletedTaskIDs = completed
	bundle.Execution.FailedTaskIDs = failed
	bundle.Execution.CompletedAt = &now
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		return err
	}
	if err := k.planStore.UpdateExecution(planID, bundle.Execution); err != nil {
		return err
	}
	if k.lineageMgr != nil {
		return k.lineageMgr.ClearActivePlan(bundle.Plan.Lineage, planID)
	}
	return nil
}

func findOrphanWorktrees(worktreeBase string, activeTasks map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read worktree base: %w", err)
	}

	var orphaned []string
	for _, lineageEntry := range entries {
		if !lineageEntry.IsDir() || strings.HasPrefix(lineageEntry.Name(), ".") {
			continue
		}
		lineageDir := filepath.Join(worktreeBase, lineageEntry.Name())
		taskEntries, err := os.ReadDir(lineageDir)
		if err != nil {
			return nil, fmt.Errorf("read lineage worktrees: %w", err)
		}
		for _, taskEntry := range taskEntries {
			if !taskEntry.IsDir() {
				continue
			}
			if activeTasks[taskEntry.Name()] {
				continue
			}
			orphaned = append(orphaned, filepath.Join(lineageDir, taskEntry.Name()))
		}
	}
	return orphaned, nil
}
