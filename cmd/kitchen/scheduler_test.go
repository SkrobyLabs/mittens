package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type schedulerHostAPI struct {
	spawnSpecs []pool.WorkerSpec
	containers []pool.ContainerInfo
}

func (h *schedulerHostAPI) SpawnWorker(_ context.Context, spec pool.WorkerSpec) (string, string, error) {
	h.spawnSpecs = append(h.spawnSpecs, spec)
	return "worker-" + spec.ID, "container-" + spec.ID, nil
}

func (h *schedulerHostAPI) KillWorker(_ context.Context, _ string) error { return nil }

func (h *schedulerHostAPI) ListContainers(_ context.Context, _ string) ([]pool.ContainerInfo, error) {
	return h.containers, nil
}

func (h *schedulerHostAPI) RecycleWorker(_ context.Context, _ string) error { return nil }

func (h *schedulerHostAPI) GetWorkerActivity(_ context.Context, _ string) (*pool.WorkerActivity, error) {
	return nil, nil
}

func (h *schedulerHostAPI) GetWorkerTranscript(_ context.Context, _ string) ([]pool.WorkerActivityRecord, error) {
	return nil, nil
}

func (h *schedulerHostAPI) SubscribeEvents(_ context.Context) (<-chan pool.RuntimeEvent, error) {
	ch := make(chan pool.RuntimeEvent)
	close(ch)
	return ch, nil
}

func (h *schedulerHostAPI) SubmitAssignment(_ context.Context, _ string, _ pool.Assignment) error {
	return nil
}

func TestSchedulerScheduleSpawnsAndDispatchesTask(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_sched",
			Lineage: "parser-errors",
			Title:   "Parser error handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	wal, pm := newSchedulerPoolManager(t, &schedulerHostAPI{}, filepath.Join(project.PoolsDir, "sched"), "kitchen-test")
	defer wal.Close()
	host := &schedulerHostAPI{}
	pm = newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched2"), "kitchen-test")

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	router := NewComplexityRouter(DefaultKitchenConfig(), nil)
	s := NewScheduler(pm, host, router, gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1", len(host.spawnSpecs))
	}
	spec := host.spawnSpecs[0]
	if spec.Provider != "anthropic" || spec.Model != "sonnet" {
		t.Fatalf("spawn spec = %+v, want anthropic/sonnet", spec)
	}
	if spec.WorkspacePath == "" {
		t.Fatal("expected workspacePath to be set for planned task")
	}
	if _, err := os.Stat(filepath.Join(spec.WorkspacePath, ".git")); err != nil {
		t.Fatalf("worktree missing .git: %v", err)
	}

	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch): %v", err)
	}

	task, ok := pm.Task(taskID)
	if !ok || task.Status != pool.TaskDispatched || task.WorkerID != "w-1" {
		t.Fatalf("task = %+v, want dispatched to w-1", task)
	}
}

func TestSchedulerOnTaskCompletedMergesAndCleansWorktree(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_complete",
			Lineage: "parser-errors",
			Title:   "Parser error handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.failurePolicy["conflict"] = FailurePolicyRule{Action: "retry_merge", Max: 0}

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	wt := host.spawnSpecs[0].WorkspacePath

	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch): %v", err)
	}

	writeFile(t, filepath.Join(wt, "merged.txt"), "hello\n")
	mustRunGit(t, wt, "add", "merged.txt")
	mustRunGit(t, wt, "commit", "-m", "task change")

	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.onTaskCompleted(taskID); err != nil {
		t.Fatalf("onTaskCompleted: %v", err)
	}

	out, err := runGit(repo, "show", "kitchen/parser-errors/lineage:merged.txt")
	if err != nil {
		t.Fatalf("show merged file: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("merged file contents = %q, want hello", out)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed, stat err = %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStateCompleted {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateCompleted)
	}
	if bundle.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateCompleted)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 0 {
		t.Fatalf("active task IDs = %+v, want empty", bundle.Execution.ActiveTaskIDs)
	}
	if len(bundle.Execution.CompletedTaskIDs) != 1 || bundle.Execution.CompletedTaskIDs[0] != taskID {
		t.Fatalf("completed task IDs = %+v, want [%s]", bundle.Execution.CompletedTaskIDs, taskID)
	}
	if bundle.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set")
	}
}

