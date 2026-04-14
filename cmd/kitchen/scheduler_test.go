package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type schedulerHostAPI struct {
	spawnSpecs    []pool.WorkerSpec
	containers    []pool.ContainerInfo
	killedWorkers []string
	listErr       error
}

func (h *schedulerHostAPI) SpawnWorker(_ context.Context, spec pool.WorkerSpec) (string, string, error) {
	h.spawnSpecs = append(h.spawnSpecs, spec)
	return "worker-" + spec.ID, "container-" + spec.ID, nil
}

func (h *schedulerHostAPI) KillWorker(_ context.Context, workerID string) error {
	h.killedWorkers = append(h.killedWorkers, workerID)
	return nil
}

func reviewCouncilTestArtifact(t *testing.T, seat string, turn int, verdict, stance string, adopted bool, findings []adapter.ReviewFinding) string {
	t.Helper()
	body, err := json.Marshal(adapter.ReviewCouncilTurnArtifact{
		Seat:                seat,
		Turn:                turn,
		Stance:              stance,
		Verdict:             verdict,
		AdoptedPriorVerdict: adopted,
		Findings:            findings,
		Summary:             "review council artifact",
	})
	if err != nil {
		t.Fatalf("marshal review council artifact: %v", err)
	}
	return "<review_council_turn>" + string(body) + "</review_council_turn>"
}

func (h *schedulerHostAPI) ListContainers(_ context.Context, _ string) ([]pool.ContainerInfo, error) {
	return h.containers, h.listErr
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

func TestSchedulerWorkerSpecForTaskUsesRoleAwareRouting(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		ProviderModels: DefaultKitchenConfig().ProviderModels,
		RoleProviders: map[string]ProviderPolicy{
			defaultRoutingRole: {Prefer: []string{"anthropic"}},
			"reviewer":         {Prefer: []string{"openai"}},
		},
	}, nil)

	s := &Scheduler{router: router}
	spec, err := s.workerSpecForTask(pool.Task{
		ID:         "review-task",
		Role:       "reviewer",
		Complexity: string(ComplexityMedium),
	})
	if err != nil {
		t.Fatalf("workerSpecForTask: %v", err)
	}
	if spec.Provider != "openai" || spec.Model != "gpt-5.4" {
		t.Fatalf("worker spec = %+v, want openai/gpt-5.4", spec)
	}
}

func TestSchedulerWorkerSpecForTaskUsesCouncilSeatRouting(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		ProviderModels: DefaultKitchenConfig().ProviderModels,
		RoleProviders: map[string]ProviderPolicy{
			defaultRoutingRole: {Prefer: []string{"anthropic"}},
		},
		CouncilSeatProviders: map[string]ProviderPolicy{
			"B": {Prefer: []string{"openai"}},
		},
	}, nil)

	s := &Scheduler{router: router}
	specA, err := s.workerSpecForTask(pool.Task{
		ID:         councilTaskID("plan-seat", 1),
		PlanID:     "plan-seat",
		Role:       plannerTaskRole,
		Complexity: string(ComplexityMedium),
	})
	if err != nil {
		t.Fatalf("workerSpecForTask A: %v", err)
	}
	specB, err := s.workerSpecForTask(pool.Task{
		ID:         councilTaskID("plan-seat", 2),
		PlanID:     "plan-seat",
		Role:       plannerTaskRole,
		Complexity: string(ComplexityMedium),
	})
	if err != nil {
		t.Fatalf("workerSpecForTask B: %v", err)
	}
	if specA.Provider != "anthropic" || specB.Provider != "openai" {
		t.Fatalf("seat routes = A:%+v B:%+v, want anthropic then openai", specA, specB)
	}
}

func TestWorkerCanRunCouncilTaskRejectsNonResidentIdleWorkerWhenRouteMismatches(t *testing.T) {
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
	if _, err := store.Create(StoredPlan{
		Plan: PlanRecord{PlanID: "plan-seat-dispatch", Title: "Seat dispatch", Lineage: "seat-dispatch"},
		Execution: ExecutionRecord{
			PlanID:          "plan-seat-dispatch",
			State:           planStateReviewing,
			CouncilMaxTurns: 4,
			CouncilSeats:    newCouncilSeats(),
		},
	}); err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-seat-dispatch"), "kitchen-test")
	worker, err := pm.SpawnWorker(pool.WorkerSpec{ID: "planner-openai", Role: plannerTaskRole, Provider: "openai", Model: "gpt-5.4"})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker(worker.ID, "container-"+worker.ID); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	s := &Scheduler{
		pm:    pm,
		plans: store,
		router: NewComplexityRouter(KitchenConfig{
			ProviderModels: DefaultKitchenConfig().ProviderModels,
			RoleProviders: map[string]ProviderPolicy{
				defaultRoutingRole: {Prefer: []string{"anthropic"}},
			},
		}, nil),
	}
	allowed, handled := s.workerCanRunCouncilTask(*worker, pool.Task{
		ID:         councilTaskID("plan-seat-dispatch", 1),
		PlanID:     "plan-seat-dispatch",
		Role:       plannerTaskRole,
		Complexity: string(ComplexityMedium),
	})
	if !handled {
		t.Fatal("expected council task handling")
	}
	if allowed {
		t.Fatal("expected mismatched idle worker to be rejected")
	}
}

func TestWorkerCanRunCouncilTaskAllowsLegacyWorkerWithUnknownModelByProvider(t *testing.T) {
	worker := pool.Worker{ID: "legacy", Provider: "anthropic", Role: plannerTaskRole}
	keys := []PoolKey{{Provider: "anthropic", Model: "sonnet"}}
	if !workerMatchesAnyRouteKey(worker, keys) {
		t.Fatal("expected worker with unknown model to match by provider")
	}
}

func TestScheduleReviewCouncilDoesNotReuseIdlePlannerWorker(t *testing.T) {
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
			PlanID:  "plan_review_spawn",
			Lineage: "feat-review-spawn",
			Title:   "Review spawn guard",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			PlanID:                "plan_review_spawn",
			State:                 planStateImplementationReview,
			ReviewCouncilMaxTurns: 4,
			ReviewCouncilSeats:    newReviewCouncilSeats(),
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-review-spawn"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{
		ID:       "planner-1",
		Role:     plannerTaskRole,
		Provider: "anthropic",
		Model:    "sonnet",
	}); err != nil {
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

	reviewTaskID := reviewCouncilTaskID(planID, 1)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn review): %v", err)
	}

	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1 fresh reviewer worker", len(host.spawnSpecs))
	}
	if host.spawnSpecs[0].Role != "reviewer" {
		t.Fatalf("spawn role = %q, want reviewer", host.spawnSpecs[0].Role)
	}
	if host.spawnSpecs[0].WorkspacePath == "" {
		t.Fatal("expected review spawn to have dedicated workspacePath")
	}

	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("review task status = %q, want queued before spawned reviewer is ready", task.Status)
	}
	if task.WorkerID != "" {
		t.Fatalf("review task workerID = %q, want no planner reuse", task.WorkerID)
	}

	planner, ok := pm.Worker("planner-1")
	if !ok {
		t.Fatal("planner-1 missing")
	}
	if planner.CurrentTaskID != "" {
		t.Fatalf("planner current task = %q, want idle", planner.CurrentTaskID)
	}
}

