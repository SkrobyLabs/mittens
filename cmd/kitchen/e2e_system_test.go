package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestKitchenEndToEndMultiLineageDispatchesIndependentWorktrees(t *testing.T) {
	runtime := newFakeRuntimeDaemon(t, "broker-token", "pool-token")
	defer runtime.Close()

	hostAPI := newRuntimeClient(runtime.socketPath, "broker-token", "pool-token")
	k := newTestKitchenWithHostAPI(t, hostAPI)
	k.cfg.Concurrency.MaxWorkersTotal = 4

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	planA, err := k.SubmitIdea("Implement lineage one task", "lineage-one", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea(planA): %v", err)
	}
	plannerSpawns := waitForSpawnByRole(t, runtime, plannerTaskRole, 1)
	completePlannerSpawn(t, k, runtime, plannerSpawns[0], adapter.PlanArtifact{
		Title:   "Lineage one",
		Summary: "Single implementation task for lineage one.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Lineage one task",
			Prompt:     "Implement the lineage one change.",
			Complexity: string(ComplexityLow),
		}},
	})
	waitFor(t, 2*time.Second, func() bool {
		got, err := k.GetPlan(planA.Plan.PlanID)
		return err == nil && got.Execution.State == planStatePendingApproval
	})

	planB, err := k.SubmitIdea("Implement lineage two task", "lineage-two", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea(planB): %v", err)
	}
	completePlannerSpawn(t, k, runtime, plannerSpawns[0], adapter.PlanArtifact{
		Title:   "Lineage two",
		Summary: "Single implementation task for lineage two.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Lineage two task",
			Prompt:     "Implement the lineage two change.",
			Complexity: string(ComplexityLow),
		}},
	})
	waitFor(t, 2*time.Second, func() bool {
		got, err := k.GetPlan(planB.Plan.PlanID)
		return err == nil && got.Execution.State == planStatePendingApproval
	})

	if err := k.ApprovePlan(planA.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan(planA): %v", err)
	}
	if err := k.ApprovePlan(planB.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan(planB): %v", err)
	}

	implSpawns := waitForSpawnByRole(t, runtime, "implementer", 2)
	if implSpawns[0].WorkspacePath == "" || implSpawns[1].WorkspacePath == "" {
		t.Fatalf("implementation spawns missing workspacePath: %+v", implSpawns)
	}
	if implSpawns[0].WorkspacePath == implSpawns[1].WorkspacePath {
		t.Fatalf("workspace paths should differ, got %q", implSpawns[0].WorkspacePath)
	}
	if !strings.Contains(implSpawns[0].WorkspacePath, "/lineage-") || !strings.Contains(implSpawns[1].WorkspacePath, "/lineage-") {
		t.Fatalf("workspace paths should include lineage dirs, got %q and %q", implSpawns[0].WorkspacePath, implSpawns[1].WorkspacePath)
	}

	task1 := registerAndPollWorkerTask(t, k, implSpawns[0].ID, implSpawns[0].containerID)
	task2 := registerAndPollWorkerTask(t, k, implSpawns[1].ID, implSpawns[1].containerID)

	if task1.PlanID == task2.PlanID {
		t.Fatalf("expected tasks from different plans, got shared planID %q", task1.PlanID)
	}

	waitFor(t, 2*time.Second, func() bool {
		got1, ok1 := k.pm.Task(task1.ID)
		got2, ok2 := k.pm.Task(task2.ID)
		return ok1 && ok2 &&
			got1.Status == pool.TaskDispatched &&
			got2.Status == pool.TaskDispatched &&
			got1.WorkerID != "" &&
			got2.WorkerID != "" &&
			got1.WorkerID != got2.WorkerID
	})
}