func TestSchedulerDoesNotDispatchTaskToIdleWorkerFromWrongProvider(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_provider_match",
			Lineage: "parser-errors",
			Title:   "Provider matching",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched"), "kitchen-test")
	claudeWorker, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-claude", Role: "implementer", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("SpawnWorker claude: %v", err)
	}
	if err := pm.RegisterWorker(claudeWorker.ID, "container-w-claude"); err != nil {
		t.Fatalf("RegisterWorker claude: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
			},
		},
		Concurrency: DefaultKitchenConfig().Concurrency,
	}, nil,
		PoolKey{Provider: "claude"},
		PoolKey{Provider: "codex"},
	)
	s := NewScheduler(pm, host, router, gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-provider",
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %s missing", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued until matching provider worker exists", task.Status)
	}
	claudeState, ok := pm.Worker("w-claude")
	if !ok {
		t.Fatal("worker w-claude missing")
	}
	if claudeState.CurrentTaskID != "" {
		t.Fatalf("w-claude current task = %q, want none", claudeState.CurrentTaskID)
	}
	if len(host.spawnSpecs) < 2 {
		t.Fatalf("spawn specs = %d, want existing worker plus a new provider-matched worker", len(host.spawnSpecs))
	}
	last := host.spawnSpecs[len(host.spawnSpecs)-1]
	if last.Provider != "openai" {
		t.Fatalf("spawned provider = %q, want openai", last.Provider)
	}
}

func TestSchedulerDoesNotDispatchImplementationTaskToPlannerWorker(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_role_match",
			Lineage: "parser-errors",
			Title:   "Parser error handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-role"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "planner-1", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker planner: %v", err)
	}
	if err := pm.RegisterWorker("planner-1", "container-planner-1"); err != nil {
		t.Fatalf("RegisterWorker planner: %v", err)
	}
	host.spawnSpecs = nil

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-impl",
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
	if task.WorkerID != "" {
		t.Fatalf("task workerID = %q, want empty", task.WorkerID)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1 new worker spawn", len(host.spawnSpecs))
	}
	if host.spawnSpecs[0].Role != "implementer" {
		t.Fatalf("spawned role = %q, want implementer", host.spawnSpecs[0].Role)
	}
}

func TestSchedulerRespectsMaxWorkersPerLineage(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_lineage_cap",
			Lineage: "parser-errors",
			Title:   "Parser lineage cap",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks: []PlanTask{
				{ID: "t1", Title: "task 1", Prompt: "task 1", Complexity: ComplexityMedium},
				{ID: "t2", Title: "task 2", Prompt: "task 2", Complexity: ComplexityMedium},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-lineage-cap"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	cfg := DefaultKitchenConfig().Concurrency
	cfg.MaxWorkersTotal = 4
	cfg.MaxWorkersPerLineage = 1
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, cfg, "kitchen-test")

	task1ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "task 1",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t1: %v", err)
	}
	task2ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t2"),
		PlanID:     planID,
		Prompt:     "task 2",
		Complexity: string(ComplexityMedium),
		Priority:   2,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t2: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1 under lineage cap", len(host.spawnSpecs))
	}

	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker w-1: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch first): %v", err)
	}

	firstTask, ok := pm.Task(task1ID)
	if !ok || firstTask.Status != pool.TaskDispatched || firstTask.WorkerID != "w-1" {
		t.Fatalf("first task = %+v, want dispatched to w-1", firstTask)
	}

	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-2", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker w-2: %v", err)
	}
	if err := pm.RegisterWorker("w-2", "container-w-2"); err != nil {
		t.Fatalf("RegisterWorker w-2: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(cap check): %v", err)
	}

	secondTask, ok := pm.Task(task2ID)
	if !ok {
		t.Fatalf("task %q not found", task2ID)
	}
	if secondTask.Status != pool.TaskQueued {
		t.Fatalf("second task status = %q, want queued while lineage cap is reached", secondTask.Status)
	}
	worker2, ok := pm.Worker("w-2")
	if !ok {
		t.Fatal("worker w-2 not found")
	}
	if worker2.Status != pool.WorkerIdle {
		t.Fatalf("worker2 status = %q, want idle", worker2.Status)
	}

	if err := pm.FailTask("w-1", task1ID, "done"); err != nil {
		t.Fatalf("FailTask t1: %v", err)
	}
	if err := s.onTaskFailed(task1ID, FailureEnvironment); err != nil {
		t.Fatalf("onTaskFailed(t1): %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(after free capacity): %v", err)
	}

	secondTask, ok = pm.Task(task2ID)
	if !ok || secondTask.Status != pool.TaskDispatched {
		t.Fatalf("second task = %+v, want dispatched once lineage capacity frees", secondTask)
	}
	if secondTask.WorkerID != "w-1" && secondTask.WorkerID != "w-2" {
		t.Fatalf("second task worker = %q, want one of the idle implementers", secondTask.WorkerID)
	}
}

