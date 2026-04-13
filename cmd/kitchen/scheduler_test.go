package main

import (
	"context"
	"encoding/json"
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

func TestSchedulerWorkerSpecForTaskUsesRoleAwareRouting(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
			},
		},
		RoleRouting: map[string]map[Complexity]RoutingRule{
			"reviewer": {
				ComplexityMedium: {
					Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
				},
			},
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
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
			},
		},
		CouncilSeats: map[string]CouncilSeatRoutingConfig{
			"B": {
				Default: RoutingRule{
					Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
				},
			},
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
			Routing: map[Complexity]RoutingRule{
				ComplexityMedium: {Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}}},
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

func TestOnImplementationReviewCompleted_Fail(t *testing.T) {
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
	if bundle.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateCompleted)
	}
	if bundle.Plan.State != planStateCompleted {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateCompleted)
	}
	if bundle.Execution.ImplReviewStatus != planReviewStatusFailed {
		t.Fatalf("impl review status = %q, want %q", bundle.Execution.ImplReviewStatus, planReviewStatusFailed)
	}
	if len(bundle.Execution.ImplReviewFindings) == 0 {
		t.Fatal("expected impl review findings to be populated on fail")
	}
	foundFeedback := false
	for _, f := range bundle.Execution.ImplReviewFindings {
		if strings.Contains(f, "Missing error handling") {
			foundFeedback = true
			break
		}
	}
	if !foundFeedback {
		t.Fatalf("impl review findings = %v, want feedback included", bundle.Execution.ImplReviewFindings)
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
	if bundle.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateCompleted)
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

func TestRecoverReviewCouncilPlansOnStartup_FinalizesLegacyCompletedReview(t *testing.T) {
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
			PlanID:  "plan_legacy_ir_pass",
			Lineage: "feat-legacy-ir-pass",
			Title:   "Legacy impl review pass",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:               planStateImplementationReview,
			ImplReviewRequested: true,
			ActiveTaskIDs:       []string{planTaskRuntimeID("plan_legacy_ir_pass", "impl-review-1")},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-legacy-ir-pass"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID := planTaskRuntimeID(planID, "impl-review-1")
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         taskID,
		PlanID:     planID,
		Prompt:     "legacy implementation review",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-legacy", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker reviewer: %v", err)
	}
	if err := pm.RegisterWorker("w-legacy", "container-w-legacy"); err != nil {
		t.Fatalf("RegisterWorker reviewer: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-legacy"); err != nil {
		t.Fatalf("DispatchTask review: %v", err)
	}

	workerStateDir := pool.WorkerStateDir(pm.StateDir(), "w-legacy")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	output := "<review><verdict>pass</verdict><feedback>LGTM</feedback><severity>minor</severity></review>"
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(output), 0o644); err != nil {
		t.Fatalf("WriteFile review result: %v", err)
	}
	if err := pm.CompleteTask("w-legacy", taskID); err != nil {
		t.Fatalf("CompleteTask review: %v", err)
	}

	if err := s.recoverReviewCouncilPlansOnStartup(); err != nil {
		t.Fatalf("recoverReviewCouncilPlansOnStartup: %v", err)
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
	if bundle.Execution.ImplReviewStatus != planReviewStatusPassed {
		t.Fatalf("impl review status = %q, want %q", bundle.Execution.ImplReviewStatus, planReviewStatusPassed)
	}
	if _, ok := pm.Task(taskID); ok {
		t.Fatalf("legacy task %q still present after recovery", taskID)
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