func TestKitchenEndToEndSpawnsFreshWorkerPerTask(t *testing.T) {
	runtime := newFakeRuntimeDaemon(t, "broker-token", "pool-token")
	defer runtime.Close()

	hostAPI := newRuntimeClient(runtime.socketPath, "broker-token", "pool-token")
	k := newTestKitchenWithHostAPI(t, hostAPI)
	k.cfg.Concurrency.MaxWorkersTotal = 4

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	bundle, err := k.SubmitIdea("Exercise per-task worker spawning", "per-task-e2e", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	plannerSpawn := waitForSpawnByRole(t, runtime, plannerTaskRole, 1)[0]
	completePlannerSpawn(t, k, runtime, plannerSpawn, adapter.PlanArtifact{
		Title:   "Per-task worker flow",
		Summary: "Two sequential tasks to verify each gets its own worker.",
		Tasks: []adapter.PlanArtifactTask{
			{
				ID:         "t1",
				Title:      "First task",
				Prompt:     "Do the first task.",
				Complexity: string(ComplexityLow),
			},
			{
				ID:           "t2",
				Title:        "Second task",
				Prompt:       "Do the second task.",
				Complexity:   string(ComplexityLow),
				Dependencies: []string{"t1"},
			},
		},
	})
	waitFor(t, 2*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && got.Execution.State == planStatePendingApproval
	})
	if err := k.pm.KillWorker(plannerSpawn.ID); err != nil {
		t.Fatalf("KillWorker(planner): %v", err)
	}

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	// First task dispatches to a freshly spawned implementer.
	firstSpawn := waitForSpawnByRole(t, runtime, "implementer", 1)[0]
	firstTask := registerAndPollWorkerTask(t, k, firstSpawn.ID, firstSpawn.containerID)
	if !strings.HasSuffix(firstTask.ID, "-t1") {
		t.Fatalf("first task ID = %q, want runtime task ending in -t1", firstTask.ID)
	}
	writeWorkerResult(t, k, firstSpawn.ID, "done\n")
	completeWorkerTask(t, k, firstSpawn.ID, firstTask.ID)

	// After completion Kitchen must kill the worker since its
	// container is bind-mounted to a now-discarded worktree.
	waitFor(t, 2*time.Second, func() bool {
		w, ok := k.pm.Worker(firstSpawn.ID)
		return ok && w.Status == pool.WorkerDead
	})

	// Second task must get its own spawn, not reuse the first worker.
	implSpawns := waitForSpawnByRole(t, runtime, "implementer", 2)
	if len(implSpawns) < 2 {
		t.Fatalf("expected two implementer spawns, got %d", len(implSpawns))
	}
	secondSpawn := implSpawns[1]
	if secondSpawn.ID == firstSpawn.ID {
		t.Fatalf("second task reused worker %q instead of fresh spawn", firstSpawn.ID)
	}
	secondTask := registerAndPollWorkerTask(t, k, secondSpawn.ID, secondSpawn.containerID)
	if !strings.HasSuffix(secondTask.ID, "-t2") {
		t.Fatalf("second task ID = %q, want runtime task ending in -t2", secondTask.ID)
	}
}