func TestSchedulerOnTaskCompletedEmitsPlanCompletedNotification(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_notify",
			Lineage: "parser-errors",
			Title:   "Parser error handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-notify"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	notifications := make(chan pool.Notification, 1)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.notify = func(n pool.Notification) { notifications <- n }

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	wt := host.spawnSpecs[0].WorkspacePath

	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch): %v", err)
	}

	writeFile(t, filepath.Join(wt, "merged.txt"), "hello\n")
	mustRunGit(t, wt, "add", "merged.txt")
	mustRunGit(t, wt, "commit", "-m", "task change")

	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.onTaskCompleted(taskID); err != nil {
		t.Fatalf("onTaskCompleted: %v", err)
	}

	select {
	case n := <-notifications:
		if n.Type != "plan_completed" {
			t.Fatalf("notification type = %q, want plan_completed", n.Type)
		}
		if n.ID != planID {
			t.Fatalf("notification id = %q, want %q", n.ID, planID)
		}
	default:
		t.Fatal("expected plan_completed notification")
	}
}

func TestSchedulerOnTaskFailedUpdatesExecutionProgress(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_failed",
			Lineage: "parser-errors",
			Title:   "Parser error handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks: []PlanTask{
				{ID: "t1", Title: "task 1", Prompt: "task 1", Complexity: ComplexityMedium},
				{ID: "t2", Title: "task 2", Prompt: "task 2", Complexity: ComplexityMedium},
			},
			State: planStateActive,
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{"t-1", "t-2"},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-failed"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	task1ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		PlanID:     planID,
		Prompt:     "implement change 1",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t1: %v", err)
	}
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-2",
		PlanID:     planID,
		Prompt:     "implement change 2",
		Complexity: string(ComplexityMedium),
		Priority:   2,
		Role:       "implementer",
	}); err != nil {
		t.Fatalf("EnqueueTask t2: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch): %v", err)
	}
	if err := pm.FailTask("w-1", task1ID, "boom"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	if err := s.onTaskFailed(task1ID, FailureUnknown); err != nil {
		t.Fatalf("onTaskFailed: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 1 || bundle.Execution.ActiveTaskIDs[0] != "t-2" {
		t.Fatalf("active task IDs = %+v, want [t-2]", bundle.Execution.ActiveTaskIDs)
	}
	if len(bundle.Execution.FailedTaskIDs) != 1 || bundle.Execution.FailedTaskIDs[0] != task1ID {
		t.Fatalf("failed task IDs = %+v, want [%s]", bundle.Execution.FailedTaskIDs, task1ID)
	}
	if bundle.Execution.CompletedAt != nil {
		t.Fatalf("completedAt = %v, want nil", bundle.Execution.CompletedAt)
	}
}

func TestSchedulerOnTaskCompletedMergeConflictFailsTaskAndCleansWorktree(t *testing.T) {
	repo := initGitRepo(t)
	writeFile(t, filepath.Join(repo, "shared.txt"), "base\n")
	mustRunGit(t, repo, "add", "shared.txt")
	mustRunGit(t, repo, "commit", "-m", "add shared file")

	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	planID := "plan_conflict"
	store := NewPlanStore(project.PlansDir)
	_, err = store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "parser-errors",
			Title:   "Parser conflict handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks: []PlanTask{
				{ID: "t1", Title: "task 1", Prompt: "task 1", Complexity: ComplexityMedium},
				{ID: "t2", Title: "task 2", Prompt: "task 2", Complexity: ComplexityMedium},
			},
			State: planStateActive,
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{planTaskRuntimeID(planID, "t1"), planTaskRuntimeID(planID, "t2")},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-conflict"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.failurePolicy["conflict"] = FailurePolicyRule{Action: "retry_merge", Max: 0}

	task1ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "task 1",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t1: %v", err)
	}
	task2ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t2"),
		PlanID:     planID,
		Prompt:     "task 2",
		Complexity: string(ComplexityMedium),
		Priority:   2,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t2: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	if len(host.spawnSpecs) != 2 {
		t.Fatalf("spawn specs = %d, want 2", len(host.spawnSpecs))
	}
	wt1 := host.spawnSpecs[0].WorkspacePath
	wt2 := host.spawnSpecs[1].WorkspacePath

	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker w-1: %v", err)
	}
	if err := pm.RegisterWorker("w-2", "container-w-2"); err != nil {
		t.Fatalf("RegisterWorker w-2: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch): %v", err)
	}

	writeFile(t, filepath.Join(wt1, "shared.txt"), "worker one\n")
	mustRunGit(t, wt1, "add", "shared.txt")
	mustRunGit(t, wt1, "commit", "-m", "task one change")
	if err := pm.CompleteTask("w-1", task1ID); err != nil {
		t.Fatalf("CompleteTask t1: %v", err)
	}
	if err := s.onTaskCompleted(task1ID); err != nil {
		t.Fatalf("onTaskCompleted(t1): %v", err)
	}

	writeFile(t, filepath.Join(wt2, "shared.txt"), "worker two\n")
	mustRunGit(t, wt2, "add", "shared.txt")
	mustRunGit(t, wt2, "commit", "-m", "task two change")
	if err := pm.CompleteTask("w-2", task2ID); err != nil {
		t.Fatalf("CompleteTask t2: %v", err)
	}
	if err := s.onTaskCompleted(task2ID); err != nil {
		t.Fatalf("onTaskCompleted(t2): %v", err)
	}

	task2, ok := pm.Task(task2ID)
	if !ok {
		t.Fatalf("task %q not found", task2ID)
	}
	if task2.Status != pool.TaskFailed {
		t.Fatalf("task2 status = %q, want %q", task2.Status, pool.TaskFailed)
	}
	if task2.Result == nil || !strings.Contains(strings.ToLower(task2.Result.Error), "merge conflict") {
		t.Fatalf("task2 result = %+v, want merge conflict error", task2.Result)
	}
	if _, err := os.Stat(wt2); !os.IsNotExist(err) {
		t.Fatalf("expected conflicting worktree to be removed, stat err = %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if len(bundle.Execution.CompletedTaskIDs) != 1 || bundle.Execution.CompletedTaskIDs[0] != task1ID {
		t.Fatalf("completed task IDs = %+v, want [%s]", bundle.Execution.CompletedTaskIDs, task1ID)
	}
	if len(bundle.Execution.FailedTaskIDs) != 1 || bundle.Execution.FailedTaskIDs[0] != task2ID {
		t.Fatalf("failed task IDs = %+v, want [%s]", bundle.Execution.FailedTaskIDs, task2ID)
	}
}