func TestRecoverReviewCouncilPlansOnStartupInvalidatesStaleReservedSeat(t *testing.T) {
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
	seats := newReviewCouncilSeats()
	seats[0].WorkerID = "reviewer-1"
	seats[0].Seat = "A"
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_review_stale_seat",
			Lineage: "feat-review-stale-seat",
			Title:   "Review stale seat recovery",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          seats,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-review-stale-seat"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{
		ID:       "reviewer-1",
		Role:     "reviewer",
		Provider: "anthropic",
		Model:    "sonnet",
	}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("reviewer-1", "container-reviewer-1"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.Heartbeat("reviewer-1", "idle", nil, ""); err != nil {
		t.Fatalf("Heartbeat reviewer: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.reapTimeout = time.Millisecond
	time.Sleep(5 * time.Millisecond)

	if err := s.recoverReviewCouncilPlansOnStartup(); err != nil {
		t.Fatalf("recoverReviewCouncilPlansOnStartup: %v", err)
	}

	worker, ok := pm.Worker("reviewer-1")
	if !ok {
		t.Fatal("reviewer-1 missing")
	}
	if worker.Status != pool.WorkerDead {
		t.Fatalf("worker status = %q, want dead", worker.Status)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if got := strings.TrimSpace(bundle.Execution.ReviewCouncilSeats[0].WorkerID); got != "" {
		t.Fatalf("seat worker = %q, want cleared", got)
	}
	reviewTaskID := reviewCouncilTaskID(planID, 2)
	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
}

func TestDispatchReadyTaskToWorkerSkipsStaleIdleWorker(t *testing.T) {
	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, t.TempDir(), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-stale", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-stale", "container-w-stale"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.Heartbeat("w-stale", "idle", nil, ""); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "work", Priority: 1, Role: "implementer"}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), nil, nil, nil, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.reapTimeout = time.Millisecond
	time.Sleep(5 * time.Millisecond)

	if err := s.dispatchReadyTaskToWorker("w-stale"); err != nil {
		t.Fatalf("dispatchReadyTaskToWorker: %v", err)
	}

	worker, ok := pm.Worker("w-stale")
	if !ok {
		t.Fatal("w-stale missing")
	}
	if worker.Status != pool.WorkerDead {
		t.Fatalf("worker status = %q, want dead", worker.Status)
	}
	task, ok := pm.Task("t-1")
	if !ok {
		t.Fatal("t-1 missing")
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
}

func TestSchedulerOnTaskCompletedMergesAndKillsWorker(t *testing.T) {
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
	// Kitchen workers are single-use: once the child worktree is
	// discarded, the container's bind mount is stale, so the worker
	// must be marked dead so the next task spawns a fresh container.
	worker, ok := pm.Worker("w-1")
	if !ok || worker.Status != pool.WorkerDead {
		t.Fatalf("worker state = %+v, want dead after task completion", worker)
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

func TestSchedulerOnPlannerTaskCompletedMigratesLineageMarker(t *testing.T) {
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

	const (
		planID     = "plan_rename"
		oldLineage = "original-lineage"
		newLineage = "renamed-lineage"
	)

	store := NewPlanStore(project.PlansDir)
	if _, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: oldLineage,
			Title:   "Initial plan",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	}); err != nil {
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

	// Simulate the active-plan marker that will be written when the
	// plan is eventually approved under its original lineage, or that
	// lingers from a previous approval. This is the state we want to
	// heal when the planner renames the lineage.
	if err := lineages.ActivatePlan(oldLineage, planID); err != nil {
		t.Fatalf("seed active plan: %v", err)
	}

	plannerTaskRuntimeID := councilTaskID(planID, 1)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         plannerTaskRuntimeID,
		PlanID:     planID,
		Prompt:     "plan this work",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn): %v", err)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1", len(host.spawnSpecs))
	}
	workerID := "w-1"
	if err := pm.RegisterWorker(workerID, "container-"+workerID); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch): %v", err)
	}
	dispatched, ok := pm.Task(plannerTaskRuntimeID)
	if !ok || dispatched.Status != pool.TaskDispatched || dispatched.WorkerID != workerID {
		t.Fatalf("planner task = %+v, want dispatched to %s", dispatched, workerID)
	}

	// Write a planner artifact that renames the lineage before the
	// task is marked completed.
	artifact := adapter.PlanArtifact{
		Lineage: newLineage,
		Title:   "Renamed plan",
		Tasks: []adapter.PlanArtifactTask{{
			ID:         "t1",
			Title:      "Do work",
			Prompt:     "execute",
			Complexity: string(ComplexityLow),
		}},
	}
	raw, err := json.Marshal(testCouncilArtifactForTask(*dispatched, artifact))
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	workerStateDir := pool.WorkerStateDir(pm.StateDir(), workerID)
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerPlanFile), raw, 0o644); err != nil {
		t.Fatalf("write plan artifact: %v", err)
	}

	if err := pm.CompleteTask(workerID, plannerTaskRuntimeID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.onTaskCompleted(plannerTaskRuntimeID); err != nil {
		t.Fatalf("onTaskCompleted: %v", err)
	}

	// The old-lineage marker must be gone and the new one must point
	// at this plan.
	if _, err := lineages.ActivePlan(oldLineage); !os.IsNotExist(err) {
		t.Fatalf("old-lineage marker still present: err=%v", err)
	}
	active, err := lineages.ActivePlan(newLineage)
	if err != nil {
		t.Fatalf("ActivePlan(%s): %v", newLineage, err)
	}
	if active != planID {
		t.Fatalf("ActivePlan(%s) = %q, want %q", newLineage, active, planID)
	}

	// Plan record should now record the renamed lineage.
	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if bundle.Plan.Lineage != newLineage {
		t.Fatalf("plan.Lineage = %q, want %q", bundle.Plan.Lineage, newLineage)
	}
}

func TestSchedulerAutoConvergesStructurallyEqualCouncilCandidate(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Surface council auto convergence", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	artifact := adapter.PlanArtifact{
		Title:   "Auto converge plan",
		Summary: "Keep the same council candidate",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Keep candidate stable",
			Prompt:           "Implement the stable plan.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	}

	turn1 := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole
	})
	completeCouncilTurnWithArtifact(t, k, bundle.Plan.PlanID, turn1, testCouncilArtifactForTask(mustTask(t, k.pm, turn1), artifact))

	turn2 := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole
	})
	second := testCouncilArtifactForTask(mustTask(t, k.pm, turn2), artifact)
	second.Stance = "revise"
	second.AdoptedPriorPlan = false
	second.Summary = "No substantive changes."
	second.SeatMemo = second.Summary
	completeCouncilTurnWithArtifact(t, k, bundle.Plan.PlanID, turn2, second)

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStatePendingApproval {
		t.Fatalf("execution state = %q, want %q", got.Execution.State, planStatePendingApproval)
	}
	if got.Execution.CouncilFinalDecision != councilConverged {
		t.Fatalf("final decision = %q, want converged", got.Execution.CouncilFinalDecision)
	}
	if len(got.Execution.CouncilTurns) != 2 {
		t.Fatalf("council turns = %d, want 2", len(got.Execution.CouncilTurns))
	}
	last := got.Execution.History[len(got.Execution.History)-1]
	if last.Type != planHistoryCouncilAutoConverged {
		t.Fatalf("last history = %+v, want council_auto_converged", last)
	}
}

func TestSchedulerAutoConvergedCouncilTriggersAutoApproval(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Auto approve identical council candidate", "", true, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	artifact := adapter.PlanArtifact{
		Title: "Auto approve plan",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Keep candidate stable",
			Prompt:           "Implement the stable plan.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	}

	turn1 := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole
	})
	completeCouncilTurnWithArtifact(t, k, bundle.Plan.PlanID, turn1, testCouncilArtifactForTask(mustTask(t, k.pm, turn1), artifact))

	turn2 := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole
	})
	second := testCouncilArtifactForTask(mustTask(t, k.pm, turn2), artifact)
	second.Stance = "revise"
	second.AdoptedPriorPlan = false
	second.Summary = "No substantive changes."
	second.SeatMemo = second.Summary

	var activated []string
	completeCouncilTurnWithArtifactUsingActivator(t, k, bundle.Plan.PlanID, turn2, second, func(planID string) error {
		activated = append(activated, planID)
		return nil
	})

	if len(activated) != 1 || activated[0] != bundle.Plan.PlanID {
		t.Fatalf("activated = %+v, want [%s]", activated, bundle.Plan.PlanID)
	}
}

func TestSchedulerRecoverCouncilPlansSkipsAutoConvergedPlan(t *testing.T) {
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
			PlanID:  "plan_auto_recover",
			Lineage: "auto-recover",
			Title:   "Recovered auto converge",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			PlanID:                "plan_auto_recover",
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 2,
			CouncilFinalDecision:  councilConverged,
			CouncilWarnings:       []adapter.CouncilDisagreement{{ID: "d1", Severity: "major", Title: "Warning", Category: "scope", Impact: "still okay"}},
			CouncilTurns:          []CouncilTurnRecord{{Turn: 1, Artifact: &adapter.CouncilTurnArtifact{Seat: "A", Turn: 1, CandidatePlan: &adapter.PlanArtifact{Title: "Recovered", Tasks: []adapter.PlanArtifactTask{{ID: "t1", Title: "Task", Prompt: "Do work", Complexity: "medium"}}}}}, {Turn: 2, Artifact: &adapter.CouncilTurnArtifact{Seat: "B", Turn: 2, CandidatePlan: &adapter.PlanArtifact{Title: "Recovered", Tasks: []adapter.PlanArtifactTask{{ID: "t1", Title: "Task", Prompt: "Do work", Complexity: "medium"}}}}}},
			History:               []PlanHistoryEntry{{Type: planHistoryCouncilAutoConverged, Cycle: 2, TaskID: councilTaskID("plan_auto_recover", 2), Summary: "Council auto-converged."}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-auto-recover"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	if err := s.recoverCouncilPlansOnStartup(); err != nil {
		t.Fatalf("recoverCouncilPlansOnStartup: %v", err)
	}
	if _, exists := pm.Task(councilTaskID(planID, 3)); exists {
		t.Fatal("unexpected new council turn enqueued for auto-converged plan")
	}
}

func mustTask(t *testing.T, pm *pool.PoolManager, taskID string) pool.Task {
	t.Helper()
	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	return *task
}