func TestKitchenEndToEndMergeConflictFailsSecondTask(t *testing.T) {
	runtime := newFakeRuntimeDaemon(t, "broker-token", "pool-token")
	defer runtime.Close()

	hostAPI := newRuntimeClient(runtime.socketPath, "broker-token", "pool-token")
	k := newTestKitchenWithHostAPI(t, hostAPI)
	k.cfg.Concurrency.MaxWorkersTotal = 4
	k.cfg.FailurePolicy["conflict"] = FailurePolicyRule{Action: "retry_merge", Max: 0}

	writeFile(t, filepath.Join(k.repoPath, "shared.txt"), "base\n")
	mustRunGit(t, k.repoPath, "add", "shared.txt")
	mustRunGit(t, k.repoPath, "commit", "-m", "add shared file")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	bundle, err := k.SubmitIdea("Exercise merge conflict handling", "merge-conflict-e2e", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	plannerSpawn := waitForSpawnByRole(t, runtime, plannerTaskRole, 1)[0]
	completePlannerSpawn(t, k, runtime, plannerSpawn, adapter.PlanArtifact{
		Title:   "Conflict flow",
		Summary: "Two independent tasks both modify shared.txt.",
		Tasks: []adapter.PlanArtifactTask{
			{
				ID:         "t1",
				Title:      "First conflicting task",
				Prompt:     "Change shared.txt in one way.",
				Complexity: string(ComplexityLow),
			},
			{
				ID:         "t2",
				Title:      "Second conflicting task",
				Prompt:     "Change shared.txt in a different way.",
				Complexity: string(ComplexityLow),
			},
		},
	})
	waitFor(t, 2*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && got.Execution.State == planStatePendingApproval
	})

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	implSpawns := waitForSpawnByRole(t, runtime, "implementer", 2)
	task1 := registerAndPollWorkerTask(t, k, implSpawns[0].ID, implSpawns[0].containerID)
	task2 := registerAndPollWorkerTask(t, k, implSpawns[1].ID, implSpawns[1].containerID)

	writeFile(t, filepath.Join(implSpawns[0].WorkspacePath, "shared.txt"), "worker one\n")
	mustRunGit(t, implSpawns[0].WorkspacePath, "add", "shared.txt")
	mustRunGit(t, implSpawns[0].WorkspacePath, "commit", "-m", "task one change")
	writeWorkerResult(t, k, implSpawns[0].ID, "done one\n")
	completeWorkerTask(t, k, implSpawns[0].ID, task1.ID)

	waitFor(t, 2*time.Second, func() bool {
		content, err := runGit(k.repoPath, "show", lineageBranchName(bundle.Plan.Lineage)+":shared.txt")
		return err == nil && strings.TrimSpace(content) == "worker one"
	})

	writeFile(t, filepath.Join(implSpawns[1].WorkspacePath, "shared.txt"), "worker two\n")
	mustRunGit(t, implSpawns[1].WorkspacePath, "add", "shared.txt")
	mustRunGit(t, implSpawns[1].WorkspacePath, "commit", "-m", "task two change")
	writeWorkerResult(t, k, implSpawns[1].ID, "done two\n")
	completeWorkerTask(t, k, implSpawns[1].ID, task2.ID)

	waitFor(t, 3*time.Second, func() bool {
		got, ok := k.pm.Task(task2.ID)
		return ok && got.Status == pool.TaskFailed
	})

	got, ok := k.pm.Task(task2.ID)
	if !ok {
		t.Fatalf("task %q not found", task2.ID)
	}
	if got.Result == nil || !strings.Contains(strings.ToLower(got.Result.Error), "merge conflict") {
		t.Fatalf("task result = %+v, want merge conflict error", got.Result)
	}
	waitFor(t, 3*time.Second, func() bool {
		_, err := os.Stat(implSpawns[1].WorkspacePath)
		return os.IsNotExist(err)
	})
	if _, err := os.Stat(implSpawns[1].WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("expected conflicting worktree to be removed, stat err = %v", err)
	}

	plan, err := k.GetPlan(bundle.Plan.PlanID)
	waitFor(t, 3*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && len(got.Execution.FailedTaskIDs) == 1 && got.Execution.FailedTaskIDs[0] == task2.ID
	})
	plan, err = k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if plan.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", plan.Execution.State, planStateActive)
	}
	if len(plan.Execution.FailedTaskIDs) != 1 || plan.Execution.FailedTaskIDs[0] != task2.ID {
		t.Fatalf("failed task IDs = %+v, want [%s]", plan.Execution.FailedTaskIDs, task2.ID)
	}
}