func TestSchedulerEnforceTaskTimeoutsFailsTimedOutTask(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_timeout",
			Lineage: "parser-errors",
			Title:   "Parser timeout handling",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks: []PlanTask{{
				ID:             "t1",
				Title:          "task 1",
				Prompt:         "task 1",
				Complexity:     ComplexityMedium,
				TimeoutMinutes: 1,
			}},
			State: planStateActive,
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{planTaskRuntimeID("plan_timeout", "t1")},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-timeout"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:             planTaskRuntimeID(planID, "t1"),
		PlanID:         planID,
		Prompt:         "implement timeout-prone change",
		Complexity:     string(ComplexityMedium),
		Priority:       1,
		TimeoutMinutes: 1,
		Role:           "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	task, ok := pm.Task(taskID)
	if !ok || task.DispatchedAt == nil {
		t.Fatalf("task = %+v, want dispatched task with timestamp", task)
	}
	s.nowFunc = func() time.Time {
		return task.DispatchedAt.Add(2 * time.Minute)
	}

	if err := s.enforceTaskTimeouts(); err != nil {
		t.Fatalf("enforceTaskTimeouts: %v", err)
	}

	task, ok = pm.Task(taskID)
	if !ok {
		t.Fatalf("task %s missing", taskID)
	}
	if task.Status != pool.TaskFailed {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskFailed)
	}
	if task.Result == nil || !strings.Contains(task.Result.Error, "time budget") {
		t.Fatalf("task result = %+v, want timeout failure message", task.Result)
	}
}