func completeCouncilTurnWithArtifact(t *testing.T, k *Kitchen, planID, taskID string, artifact adapter.CouncilTurnArtifact) {
	t.Helper()
	completeCouncilTurnWithArtifactUsingActivator(t, k, planID, taskID, artifact, k.ApprovePlan)
}

func completeCouncilTurnWithArtifactUsingActivator(t *testing.T, k *Kitchen, planID, taskID string, artifact adapter.CouncilTurnArtifact, activatePlan func(string) error) {
	t.Helper()

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.notify = k.sendNotify
	s.activatePlan = activatePlan
	k.scheduler = s

	workerID := "planner-" + planID + "-" + taskID
	if _, ok := k.pm.Worker(workerID); !ok {
		if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: workerID, Role: plannerTaskRole}); err != nil {
			t.Fatalf("SpawnWorker: %v", err)
		}
		if err := k.pm.RegisterWorker(workerID, "container-"+workerID); err != nil {
			t.Fatalf("RegisterWorker: %v", err)
		}
	}
	if err := k.pm.DispatchTask(taskID, workerID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	workerStateDir := pool.WorkerStateDir(k.pm.StateDir(), workerID)
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("Marshal council artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerPlanFile), raw, 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("planned\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := k.pm.CompleteTask(workerID, taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.onTaskCompleted(taskID); err != nil {
		t.Fatalf("onTaskCompleted: %v", err)
	}
}

func TestSchedulerKeepDeadWorkersRetainsThenEvictsUnderCap(t *testing.T) {
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
	const planID = "plan_keepdead"
	if _, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "keep-dead",
			Title:   "Keep dead workers",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
		},
	}); err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	cfg := DefaultKitchenConfig().Concurrency
	cfg.MaxWorkersTotal = 1
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, cfg, "kitchen-test")
	s.keepDeadWorkers = true

	// First task: spawn w-1, dispatch, make worker edit a file,
	// complete. With keepDeadWorkers the container must NOT be killed
	// yet — only MarkDead'd — and the ID tracked for later eviction.
	task1ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		PlanID:     planID,
		Prompt:     "first",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t1: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn t1): %v", err)
	}
	wt := host.spawnSpecs[0].WorkspacePath
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker w-1: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch t1): %v", err)
	}
	writeFile(t, filepath.Join(wt, "one.txt"), "1\n")
	mustRunGit(t, wt, "add", "one.txt")
	mustRunGit(t, wt, "commit", "-m", "t1")
	if err := pm.CompleteTask("w-1", task1ID); err != nil {
		t.Fatalf("CompleteTask t1: %v", err)
	}
	if err := s.onTaskCompleted(task1ID); err != nil {
		t.Fatalf("onTaskCompleted(t1): %v", err)
	}

	if len(host.killedWorkers) != 0 {
		t.Fatalf("killedWorkers = %v, want empty while worker is retained", host.killedWorkers)
	}
	worker1, ok := pm.Worker("w-1")
	if !ok || worker1.Status != pool.WorkerDead {
		t.Fatalf("worker w-1 = %+v, want dead", worker1)
	}
	if len(s.retainedDeadWorkers) != 1 || s.retainedDeadWorkers[0] != "w-1" {
		t.Fatalf("retainedDeadWorkers = %v, want [w-1]", s.retainedDeadWorkers)
	}

	// Second task: enqueuing and scheduling should evict w-1 (host
	// KillWorker) to make room under MaxWorkersTotal=1, then spawn a
	// fresh w-2 for t2.
	task2ID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-2",
		PlanID:     planID,
		Prompt:     "second",
		Complexity: string(ComplexityMedium),
		Priority:   2,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask t2: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn t2): %v", err)
	}

	if len(host.killedWorkers) != 1 || host.killedWorkers[0] != "w-1" {
		t.Fatalf("killedWorkers = %v, want [w-1] after eviction", host.killedWorkers)
	}
	if len(s.retainedDeadWorkers) != 0 {
		t.Fatalf("retainedDeadWorkers = %v, want empty after eviction", s.retainedDeadWorkers)
	}
	if len(host.spawnSpecs) != 2 {
		t.Fatalf("spawnSpecs count = %d, want 2 after eviction + fresh spawn", len(host.spawnSpecs))
	}
	if _, ok := pm.Task(task2ID); !ok {
		t.Fatal("task t-2 missing after schedule")
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
		ProviderModels: DefaultKitchenConfig().ProviderModels,
		RoleProviders: map[string]ProviderPolicy{
			defaultRoutingRole: {Prefer: []string{"openai"}},
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

func TestRecoverFailedTasksOnStartup_RevivesConflictTaskAndDiscardsWorktree(t *testing.T) {
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

	planID := "plan_startup_conflict_retry"
	taskID := planTaskRuntimeID(planID, "t1")
	store := NewPlanStore(project.PlansDir)
	_, err = store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "parser-errors",
			Title:   "Startup conflict retry",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks:   []PlanTask{{ID: "t1", Title: "Task 1", Prompt: "task 1", Complexity: ComplexityMedium}},
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{taskID},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-startup-conflict-retry"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", strings.TrimSpace(head)); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	worktreePath, err := gitMgr.CreateChildWorktree("parser-errors", taskID)
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("stat worktree before recovery: %v", err)
	}

	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.failurePolicy["conflict"] = FailurePolicyRule{Action: "retry_merge", Max: 2}

	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         taskID,
		PlanID:     planID,
		Prompt:     "task 1",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := pm.FailTask("w-1", taskID, "merge conflicts: shared.txt"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if err := s.recoverFailedTasksOnStartup(); err != nil {
		t.Fatalf("recoverFailedTasksOnStartup: %v", err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}
	if task.RetryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", task.RetryCount)
	}
	if !task.RequireFreshWorker {
		t.Fatal("expected retried task to require a fresh worker")
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected conflicting worktree to be removed, stat err = %v", err)
	}
	worker, ok := pm.Worker("w-1")
	if !ok || worker.Status != pool.WorkerDead {
		t.Fatalf("worker state = %+v, want dead after startup retry cleanup", worker)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	last := bundle.Execution.History[len(bundle.Execution.History)-1]
	if last.Type != planHistoryConflictRetried {
		t.Fatalf("history type = %q, want %q", last.Type, planHistoryConflictRetried)
	}
}

func TestRecoverOrphanedPlansOnStartup_MarksActivePlanPlanningFailed(t *testing.T) {
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
			PlanID:  "plan_orphaned_active",
			Lineage: "orphaned-active",
			Title:   "Orphaned active plan",
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State: planStateActive,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-orphaned-plan"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	notifications := make(chan pool.Notification, 1)
	s.notify = func(n pool.Notification) {
		notifications <- n
	}

	if err := s.recoverOrphanedPlansOnStartup(); err != nil {
		t.Fatalf("recoverOrphanedPlansOnStartup: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStatePlanningFailed {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStatePlanningFailed)
	}
	if bundle.Execution.State != planStatePlanningFailed {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStatePlanningFailed)
	}
	if bundle.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set")
	}
	if len(bundle.Execution.ActiveTaskIDs) != 0 || len(bundle.Execution.CompletedTaskIDs) != 0 || len(bundle.Execution.FailedTaskIDs) != 0 {
		t.Fatalf("expected task IDs to be cleared, got active=%v completed=%v failed=%v", bundle.Execution.ActiveTaskIDs, bundle.Execution.CompletedTaskIDs, bundle.Execution.FailedTaskIDs)
	}
	last := bundle.Execution.History[len(bundle.Execution.History)-1]
	if last.Type != planHistoryPlanningFailed {
		t.Fatalf("history type = %q, want %q", last.Type, planHistoryPlanningFailed)
	}
	select {
	case n := <-notifications:
		if n.Type != "plan_failed" {
			t.Fatalf("notification type = %q, want plan_failed", n.Type)
		}
		if n.ID != planID {
			t.Fatalf("notification id = %q, want %q", n.ID, planID)
		}
	default:
		t.Fatal("expected plan_failed notification")
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
			ActiveTaskIDs: []string{councilTaskID("plan_planner_timeout", 1)},
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
		ID:         councilTaskID(planID, 1),
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

func TestSyncPlanExecution_ImplReviewEnqueued(t *testing.T) {
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
			PlanID:  "plan_ir_enqueue",
			Lineage: "feat-ir",
			Title:   "Impl review enqueue test",
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:               planStateActive,
			ImplReviewRequested: true,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-enqueue"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
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
	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if err := s.syncPlanExecution(planID); err != nil {
		t.Fatalf("syncPlanExecution: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	if bundle.Execution.CompletedAt != nil {
		t.Fatalf("expected completedAt to be nil while review pending, got %v", bundle.Execution.CompletedAt)
	}

	reviewTaskID := reviewCouncilTaskID(planID, 1)
	reviewTask, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review council task %q not found in pool", reviewTaskID)
	}
	if reviewTask.Status != pool.TaskQueued {
		t.Fatalf("review council task status = %q, want %q", reviewTask.Status, pool.TaskQueued)
	}
	if reviewTask.PlanID != planID {
		t.Fatalf("review council task planID = %q, want %q", reviewTask.PlanID, planID)
	}
	host.spawnSpecs = nil
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn review): %v", err)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1 review council worker", len(host.spawnSpecs))
	}
	if host.spawnSpecs[0].WorkspacePath == "" {
		t.Fatal("expected review council worker to receive a review worktree")
	}
	if _, err := os.Stat(filepath.Join(host.spawnSpecs[0].WorkspacePath, ".git")); err != nil {
		t.Fatalf("review council worktree missing .git: %v", err)
	}
}