func TestKitchenEndToEndMergeConflictRetriesFromUpdatedLineageHead(t *testing.T) {
	runtime := newFakeRuntimeDaemon(t, "broker-token", "pool-token")
	defer runtime.Close()

	hostAPI := newRuntimeClient(runtime.socketPath, "broker-token", "pool-token")
	k := newTestKitchenWithHostAPI(t, hostAPI)
	k.cfg.Concurrency.MaxWorkersTotal = 4
	k.cfg.FailurePolicy["conflict"] = FailurePolicyRule{Action: "retry_merge", Max: 1}

	writeFile(t, filepath.Join(k.repoPath, "shared.txt"), "base\n")
	mustRunGit(t, k.repoPath, "add", "shared.txt")
	mustRunGit(t, k.repoPath, "commit", "-m", "add shared file")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	bundle, err := k.SubmitIdea("Exercise merge conflict retry", "merge-conflict-retry-e2e", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	plannerSpawn := waitForSpawnByRole(t, runtime, plannerTaskRole, 1)[0]
	completePlannerSpawn(t, k, runtime, plannerSpawn, adapter.PlanArtifact{
		Title:   "Conflict retry flow",
		Summary: "Two independent tasks both modify shared.txt.",
		Tasks: []adapter.PlanArtifactTask{
			{
				ID:         "t1",
				Title:      "First conflicting task",
				Prompt:     "Change shared.txt in one way.",
				Complexity: string(ComplexityLow),
			},
			{
				ID:         "t2",
				Title:      "Second conflicting task",
				Prompt:     "Change shared.txt in a different way.",
				Complexity: string(ComplexityLow),
			},
		},
	})
	waitFor(t, 2*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && got.Execution.State == planStatePendingApproval
	})

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	implSpawns := waitForSpawnByRole(t, runtime, "implementer", 2)
	task1 := registerAndPollWorkerTask(t, k, implSpawns[0].ID, implSpawns[0].containerID)
	task2 := registerAndPollWorkerTask(t, k, implSpawns[1].ID, implSpawns[1].containerID)

	writeFile(t, filepath.Join(implSpawns[0].WorkspacePath, "shared.txt"), "worker one\n")
	mustRunGit(t, implSpawns[0].WorkspacePath, "add", "shared.txt")
	mustRunGit(t, implSpawns[0].WorkspacePath, "commit", "-m", "task one change")
	writeWorkerResult(t, k, implSpawns[0].ID, "done one\n")
	completeWorkerTask(t, k, implSpawns[0].ID, task1.ID)

	waitFor(t, 2*time.Second, func() bool {
		content, err := runGit(k.repoPath, "show", lineageBranchName(bundle.Plan.Lineage)+":shared.txt")
		return err == nil && strings.TrimSpace(content) == "worker one"
	})

	writeFile(t, filepath.Join(implSpawns[1].WorkspacePath, "shared.txt"), "worker two\n")
	mustRunGit(t, implSpawns[1].WorkspacePath, "add", "shared.txt")
	mustRunGit(t, implSpawns[1].WorkspacePath, "commit", "-m", "task two change")
	writeWorkerResult(t, k, implSpawns[1].ID, "done two\n")
	completeWorkerTask(t, k, implSpawns[1].ID, task2.ID)

	waitFor(t, 3*time.Second, func() bool {
		got, ok := k.pm.Task(task2.ID)
		return ok && got.Status == pool.TaskQueued && got.RetryCount == 1 && got.RequireFreshWorker
	})

	implSpawns = waitForSpawnByRole(t, runtime, "implementer", 3)
	retrySpawn := implSpawns[2]
	retryTask := registerAndPollWorkerTask(t, k, retrySpawn.ID, retrySpawn.containerID)
	if retryTask.ID != task2.ID {
		t.Fatalf("retry task ID = %q, want %q", retryTask.ID, task2.ID)
	}
	deadline := time.Now().Add(3 * time.Second)
	var lastContent string
	var lastErr error
	for time.Now().Before(deadline) {
		lastContent, lastErr = runGit(retrySpawn.WorkspacePath, "show", "HEAD:shared.txt")
		if lastErr == nil && strings.TrimSpace(lastContent) == "worker one" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("git show retried shared.txt: %v", lastErr)
	}
	if strings.TrimSpace(lastContent) != "worker one" {
		t.Fatalf("retried worktree shared.txt = %q, want worker one", lastContent)
	}

	writeFile(t, filepath.Join(retrySpawn.WorkspacePath, "shared.txt"), "resolved\n")
	mustRunGit(t, retrySpawn.WorkspacePath, "add", "shared.txt")
	mustRunGit(t, retrySpawn.WorkspacePath, "commit", "-m", "task two resolved")
	writeWorkerResult(t, k, retrySpawn.ID, "done resolved\n")
	completeWorkerTask(t, k, retrySpawn.ID, retryTask.ID)

	waitFor(t, 3*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && got.Execution.State == planStateCompleted
	})
}

func TestKitchenEndToEndTimeoutUsesSchedulerClockSeam(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Exercise timeout handling", "timeout-e2e", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title:   "Timeout flow",
		Summary: "Single task with timeout budget.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Timed task",
			Prompt:     "Do the timed task.",
			Complexity: string(ComplexityLow),
		}},
	})

	stored, err := k.planStore.Get(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("Get plan store bundle: %v", err)
	}
	stored.Plan.Tasks[0].TimeoutMinutes = 1
	if err := k.planStore.UpdatePlan(stored.Plan); err != nil {
		t.Fatalf("UpdatePlan timeout: %v", err)
	}

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-timeout", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-timeout", "container-w-timeout"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-timeout"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	pmTask, ok := k.pm.Task(taskID)
	if !ok || pmTask.DispatchedAt == nil {
		t.Fatalf("task = %+v, want dispatched task with timestamp", pmTask)
	}
	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.nowFunc = func() time.Time {
		return pmTask.DispatchedAt.Add(2 * time.Minute)
	}
	if err := s.enforceTaskTimeouts(); err != nil {
		t.Fatalf("enforceTaskTimeouts: %v", err)
	}
	if err := s.onTaskFailed(taskID, FailureTimeout); err != nil {
		t.Fatalf("onTaskFailed(timeout): %v", err)
	}

	got, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if got.Result == nil || !strings.Contains(got.Result.Error, "time budget") {
		t.Fatalf("task result = %+v, want timeout error", got.Result)
	}

	plan, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if len(plan.Execution.FailedTaskIDs) != 1 || plan.Execution.FailedTaskIDs[0] != taskID {
		t.Fatalf("failed task IDs = %+v, want [%s]", plan.Execution.FailedTaskIDs, taskID)
	}
}