func TestSchedulerOnTaskFailedConflictRevivesTaskForFreshRetry(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	planID := "plan_conflict_retry"
	store := NewPlanStore(project.PlansDir)
	_, err = store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "parser-errors",
			Title:   "Parser conflict retry",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks: []PlanTask{
				{ID: "t2", Title: "task 2", Prompt: "task 2", Complexity: ComplexityMedium},
			},
			State: planStateActive,
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{planTaskRuntimeID(planID, "t2")},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-conflict-retry"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	host.spawnSpecs = nil

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t2"),
		PlanID:     planID,
		Prompt:     "task 2",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := pm.FailTask("w-1", taskID, "merge conflicts: shared.txt"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	if err := s.onTaskFailed(taskID, FailureConflict); err != nil {
		t.Fatalf("onTaskFailed: %v", err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("status = %q, want %q", task.Status, pool.TaskQueued)
	}
	if task.RetryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", task.RetryCount)
	}
	if !task.RequireFreshWorker {
		t.Fatal("expected retried task to require a fresh worker")
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1 fresh retry worker", len(host.spawnSpecs))
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if len(bundle.Execution.FailedTaskIDs) != 0 {
		t.Fatalf("failed task IDs = %+v, want empty after revive", bundle.Execution.FailedTaskIDs)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 1 || bundle.Execution.ActiveTaskIDs[0] != taskID {
		t.Fatalf("active task IDs = %+v, want [%s]", bundle.Execution.ActiveTaskIDs, taskID)
	}
}

func TestSchedulerEnforceTaskTimeoutsSkipsPlannerTask(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_planner_timeout",
			Lineage: "parser-errors",
			Title:   "Planner timeout exemption",
			State:   planStatePlanning,
		},
		Execution: ExecutionRecord{
			State:         planStatePlanning,
			ActiveTaskIDs: []string{planTaskRuntimeID("plan_planner_timeout", plannerTaskID)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-planner-timeout"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, plannerTaskID),
		PlanID:     planID,
		Prompt:     "plan the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "planner-1", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("planner-1", "container-planner-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "planner-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := pool.Apply(pm, pool.Event{
		Timestamp: time.Now().Add(-2 * time.Minute),
		Type:      pool.EventTaskDispatched,
		TaskID:    taskID,
		WorkerID:  "planner-1",
	}); err != nil {
		t.Fatalf("Apply old dispatched timestamp: %v", err)
	}

	if err := s.enforceTaskTimeouts(); err != nil {
		t.Fatalf("enforceTaskTimeouts: %v", err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %s missing", taskID)
	}
	if task.Status != pool.TaskDispatched {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskDispatched)
	}
	if task.Result != nil {
		t.Fatalf("task result = %+v, want nil", task.Result)
	}
}

func newKitchenTestPaths(t *testing.T) KitchenPaths {
	t.Helper()
	root := t.TempDir()
	return KitchenPaths{
		HomeDir:      root,
		ConfigPath:   filepath.Join(root, "config.yaml"),
		StateDir:     filepath.Join(root, "state"),
		ProjectsDir:  filepath.Join(root, "projects"),
		WorktreesDir: filepath.Join(root, "worktrees"),
	}
}

func newSchedulerPoolManager(t *testing.T, host pool.HostAPI, stateDir, sessionID string) (*pool.WAL, *pool.PoolManager) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	wal, err := pool.OpenWAL(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	pm := pool.NewPoolManager(pool.PoolConfig{
		SessionID:  sessionID,
		MaxWorkers: 16,
		StateDir:   stateDir,
	}, wal, host)
	return wal, pm
}

func newSchedulerPoolManagerWithHost(t *testing.T, host pool.HostAPI, stateDir, sessionID string) *pool.PoolManager {
	t.Helper()
	wal, pm := newSchedulerPoolManager(t, host, stateDir, sessionID)
	t.Cleanup(func() { _ = wal.Close() })
	return pm
}