func TestSyncPlanExecution_NoImplReview(t *testing.T) {
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
			PlanID:  "plan_no_ir",
			Lineage: "feat-no-ir",
			Title:   "No impl review test",
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:               planStateActive,
			ImplReviewRequested: false,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-no-ir"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "implement change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
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
	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if err := s.syncPlanExecution(planID); err != nil {
		t.Fatalf("syncPlanExecution: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateCompleted)
	}
	if bundle.Plan.State != planStateCompleted {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateCompleted)
	}
	if bundle.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set after completion")
	}
}

func TestSyncPlanExecution_PreservesResearchComplete(t *testing.T) {
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
			PlanID: "plan_research_sync",
			Mode:   "research",
			Title:  "Research promotion readiness",
			State:  planStateResearchComplete,
		},
		Execution: ExecutionRecord{
			State:          planStateResearchComplete,
			ResearchOutput: "Research findings are available for promotion.",
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-research-complete"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	if err := s.syncPlanExecution(planID); err != nil {
		t.Fatalf("syncPlanExecution: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateResearchComplete {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateResearchComplete)
	}
	if bundle.Plan.State != planStateResearchComplete {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateResearchComplete)
	}
	if bundle.Execution.CompletedAt != nil {
		t.Fatalf("completedAt = %v, want nil for preserved research_complete", bundle.Execution.CompletedAt)
	}
}

func TestSyncPlanExecution_DoesNotRequeueCompletedImplementationReviewAfterFixMerge(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}
	reviewedAt := time.Now().UTC()

	store := NewPlanStore(project.PlansDir)
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_fix_merge_review_passed",
			Lineage: "feat-fix-merge-review-passed",
			Title:   "Fix merge after review pass",
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:                       planStateActive,
			ImplReviewRequested:         true,
			ImplReviewStatus:            planReviewStatusPassed,
			ImplReviewedAt:              &reviewedAt,
			ReviewCouncilTurnsCompleted: 2,
			ReviewCouncilFinalDecision:  reviewCouncilConverged,
			ActiveTaskIDs:               []string{planTaskRuntimeID("plan_fix_merge_review_passed", "fix-merge-123")},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-fix-merge-review-passed"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	fixTaskID := planTaskRuntimeID(planID, "fix-merge-123")
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         fixTaskID,
		PlanID:     planID,
		Prompt:     "resolve merge conflicts",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       lineageFixMergeRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: lineageFixMergeRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(fixTaskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := pm.CompleteTask("w-1", fixTaskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if err := s.syncPlanExecution(planID); err != nil {
		t.Fatalf("syncPlanExecution: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateCompleted)
	}
	if bundle.Plan.State != planStateCompleted {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateCompleted)
	}
	if bundle.Execution.ImplReviewStatus != planReviewStatusPassed {
		t.Fatalf("impl review status = %q, want %q", bundle.Execution.ImplReviewStatus, planReviewStatusPassed)
	}
	if bundle.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set after fix-merge completion")
	}
	if _, ok := pm.Task(reviewCouncilTaskID(planID, 1)); ok {
		t.Fatalf("unexpected review council task %q enqueued after fix-merge completion", reviewCouncilTaskID(planID, 1))
	}
}

func TestSyncPlanExecution_PreservesReviewingPlanWithoutTasks(t *testing.T) {
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
			PlanID:  "plan_reviewing_sync_guard",
			Lineage: "feat-reviewing-sync-guard",
			Title:   "Reviewing sync guard",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 1,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-reviewing-sync-guard"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	if err := s.syncPlanExecution(planID); err != nil {
		t.Fatalf("syncPlanExecution: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStateReviewing {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateReviewing)
	}
	if bundle.Execution.State != planStateReviewing {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateReviewing)
	}
	if bundle.Execution.CompletedAt != nil {
		t.Fatalf("completedAt = %v, want nil", bundle.Execution.CompletedAt)
	}
}

func TestReconcilePlanExecutionOnStartup_PreservesMergedPlanState(t *testing.T) {
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
			PlanID:  "plan_merged_restart_guard",
			Lineage: "feat-merged-restart-guard",
			Title:   "Merged restart guard",
			State:   planStateMerged,
		},
		Execution: ExecutionRecord{
			State:            planStateMerged,
			CompletedTaskIDs: []string{"t-1"},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-merged-restart-guard"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		PlanID:     planID,
		Prompt:     "already merged work",
		Complexity: string(ComplexityLow),
		Priority:   1,
		Role:       "implementer",
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
	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if err := s.reconcilePlanExecutionOnStartup(); err != nil {
		t.Fatalf("reconcilePlanExecutionOnStartup: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStateMerged {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateMerged)
	}
	if bundle.Execution.State != planStateMerged {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateMerged)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 0 {
		t.Fatalf("active task IDs = %+v, want empty", bundle.Execution.ActiveTaskIDs)
	}
	if len(bundle.Execution.CompletedTaskIDs) != 1 || bundle.Execution.CompletedTaskIDs[0] != taskID {
		t.Fatalf("completed task IDs = %+v, want [%s]", bundle.Execution.CompletedTaskIDs, taskID)
	}
}

func TestOnImplementationReviewCompleted_Pass(t *testing.T) {
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
			PlanID:  "plan_ir_pass",
			Lineage: "feat-ir-pass",
			Title:   "Impl review pass test",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: "pass",
					Summary: "initial review",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-pass"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(reviewTaskID, "w-rev"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}

	workerStateDir := pool.WorkerStateDir(pm.StateDir(), "w-rev")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	output := reviewCouncilTestArtifact(t, "B", 2, "pass", "converged", true, nil)
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(output), 0o644); err != nil {
		t.Fatalf("WriteFile review result: %v", err)
	}
	if err := pm.CompleteTask("w-rev", reviewTaskID); err != nil {
		t.Fatalf("CompleteTask review: %v", err)
	}

	if err := s.onTaskCompleted(reviewTaskID); err != nil {
		t.Fatalf("onTaskCompleted(review): %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateCompleted)
	}
	if bundle.Plan.State != planStateCompleted {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateCompleted)
	}
	if bundle.Execution.ImplReviewStatus != planReviewStatusPassed {
		t.Fatalf("impl review status = %q, want %q", bundle.Execution.ImplReviewStatus, planReviewStatusPassed)
	}
	if len(bundle.Execution.ImplReviewFindings) != 0 {
		t.Fatalf("impl review findings = %v, want empty for pass", bundle.Execution.ImplReviewFindings)
	}
	if bundle.Execution.ReviewCouncilFinalDecision != reviewCouncilConverged {
		t.Fatalf("review council decision = %q, want %q", bundle.Execution.ReviewCouncilFinalDecision, reviewCouncilConverged)
	}
	if bundle.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set after impl review pass")
	}
}

func TestOnImplementationReviewCompleted_PassDiscardsWorktreeAndKillsWorker(t *testing.T) {
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
			PlanID:  "plan_ir_pass_cleanup",
			Lineage: "feat-ir-pass-cleanup",
			Title:   "Impl review pass cleanup test",
			State:   planStateImplementationReview,
			Anchor: PlanAnchor{
				Branch: "main",
				Commit: strings.TrimSpace(head),
			},
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: "pass",
					Summary: "initial review",
				},
			}},
			Anchor: PlanAnchor{
				Branch: "main",
				Commit: strings.TrimSpace(head),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-pass-cleanup"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn review): %v", err)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1", len(host.spawnSpecs))
	}
	wt := host.spawnSpecs[0].WorkspacePath
	if wt == "" {
		t.Fatal("expected worktree for review council turn")
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch review): %v", err)
	}

	workerStateDir := pool.WorkerStateDir(pm.StateDir(), "w-1")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	output := reviewCouncilTestArtifact(t, "B", 2, "pass", "converged", true, nil)
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(output), 0o644); err != nil {
		t.Fatalf("WriteFile review result: %v", err)
	}
	if err := pm.CompleteTask("w-1", reviewTaskID); err != nil {
		t.Fatalf("CompleteTask review: %v", err)
	}

	if err := s.onTaskCompleted(reviewTaskID); err != nil {
		t.Fatalf("onTaskCompleted(review): %v", err)
	}

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected review council worktree to be removed, stat err = %v", err)
	}
	worker, ok := pm.Worker("w-1")
	if !ok || worker.Status != pool.WorkerDead {
		t.Fatalf("worker state = %+v, want dead after review council completion", worker)
	}
}