func TestKitchenEndToEndPlanningFailureCanBeReplannedAndRecovered(t *testing.T) {
	k := newTestKitchen(t)

	original, err := k.SubmitIdea("Review this branch and come up with a squash commit message", "review-branch", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	original.Plan.State = planStatePlanningFailed
	original.Execution.State = planStatePlanningFailed
	original.Execution.ActiveTaskIDs = nil
	original.Execution.FailedTaskIDs = []string{councilTaskID(original.Plan.PlanID, 1)}
	if err := k.planStore.UpdatePlan(original.Plan); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if err := k.planStore.UpdateExecution(original.Plan.PlanID, original.Execution); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}
	if err := k.pm.CancelTask(councilTaskID(original.Plan.PlanID, 1)); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	newPlanID, err := k.Replan(original.Plan.PlanID, "Try again with a fresh planning pass")
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}
	if newPlanID == original.Plan.PlanID {
		t.Fatalf("replan reused original plan ID %q", newPlanID)
	}

	completePlanningTask(t, k, newPlanID, adapter.PlanArtifact{
		Title:   "Recovered plan",
		Summary: "Recovered after planner artifact failure.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Recovered task",
			Prompt:     "Write the recovered change.",
			Complexity: string(ComplexityLow),
		}},
	})

	got, err := k.GetPlan(newPlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStatePendingApproval {
		t.Fatalf("execution state = %q, want %q", got.Execution.State, planStatePendingApproval)
	}
}

func TestKitchenEndToEndRuntimeMuxRoutesPlannerAndImplementationToDifferentProviders(t *testing.T) {
	claudeRuntime := newFakeRuntimeDaemon(t, "broker-claude", "pool-claude")
	defer claudeRuntime.Close()
	codexRuntime := newFakeRuntimeDaemon(t, "broker-codex", "pool-codex")
	defer codexRuntime.Close()

	hostAPI := newRuntimeMux(map[string]pool.RuntimeAPI{
		"claude": newRuntimeClient(claudeRuntime.socketPath, "broker-claude", "pool-claude"),
		"codex":  newRuntimeClient(codexRuntime.socketPath, "broker-codex", "pool-codex"),
	})
	k := newTestKitchenWithHostAPI(t, hostAPI)
	cfg := DefaultKitchenConfig()
	cfg.Routing[ComplexityLow] = RoutingRule{
		Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
	}
	cfg.Routing[ComplexityMedium] = RoutingRule{
		Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
	}
	k.cfg = cfg
	k.router = NewComplexityRouter(cfg, k.health, PoolKey{Provider: "claude"}, PoolKey{Provider: "codex"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	bundle, err := k.SubmitIdea("Route planning to codex and implementation to claude", "provider-routing", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	codexPlannerSpawn := waitForSpawnByRole(t, codexRuntime, plannerTaskRole, 1)[0]
	if got := claudeRuntime.SpawnCount(); got != 0 {
		t.Fatalf("claude runtime spawn count = %d, want 0 before implementation", got)
	}
	completePlannerSpawn(t, k, codexRuntime, codexPlannerSpawn, adapter.PlanArtifact{
		Title:   "Provider-routed plan",
		Summary: "Planner runs on codex, implementation on claude.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Implement change",
			Prompt:     "Implement the routed change.",
			Complexity: string(ComplexityLow),
		}},
	})

	waitFor(t, 3*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && got.Execution.State == planStatePendingApproval
	})
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	claudeImplSpawn := waitForSpawnByRole(t, claudeRuntime, "implementer", 1)[0]
	if got := codexRuntime.SpawnCount(); got != 2 {
		t.Fatalf("codex runtime spawn count = %d, want two planner council spawns", got)
	}
	if claudeImplSpawn.Provider != "anthropic" {
		t.Fatalf("implementation spawn provider = %q, want anthropic", claudeImplSpawn.Provider)
	}
	task := registerAndPollWorkerTask(t, k, claudeImplSpawn.ID, claudeImplSpawn.containerID)
	if task.PlanID != bundle.Plan.PlanID {
		t.Fatalf("implementation task planID = %q, want %q", task.PlanID, bundle.Plan.PlanID)
	}
}