func TestOnImplementationReviewCompleted_FailQueuesAutoRemediation(t *testing.T) {
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
			PlanID:  "plan_ir_fail",
			Lineage: "feat-ir-fail",
			Title:   "Impl review fail test",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: "fail",
					Summary: "initial review",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-fail"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(reviewTaskID, "w-rev"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}

	workerStateDir := pool.WorkerStateDir(pm.StateDir(), "w-rev")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	output := reviewCouncilTestArtifact(t, "B", 2, "fail", "converged", true, []adapter.ReviewFinding{{
		ID:          "f1",
		File:        "pkg/service.go",
		Line:        27,
		Category:    "correctness",
		Description: "Missing error handling in the new path",
		Severity:    "major",
	}})
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(output), 0o644); err != nil {
		t.Fatalf("WriteFile review result: %v", err)
	}
	if err := pm.CompleteTask("w-rev", reviewTaskID); err != nil {
		t.Fatalf("CompleteTask review: %v", err)
	}

	if err := s.onTaskCompleted(reviewTaskID); err != nil {
		t.Fatalf("onTaskCompleted(review): %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if bundle.Plan.State != planStateActive {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateActive)
	}
	if !bundle.Execution.AutoRemediationActive {
		t.Fatal("expected auto-remediation to be active")
	}
	if bundle.Execution.AutoRemediationAttempt != 1 {
		t.Fatalf("auto remediation attempt = %d, want 1", bundle.Execution.AutoRemediationAttempt)
	}
	if bundle.Execution.AutoRemediationSource == nil {
		t.Fatal("expected remediation source to be persisted")
	}
	remediationTaskID := planTaskRuntimeID(planID, "review-fix-r1")
	if !containsString(bundle.Execution.ActiveTaskIDs, remediationTaskID) {
		t.Fatalf("active task ids = %v, want remediation task", bundle.Execution.ActiveTaskIDs)
	}
	queued, ok := pm.Task(remediationTaskID)
	if !ok {
		t.Fatalf("expected remediation task %q to be queued", remediationTaskID)
	}
	if queued.Status != pool.TaskQueued {
		t.Fatalf("remediation task status = %q, want %q", queued.Status, pool.TaskQueued)
	}
	foundFeedback := false
	for _, f := range autoRemediationFindings(bundle.Execution.AutoRemediationSource) {
		if strings.Contains(f, "Missing error handling") {
			foundFeedback = true
			break
		}
	}
	if !foundFeedback {
		t.Fatalf("auto remediation findings = %v, want feedback included", autoRemediationFindings(bundle.Execution.AutoRemediationSource))
	}
}

func TestOnImplementationReviewFailed(t *testing.T) {
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
			PlanID:  "plan_ir_infra_fail",
			Lineage: "feat-ir-infra",
			Title:   "Impl review infra failure test",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-infra"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(reviewTaskID, "w-rev"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}
	if err := pm.FailTask("w-rev", reviewTaskID, "container crashed"); err != nil {
		t.Fatalf("FailTask review: %v", err)
	}

	if err := s.onTaskFailed(reviewTaskID, FailureEnvironment); err != nil {
		t.Fatalf("onTaskFailed(review): %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q (environment failure should requeue review council turn)", bundle.Execution.State, planStateImplementationReview)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	task, ok := pm.Task(reviewTaskID)
	if !ok || task.Status != pool.TaskQueued {
		t.Fatalf("review task = %+v, want queued retry", task)
	}
}

func TestOnImplementationReviewFailed_AuthFailureRetriesWithFreshReviewer(t *testing.T) {
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
			PlanID:  "plan_ir_auth_retry",
			Lineage: "feat-ir-auth-retry",
			Title:   "Impl review auth retry test",
			State:   planStateImplementationReview,
			Anchor: PlanAnchor{
				Branch: "main",
				Commit: strings.TrimSpace(head),
			},
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: "pass",
					Summary: "initial review",
				},
			}},
			Anchor: PlanAnchor{
				Branch: "main",
				Commit: strings.TrimSpace(head),
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-auth"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.failurePolicy[string(FailureAuth)] = FailurePolicyRule{Action: authActionRecycleWorkerRetrySameProvider, Max: 1}

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(spawn review): %v", err)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs = %d, want 1", len(host.spawnSpecs))
	}
	initialSpawn := host.spawnSpecs[0]
	if initialSpawn.WorkspacePath == "" {
		t.Fatal("expected review auth retry test to use a dedicated workspace")
	}
	markerPath := filepath.Join(initialSpawn.WorkspacePath, "auth-retry-marker.txt")
	if err := os.WriteFile(markerPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}

	if err := pm.RegisterWorker(initialSpawn.ID, "container-"+initialSpawn.ID); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	host.spawnSpecs = nil
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule(dispatch review): %v", err)
	}

	if err := pm.FailTask(initialSpawn.ID, reviewTaskID, "Failed to authenticate. API Error: 401 Invalid authentication credentials"); err != nil {
		t.Fatalf("FailTask review: %v", err)
	}

	if err := s.onTaskFailed(reviewTaskID, FailureAuth); err != nil {
		t.Fatalf("onTaskFailed(review auth): %v", err)
	}

	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("review task status = %q, want %q", task.Status, pool.TaskQueued)
	}
	if task.RetryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", task.RetryCount)
	}
	if !task.RequireFreshWorker {
		t.Fatal("expected auth-retried review task to require a fresh worker")
	}

	worker, ok := pm.Worker(initialSpawn.ID)
	if !ok || worker.Status != pool.WorkerDead {
		t.Fatalf("worker state = %+v, want dead after auth retry cleanup", worker)
	}
	if len(host.killedWorkers) != 1 || host.killedWorkers[0] != initialSpawn.ID {
		t.Fatalf("killed workers = %+v, want [%s]", host.killedWorkers, initialSpawn.ID)
	}
	if len(host.spawnSpecs) != 1 {
		t.Fatalf("spawn specs after auth retry = %d, want 1", len(host.spawnSpecs))
	}
	if host.spawnSpecs[0].Role != "reviewer" {
		t.Fatalf("spawn role = %q, want reviewer", host.spawnSpecs[0].Role)
	}
	if host.spawnSpecs[0].WorkspacePath == "" {
		t.Fatal("expected fresh reviewer spawn to receive a workspace")
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("expected old review worktree contents to be discarded, stat err = %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	if len(bundle.Execution.FailedTaskIDs) != 0 {
		t.Fatalf("failed task IDs = %+v, want empty after auth revive", bundle.Execution.FailedTaskIDs)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 1 || bundle.Execution.ActiveTaskIDs[0] != reviewTaskID {
		t.Fatalf("active task IDs = %+v, want [%s]", bundle.Execution.ActiveTaskIDs, reviewTaskID)
	}
}

func TestSchedulerRunSkipsStartupRecoveryWhenRuntimeDiscoveryFails(t *testing.T) {
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

	planID := "plan_startup_conflict_outage"
	taskID := planTaskRuntimeID(planID, "t1")
	store := NewPlanStore(project.PlansDir)
	_, err = store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "parser-errors",
			Title:   "Startup conflict outage",
			Anchor:  PlanAnchor{Commit: strings.TrimSpace(head)},
			Tasks:   []PlanTask{{ID: "t1", Title: "Task 1", Prompt: "task 1", Complexity: ComplexityMedium}},
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:         planStateActive,
			ActiveTaskIDs: []string{taskID},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-startup-conflict-outage"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", strings.TrimSpace(head)); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.failurePolicy["conflict"] = FailurePolicyRule{Action: "retry_merge", Max: 2}
	s.reconcileInterval = 10 * time.Millisecond
	host.spawnSpecs = nil

	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         taskID,
		PlanID:     planID,
		Prompt:     "task 1",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := pm.FailTask("w-1", taskID, "merge conflicts: shared.txt"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	<-done

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskFailed {
		t.Fatalf("task status = %q, want failed while discovery is unavailable", task.Status)
	}
	if task.RetryCount != 0 {
		t.Fatalf("retryCount = %d, want 0", task.RetryCount)
	}
	if len(host.spawnSpecs) != 0 {
		t.Fatalf("spawn specs = %d, want 0 during startup outage", len(host.spawnSpecs))
	}
}

func TestSchedulerReconcileRuntimeDiscoveryFailurePausesSpawnsAndReapsStaleWorkers(t *testing.T) {
	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-runtime-discovery-fail"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-stale", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker stale: %v", err)
	}
	if err := pm.RegisterWorker("w-stale", "container-w-stale"); err != nil {
		t.Fatalf("RegisterWorker stale: %v", err)
	}
	if _, err := pm.EnqueueTask(pool.TaskSpec{ID: "t-inflight", Prompt: "work", Priority: 1, Role: "implementer"}); err != nil {
		t.Fatalf("EnqueueTask inflight: %v", err)
	}
	if err := pm.DispatchTask("t-inflight", "w-stale"); err != nil {
		t.Fatalf("DispatchTask inflight: %v", err)
	}
	if err := pm.Heartbeat("w-stale", "working", nil, ""); err != nil {
		t.Fatalf("Heartbeat stale worker: %v", err)
	}
	if _, err := pm.EnqueueTask(pool.TaskSpec{ID: "t-pending", Prompt: "more work", Priority: 2, Role: "implementer"}); err != nil {
		t.Fatalf("EnqueueTask pending: %v", err)
	}

	store := NewPlanStore(project.PlansDir)
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.reapTimeout = time.Millisecond
	notifications := make(chan pool.Notification, 8)
	s.notify = func(n pool.Notification) { notifications <- n }
	time.Sleep(5 * time.Millisecond)
	host.spawnSpecs = nil

	for attempt := 0; attempt < 2; attempt++ {
		if err := s.reconcile(); err == nil {
			t.Fatalf("reconcile attempt %d unexpectedly succeeded", attempt+1)
		}
		select {
		case n := <-notifications:
			t.Fatalf("unexpected notification on attempt %d: %+v", attempt+1, n)
		default:
		}
	}
	if err := s.reconcile(); err == nil {
		t.Fatal("third reconcile unexpectedly succeeded")
	}
	select {
	case n := <-notifications:
		if n.Type != "scheduler_runtime_discovery_unavailable" {
			t.Fatalf("notification type = %q, want scheduler_runtime_discovery_unavailable", n.Type)
		}
	default:
		t.Fatal("expected outage notification on third failure")
	}
	if err := s.reconcile(); err == nil {
		t.Fatal("fourth reconcile unexpectedly succeeded")
	}

	worker, ok := pm.Worker("w-stale")
	if !ok {
		t.Fatal("w-stale missing")
	}
	if worker.Status != pool.WorkerDead {
		t.Fatalf("worker status = %q, want dead after stale self-heal", worker.Status)
	}
	task, ok := pm.Task("t-inflight")
	if !ok {
		t.Fatal("t-inflight missing")
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("t-inflight status = %q, want queued", task.Status)
	}

	select {
	case n := <-notifications:
		t.Fatalf("unexpected duplicate notification: %+v", n)
	default:
	}

	if err := s.schedule(); err != nil {
		t.Fatalf("schedule during outage: %v", err)
	}
	if len(host.spawnSpecs) != 0 {
		t.Fatalf("spawn specs during outage = %d, want 0", len(host.spawnSpecs))
	}

	host.listErr = nil
	host.containers = nil
	if err := s.reconcile(); err != nil {
		t.Fatalf("reconcile after recovery: %v", err)
	}
	select {
	case n := <-notifications:
		if n.Type != "scheduler_runtime_discovery_recovered" {
			t.Fatalf("notification type = %q, want scheduler_runtime_discovery_recovered", n.Type)
		}
	default:
		t.Fatal("expected recovery notification")
	}
	if err := s.schedule(); err != nil {
		t.Fatalf("schedule after recovery: %v", err)
	}
	if len(host.spawnSpecs) != 2 {
		t.Fatalf("spawn specs after recovery = %d, want 2", len(host.spawnSpecs))
	}
}

func TestSchedulerReconcileRuntimeDiscoveryFailureInvalidatesReservedReviewSeat(t *testing.T) {
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
	seats := newReviewCouncilSeats()
	seats[0].WorkerID = "reviewer-1"
	seats[0].Seat = "A"
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_review_runtime_outage",
			Lineage: "feat-review-runtime-outage",
			Title:   "Review runtime outage",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          seats,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-review-runtime-outage"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{
		ID:       "reviewer-1",
		Role:     "reviewer",
		Provider: "anthropic",
		Model:    "sonnet",
	}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("reviewer-1", "container-reviewer-1"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.Heartbeat("reviewer-1", "idle", nil, ""); err != nil {
		t.Fatalf("Heartbeat reviewer: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.reapTimeout = time.Millisecond
	time.Sleep(5 * time.Millisecond)

	if err := s.reconcile(); err == nil {
		t.Fatal("reconcile unexpectedly succeeded")
	}

	worker, ok := pm.Worker("reviewer-1")
	if !ok {
		t.Fatal("reviewer-1 missing")
	}
	if worker.Status != pool.WorkerDead {
		t.Fatalf("worker status = %q, want dead", worker.Status)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if got := strings.TrimSpace(bundle.Execution.ReviewCouncilSeats[0].WorkerID); got != "" {
		t.Fatalf("seat worker = %q, want cleared", got)
	}
	reviewTaskID := reviewCouncilTaskID(planID, 2)
	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
}

func TestSchedulerReconcileRuntimeDiscoveryFailureInvalidatesReservedCouncilSeat(t *testing.T) {
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
	seats := newCouncilSeats()
	seats[0].WorkerID = "planner-1"
	seats[0].Seat = "A"
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_council_runtime_outage",
			Lineage: "feat-council-runtime-outage",
			Title:   "Council runtime outage",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       2,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          seats,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-runtime-outage"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{
		ID:       "planner-1",
		Role:     plannerTaskRole,
		Provider: "anthropic",
		Model:    "sonnet",
	}); err != nil {
		t.Fatalf("SpawnWorker planner: %v", err)
	}
	if err := pm.RegisterWorker("planner-1", "container-planner-1"); err != nil {
		t.Fatalf("RegisterWorker planner: %v", err)
	}
	if err := pm.Heartbeat("planner-1", "idle", nil, ""); err != nil {
		t.Fatalf("Heartbeat planner: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.reapTimeout = time.Millisecond
	time.Sleep(5 * time.Millisecond)

	if err := s.reconcile(); err == nil {
		t.Fatal("reconcile unexpectedly succeeded")
	}

	worker, ok := pm.Worker("planner-1")
	if !ok {
		t.Fatal("planner-1 missing")
	}
	if worker.Status != pool.WorkerDead {
		t.Fatalf("worker status = %q, want dead", worker.Status)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if got := strings.TrimSpace(bundle.Execution.CouncilSeats[0].WorkerID); got != "" {
		t.Fatalf("seat worker = %q, want cleared", got)
	}
	taskID := councilTaskID(planID, 2)
	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("council task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
}

func TestSchedulerReconcileAfterRuntimeDiscoveryRecoveryReplaysDeferredReviewFailure(t *testing.T) {
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
			PlanID:  "plan_ir_runtime_recovery",
			Lineage: "feat-ir-runtime-recovery",
			Title:   "Impl review recovery",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ActiveTaskIDs:               []string{reviewCouncilTaskID("plan_ir_runtime_recovery", 2)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-runtime-recovery"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(reviewTaskID, "w-rev"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}

	if err := s.reconcile(); err == nil {
		t.Fatal("reconcile unexpectedly succeeded")
	}

	if err := pm.FailTask("w-rev", reviewTaskID, "container crashed"); err != nil {
		t.Fatalf("FailTask review: %v", err)
	}
	s.handleNotification(pool.Notification{Type: "task_failed", ID: reviewTaskID})

	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskFailed {
		t.Fatalf("task status during outage = %q, want failed", task.Status)
	}
	if got := s.deferredTaskFailures[reviewTaskID]; got != FailureInfrastructure {
		t.Fatalf("deferred failure class = %q, want %q", got, FailureInfrastructure)
	}

	host.listErr = nil
	host.containers = nil
	if err := s.reconcile(); err != nil {
		t.Fatalf("reconcile after recovery: %v", err)
	}

	task, ok = pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found after recovery", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status after recovery = %q, want queued", task.Status)
	}
	if len(s.deferredTaskFailures) != 0 {
		t.Fatalf("deferred task failures = %v, want empty", s.deferredTaskFailures)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if !containsString(bundle.Execution.ActiveTaskIDs, reviewTaskID) {
		t.Fatalf("active task ids = %v, want %q present", bundle.Execution.ActiveTaskIDs, reviewTaskID)
	}
}

func TestSchedulerReconcileAfterRuntimeDiscoveryRecoveryReplaysDeferredCouncilFailure(t *testing.T) {
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
			PlanID:  "plan_council_runtime_recovery",
			Lineage: "feat-council-runtime-recovery",
			Title:   "Council recovery",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       2,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          newCouncilSeats(),
			ActiveTaskIDs:         []string{councilTaskID("plan_council_runtime_recovery", 2)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-runtime-recovery"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID := councilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         taskID,
		PlanID:     planID,
		Prompt:     "review the plan",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask council: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker planner: %v", err)
	}
	if err := pm.RegisterWorker("w-plan", "container-w-plan"); err != nil {
		t.Fatalf("RegisterWorker planner: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-plan"); err != nil {
		t.Fatalf("DispatchTask council: %v", err)
	}

	if err := s.reconcile(); err == nil {
		t.Fatal("reconcile unexpectedly succeeded")
	}

	if err := pm.FailTask("w-plan", taskID, "container crashed"); err != nil {
		t.Fatalf("FailTask council: %v", err)
	}
	s.handleNotification(pool.Notification{Type: "task_failed", ID: taskID})

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("council task %q not found", taskID)
	}
	if task.Status != pool.TaskFailed {
		t.Fatalf("task status during outage = %q, want failed", task.Status)
	}
	if got := s.deferredTaskFailures[taskID]; got != FailureInfrastructure {
		t.Fatalf("deferred failure class = %q, want %q", got, FailureInfrastructure)
	}

	host.listErr = nil
	host.containers = nil
	if err := s.reconcile(); err != nil {
		t.Fatalf("reconcile after recovery: %v", err)
	}

	task, ok = pm.Task(taskID)
	if !ok {
		t.Fatalf("council task %q not found after recovery", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status after recovery = %q, want queued", task.Status)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStateReviewing {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateReviewing)
	}
	if bundle.Execution.State != planStateReviewing {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateReviewing)
	}
	if !containsString(bundle.Execution.ActiveTaskIDs, taskID) {
		t.Fatalf("active task ids = %v, want %q present", bundle.Execution.ActiveTaskIDs, taskID)
	}
}

func TestOnImplementationReviewFailed_PlanFailureUsesBlockedArtifactPath(t *testing.T) {
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
			PlanID:  "plan_ir_plan_fail",
			Lineage: "feat-ir-plan-fail",
			Title:   "Impl review plan failure test",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: "fail",
					Summary: "initial fail",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-plan-fail"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(reviewTaskID, "w-rev"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}
	if err := pm.FailTask("w-rev", reviewTaskID, "invalid review council artifact (after 3 attempts): missing block"); err != nil {
		t.Fatalf("FailTask review: %v", err)
	}

	if err := s.onTaskFailed(reviewTaskID, FailurePlan); err != nil {
		t.Fatalf("onTaskFailed(review): %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReviewFailed {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReviewFailed)
	}
	if bundle.Execution.ImplReviewStatus != planReviewStatusFailed {
		t.Fatalf("impl review status = %q, want %q", bundle.Execution.ImplReviewStatus, planReviewStatusFailed)
	}
	if bundle.Execution.ReviewCouncilFinalDecision != reviewCouncilConverged {
		t.Fatalf("review council final decision = %q, want %q", bundle.Execution.ReviewCouncilFinalDecision, reviewCouncilConverged)
	}
	found := false
	for _, finding := range bundle.Execution.ImplReviewFindings {
		if strings.Contains(finding, "invalid review council artifact") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("impl review findings = %v, want blocked artifact message", bundle.Execution.ImplReviewFindings)
	}
}

func TestOnImplementationReviewTaskFailurePreservesPriorFindings(t *testing.T) {
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
			PlanID:  "plan_ir_failure_preserve",
			Lineage: "feat-ir-failure-preserve",
			Title:   "Impl review preserve findings test",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       6,
			ReviewCouncilTurnsCompleted: 5,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 5,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    5,
					Stance:  "revise",
					Verdict: "fail",
					Findings: []adapter.ReviewFinding{{
						ID:          "f1",
						File:        "cmd/kitchen/planner.go",
						Line:        101,
						Category:    "correctness",
						Description: "Preserve review findings when the next turn crashes",
						Severity:    "major",
					}},
					Summary: "prior substantive fail",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-failure-preserve"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 6)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(reviewTaskID, "w-rev"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}
	if err := pm.FailTask("w-rev", reviewTaskID, "adapter exited with code 1"); err != nil {
		t.Fatalf("FailTask review: %v", err)
	}

	if err := s.onTaskFailed(reviewTaskID, FailureUnknown); err != nil {
		t.Fatalf("onTaskFailed(review): %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReviewFailed {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReviewFailed)
	}
	if !containsString(bundle.Execution.FailedTaskIDs, reviewTaskID) {
		t.Fatalf("failed task ids = %+v, want %q present", bundle.Execution.FailedTaskIDs, reviewTaskID)
	}
	foundPrior := false
	foundCrash := false
	for _, finding := range bundle.Execution.ImplReviewFindings {
		if strings.Contains(finding, "Preserve review findings") {
			foundPrior = true
		}
		if strings.Contains(finding, "adapter exited with code 1") {
			foundCrash = true
		}
	}
	if !foundPrior || !foundCrash {
		t.Fatalf("impl review findings = %v, want prior review finding and crash message preserved", bundle.Execution.ImplReviewFindings)
	}
}

func TestRecoverReviewCouncilPlansOnStartup_RevivesCanceledTask(t *testing.T) {
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
			PlanID:  "plan_ir_cancel_recover",
			Lineage: "feat-ir-cancel-recover",
			Title:   "Impl review cancel recovery",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-cancel-recover"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	k := &Kitchen{pm: pm, planStore: store}
	if err := k.CancelTask(reviewTaskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	if err := s.recoverReviewCouncilPlansOnStartup(); err != nil {
		t.Fatalf("recoverReviewCouncilPlansOnStartup: %v", err)
	}

	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("review task status = %q, want %q", task.Status, pool.TaskQueued)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 1 || bundle.Execution.ActiveTaskIDs[0] != reviewTaskID {
		t.Fatalf("active task ids = %+v, want [%q]", bundle.Execution.ActiveTaskIDs, reviewTaskID)
	}
}

func TestApplyReviewCouncilTurnResult_ActionableFailStartsAutoRemediation(t *testing.T) {
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
			PlanID:  "plan_ir_auto_fix",
			Lineage: "feat-ir-auto-fix",
			Title:   "Implementation review auto-fix",
			Tasks: []PlanTask{{
				ID:               "t1",
				Title:            "Implement",
				Prompt:           "implement",
				Complexity:       ComplexityMedium,
				ReviewComplexity: ComplexityHigh,
				TimeoutMinutes:   25,
			}},
			State: planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: pool.ReviewFail,
					Summary: "First seat found a real bug.",
					Findings: []adapter.ReviewFinding{{
						ID:          "f1",
						File:        "cmd/kitchen/planner.go",
						Line:        42,
						Category:    "correctness",
						Description: "A follow-up implementation task should be created.",
						Suggestion:  "Create a remediation loop.",
						Severity:    pool.SeverityMajor,
					}},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-auto-fix"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	task := pool.Task{
		ID:         reviewCouncilTaskID(planID, 2),
		PlanID:     planID,
		WorkerID:   "w-rev",
		Role:       "reviewer",
		Complexity: string(ComplexityHigh),
		Status:     pool.TaskCompleted,
	}
	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	artifact := &adapter.ReviewCouncilTurnArtifact{
		Seat:                "B",
		Turn:                2,
		Stance:              "converged",
		Verdict:             pool.ReviewFail,
		AdoptedPriorVerdict: true,
		Summary:             "Second seat agrees the issue is real.",
		Findings: []adapter.ReviewFinding{{
			ID:          "f2",
			File:        "cmd/kitchen/scheduler_review_council.go",
			Line:        1,
			Category:    "correctness",
			Description: "Semantic fail should trigger remediation.",
			Suggestion:  "Queue a synthetic implementation task.",
			Severity:    pool.SeverityCritical,
		}},
	}
	if err := s.applyReviewCouncilTurnResult(task, bundle, artifact); err != nil {
		t.Fatalf("applyReviewCouncilTurnResult: %v", err)
	}

	bundle, err = store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan after remediation: %v", err)
	}
	if bundle.Plan.State != planStateActive {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateActive)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if !bundle.Execution.AutoRemediationActive {
		t.Fatal("expected auto-remediation to be active")
	}
	if bundle.Execution.AutoRemediationAttempt != 1 {
		t.Fatalf("attempt = %d, want 1", bundle.Execution.AutoRemediationAttempt)
	}
	if bundle.Execution.ImplReviewStatus != "" {
		t.Fatalf("impl review status = %q, want cleared", bundle.Execution.ImplReviewStatus)
	}
	if len(bundle.Execution.ReviewCouncilTurns) != 0 {
		t.Fatalf("review council turns = %d, want cleared", len(bundle.Execution.ReviewCouncilTurns))
	}
	if bundle.Execution.AutoRemediationSource == nil {
		t.Fatal("expected remediation source to be persisted")
	}
	if !planHasTask(bundle.Plan, "review-fix-r1") {
		t.Fatalf("plan tasks missing remediation task: %+v", bundle.Plan.Tasks)
	}
	remediationTaskID := planTaskRuntimeID(planID, "review-fix-r1")
	queued, ok := pm.Task(remediationTaskID)
	if !ok {
		t.Fatalf("remediation task %q not found", remediationTaskID)
	}
	if queued.Status != pool.TaskQueued {
		t.Fatalf("remediation task status = %q, want queued", queued.Status)
	}
	if queued.Role != "implementer" {
		t.Fatalf("remediation task role = %q, want implementer", queued.Role)
	}
	if !queued.RequireFreshWorker {
		t.Fatal("remediation task should require a fresh worker")
	}
	if !containsString(bundle.Execution.ActiveTaskIDs, remediationTaskID) {
		t.Fatalf("active task ids = %v, want remediation task", bundle.Execution.ActiveTaskIDs)
	}
}

func TestApplyReviewCouncilTurnResult_DisagreementOnlyRejectRemainsTerminal(t *testing.T) {
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
			PlanID:  "plan_ir_reject_terminal",
			Lineage: "feat-ir-reject-terminal",
			Title:   "Implementation review reject",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReview,
			ImplReviewRequested:         true,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 1,
			ReviewCouncilSeats:          newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.ReviewCouncilTurnArtifact{
					Seat:    "A",
					Turn:    1,
					Stance:  "propose",
					Verdict: pool.ReviewPass,
					Summary: "Seat A passes.",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-reject-terminal"), "kitchen-test")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-rev", "container-w-rev"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	task := pool.Task{ID: reviewCouncilTaskID(planID, 2), PlanID: planID, WorkerID: "w-rev", Role: "reviewer", Status: pool.TaskCompleted}
	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	artifact := &adapter.ReviewCouncilTurnArtifact{
		Seat:    "B",
		Turn:    2,
		Stance:  "revise",
		Verdict: pool.ReviewFail,
		Summary: "Hard-cap disagreement without actionable findings.",
		Disagreements: []adapter.CouncilDisagreement{{
			ID:       "d1",
			Severity: pool.SeverityMajor,
			Category: "correctness",
			Title:    "Disagreement only",
			Impact:   "Seats disagree but no concrete finding was produced.",
		}},
	}
	if err := s.applyReviewCouncilTurnResult(task, bundle, artifact); err != nil {
		t.Fatalf("applyReviewCouncilTurnResult: %v", err)
	}

	bundle, err = store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan after reject: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReviewFailed {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReviewFailed)
	}
	if bundle.Execution.AutoRemediationActive {
		t.Fatal("did not expect auto-remediation for disagreement-only reject")
	}
	if _, ok := pm.Task(planTaskRuntimeID(planID, "review-fix-r1")); ok {
		t.Fatal("unexpected remediation task for disagreement-only reject")
	}
}

func TestSchedulerRunRecoversAutoRemediationIntentBeforeStartupReconcile(t *testing.T) {
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
			PlanID:  "plan_ir_recovery",
			Lineage: "feat-ir-recovery",
			Title:   "Auto-remediation recovery",
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:                       planStateActive,
			ImplReviewRequested:         true,
			AutoRemediationAttempt:      1,
			AutoRemediationActive:       true,
			AutoRemediationPlanTaskID:   "review-fix-r1",
			AutoRemediationTaskID:       planTaskRuntimeID("plan_ir_recovery", "review-fix-r1"),
			AutoRemediationSourceTaskID: reviewCouncilTaskID("plan_ir_recovery", 2),
			AutoRemediationSource: &AutoRemediationSourceRecord{
				Decision:     reviewCouncilConverged,
				Verdict:      pool.ReviewFail,
				Seat:         "B",
				Turn:         2,
				ReviewTaskID: reviewCouncilTaskID("plan_ir_recovery", 2),
				Summary:      "Recover persisted remediation intent.",
				Findings: []adapter.ReviewFinding{{
					ID:          "f1",
					File:        "cmd/kitchen/scheduler.go",
					Category:    "correctness",
					Description: "Recovered remediation must be requeued.",
					Severity:    pool.SeverityMajor,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{listErr: errors.New("runtime unavailable")}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-recovery"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")
	s.reconcileInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	<-done

	remediationTaskID := planTaskRuntimeID(planID, "review-fix-r1")
	task, ok := pm.Task(remediationTaskID)
	if !ok {
		t.Fatalf("recovered remediation task %q not found", remediationTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("recovered remediation task status = %q, want queued", task.Status)
	}
	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan after recovery: %v", err)
	}
	if !planHasTask(bundle.Plan, "review-fix-r1") {
		t.Fatalf("plan tasks missing recovered remediation task: %+v", bundle.Plan.Tasks)
	}
	if !containsString(bundle.Execution.ActiveTaskIDs, remediationTaskID) {
		t.Fatalf("active task ids = %v, want recovered remediation task", bundle.Execution.ActiveTaskIDs)
	}
}

func TestSyncPlanExecutionCompletedAutoRemediationRequeuesImplementationReview(t *testing.T) {
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
	planID := "plan_ir_retry_cycle"
	remediationTaskID := planTaskRuntimeID(planID, "review-fix-r1")
	_, err = store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "feat-ir-retry-cycle",
			Title:   "Retry implementation review",
			State:   planStateActive,
			Tasks: []PlanTask{{
				ID:               "t1",
				Title:            "Implement",
				Prompt:           "implement",
				Complexity:       ComplexityMedium,
				ReviewComplexity: ComplexityMedium,
			}, {
				ID:               "review-fix-r1",
				Title:            "Address implementation review findings",
				Prompt:           "fix review findings",
				Complexity:       ComplexityMedium,
				ReviewComplexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                       planStateActive,
			ImplReviewRequested:         true,
			ReviewCouncilCycle:          1,
			AutoRemediationAttempt:      1,
			AutoRemediationActive:       true,
			AutoRemediationPlanTaskID:   "review-fix-r1",
			AutoRemediationTaskID:       remediationTaskID,
			AutoRemediationSourceTaskID: reviewCouncilTaskID(planID, 2),
			AutoRemediationSource: &AutoRemediationSourceRecord{
				Decision:     reviewCouncilConverged,
				Verdict:      pool.ReviewFail,
				Seat:         "B",
				Turn:         2,
				ReviewTaskID: reviewCouncilTaskID(planID, 2),
				Summary:      "Queued remediation should re-enter review when done.",
				Findings: []adapter.ReviewFinding{{
					ID:          "f1",
					Category:    "correctness",
					Description: "Review should be requeued after remediation.",
					Severity:    pool.SeverityMajor,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-ir-retry-cycle"), "kitchen-test")
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:                 remediationTaskID,
		PlanID:             planID,
		Prompt:             "fix review findings",
		Complexity:         string(ComplexityMedium),
		Priority:           1,
		Role:               "implementer",
		RequireFreshWorker: true,
	}); err != nil {
		t.Fatalf("EnqueueTask remediation: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-fix", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker implementer: %v", err)
	}
	if err := pm.RegisterWorker("w-fix", "container-w-fix"); err != nil {
		t.Fatalf("RegisterWorker implementer: %v", err)
	}
	if err := pm.DispatchTask(remediationTaskID, "w-fix"); err != nil {
		t.Fatalf("DispatchTask remediation: %v", err)
	}
	if err := pm.CompleteTask("w-fix", remediationTaskID); err != nil {
		t.Fatalf("CompleteTask remediation: %v", err)
	}
	staleReviewTaskID := reviewCouncilTaskID(planID, 1)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:       staleReviewTaskID,
		PlanID:   planID,
		Prompt:   "stale prior review",
		Priority: 1,
		Role:     "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask stale review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-review-old", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker stale reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-review-old", "container-w-review-old"); err != nil {
		t.Fatalf("RegisterWorker stale reviewer: %v", err)
	}
	if err := pm.DispatchTask(staleReviewTaskID, "w-review-old"); err != nil {
		t.Fatalf("DispatchTask stale review: %v", err)
	}
	if err := pm.CompleteTask("w-review-old", staleReviewTaskID); err != nil {
		t.Fatalf("CompleteTask stale review: %v", err)
	}

	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	if err := s.syncPlanExecution(planID); err != nil {
		t.Fatalf("syncPlanExecution: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan after sync: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if bundle.Execution.AutoRemediationActive {
		t.Fatal("expected auto-remediation to clear after successful completion")
	}
	if bundle.Execution.ReviewCouncilCycle != 2 {
		t.Fatalf("review council cycle = %d, want 2", bundle.Execution.ReviewCouncilCycle)
	}
	reviewTaskID := reviewCouncilTaskIDForCycle(planID, 2, 1)
	task, ok := pm.Task(reviewTaskID)
	if !ok {
		t.Fatalf("review task %q not found", reviewTaskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("review task status = %q, want queued", task.Status)
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
