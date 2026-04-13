package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestOnCouncilTurnFailed_FailurePlanSynthesizesBlockedSeat(t *testing.T) {
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
			PlanID:  "plan_council_plan_fail",
			Lineage: "council-plan-fail",
			Title:   "Planner council plan failure",
			Summary: "Recover from planner artifact failures.",
			State:   planStateReviewing,
			Tasks: []PlanTask{{
				ID:         "t1",
				Title:      "Do work",
				Prompt:     "Implement the change.",
				Complexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          newCouncilSeats(),
			CouncilTurns: []CouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.CouncilTurnArtifact{
					Seat:   "A",
					Turn:   1,
					Stance: "propose",
					CandidatePlan: &adapter.PlanArtifact{
						Title: "Planner council plan failure",
						Tasks: []adapter.PlanArtifactTask{{
							ID:         "t1",
							Title:      "Do work",
							Prompt:     "Implement the change.",
							Complexity: "medium",
						}},
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-plan-fail"), "kitchen-test")
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
		Prompt:     "plan this change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-plan", "container-w-plan"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-plan"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := pm.FailTask("w-plan", taskID, "invalid plan artifact (after 3 attempts): extraction failed"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if err := s.onCouncilTurnFailed(mustTask(t, pm, taskID), FailurePlan); err != nil {
		t.Fatalf("onCouncilTurnFailed: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State == planStatePlanningFailed {
		t.Fatalf("execution state = %q, did not expect planning_failed", bundle.Execution.State)
	}
	if bundle.Execution.CouncilTurnsCompleted != 2 {
		t.Fatalf("council turns completed = %d, want 2", bundle.Execution.CouncilTurnsCompleted)
	}
	if len(bundle.Execution.CouncilTurns) != 2 {
		t.Fatalf("council turns = %d, want 2", len(bundle.Execution.CouncilTurns))
	}
	last := bundle.Execution.CouncilTurns[len(bundle.Execution.CouncilTurns)-1]
	if last.Artifact == nil || last.Artifact.Stance != "blocked" {
		t.Fatalf("last artifact = %+v, want blocked stance", last.Artifact)
	}
	if last.Artifact.CandidatePlan == nil {
		t.Fatal("expected blocked artifact to carry a synthesized candidate plan")
	}
	nextTaskID := councilTaskID(planID, 3)
	nextTask, ok := pm.Task(nextTaskID)
	if !ok || nextTask.Status != pool.TaskQueued {
		t.Fatalf("next task = %+v, want queued turn 3 council task", nextTask)
	}
}

func TestOnTaskFailed_ClassifiedFailurePlanRoutesToBlockedSeatRecovery(t *testing.T) {
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
			PlanID:  "plan_council_classified_fail",
			Lineage: "council-classified-fail",
			Title:   "Planner council classified failure",
			Summary: "Recover from classified planner artifact failures.",
			State:   planStateReviewing,
			Tasks: []PlanTask{{
				ID:         "t1",
				Title:      "Do work",
				Prompt:     "Implement the change.",
				Complexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          newCouncilSeats(),
			CouncilTurns: []CouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.CouncilTurnArtifact{
					Seat:   "A",
					Turn:   1,
					Stance: "propose",
					CandidatePlan: &adapter.PlanArtifact{
						Title: "Planner council classified failure",
						Tasks: []adapter.PlanArtifactTask{{
							ID:         "t1",
							Title:      "Do work",
							Prompt:     "Implement the change.",
							Complexity: "medium",
						}},
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-classified-fail"), "kitchen-test")
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
		Prompt:     "plan this change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-plan", "container-w-plan"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-plan"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	reported := "invalid plan artifact (after 3 attempts): extraction failed after 3 attempts: plan block not found"
	if err := pm.FailTask("w-plan", taskID, reported); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if err := s.onTaskFailed(taskID, ClassifyFailure(reported, nil, KitchenSignals{})); err != nil {
		t.Fatalf("onTaskFailed: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State == planStatePlanningFailed {
		t.Fatalf("execution state = %q, did not expect planning_failed", bundle.Execution.State)
	}
	if bundle.Execution.CouncilTurnsCompleted != 2 {
		t.Fatalf("council turns completed = %d, want 2", bundle.Execution.CouncilTurnsCompleted)
	}
	last := bundle.Execution.CouncilTurns[len(bundle.Execution.CouncilTurns)-1]
	if last.Artifact == nil || last.Artifact.Stance != "blocked" {
		t.Fatalf("last artifact = %+v, want blocked stance", last.Artifact)
	}
}

func TestOnTaskFailed_ClassifiedFailurePlanRecoversTurnOnePlanning(t *testing.T) {
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
			PlanID:  "plan_council_turn_one_fail",
			Lineage: "council-turn-one-fail",
			Title:   "Planner council turn one failure",
			Summary: "Recover from initial planner artifact failures.",
			State:   planStatePlanning,
			Tasks: []PlanTask{{
				ID:         "t1",
				Title:      "Do work",
				Prompt:     "Implement the change.",
				Complexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                 planStatePlanning,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 0,
			CouncilSeats:          newCouncilSeats(),
			ActiveTaskIDs:         []string{councilTaskID("plan_council_turn_one_fail", 1)},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-turn-one-fail"), "kitchen-test")
	gitMgr, err := NewGitManager(repo, paths.WorktreesDir)
	if err != nil {
		t.Fatal(err)
	}
	lineages := NewLineageManager(project.LineagesDir, project.PlansDir)
	s := NewScheduler(pm, host, NewComplexityRouter(DefaultKitchenConfig(), nil), gitMgr, store, lineages, DefaultKitchenConfig().Concurrency, "kitchen-test")

	taskID := councilTaskID(planID, 1)
	if _, err := pm.EnqueueTask(pool.TaskSpec{
		ID:         taskID,
		PlanID:     planID,
		Prompt:     "plan this change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-plan", "container-w-plan"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-plan"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	reported := "invalid plan artifact (after 3 attempts): extraction failed after 3 attempts: plan block not found"
	if err := pm.FailTask("w-plan", taskID, reported); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if err := s.onTaskFailed(taskID, ClassifyFailure(reported, nil, KitchenSignals{})); err != nil {
		t.Fatalf("onTaskFailed: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State == planStatePlanningFailed {
		t.Fatalf("execution state = %q, did not expect planning_failed", bundle.Execution.State)
	}
	if bundle.Execution.CouncilTurnsCompleted != 1 {
		t.Fatalf("council turns completed = %d, want 1", bundle.Execution.CouncilTurnsCompleted)
	}
	if bundle.Execution.CouncilTurns[0].Artifact == nil || bundle.Execution.CouncilTurns[0].Artifact.Stance != "blocked" {
		t.Fatalf("first turn artifact = %+v, want blocked stance", bundle.Execution.CouncilTurns[0].Artifact)
	}
	nextTaskID := councilTaskID(planID, 2)
	nextTask, ok := pm.Task(nextTaskID)
	if !ok || nextTask.Status != pool.TaskQueued {
		t.Fatalf("next task = %+v, want queued turn 2 council task", nextTask)
	}
}

func TestOnCouncilTurnCompleted_NilCandidateAdoption(t *testing.T) {
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
	initialPlan := PlanRecord{
		PlanID:  "plan_council_nil_adoption",
		Lineage: "council-nil-adoption",
		Title:   "Parser cleanup",
		Summary: "Keep prior plan intact.",
		State:   planStateReviewing,
		Tasks: []PlanTask{{
			ID:         "t1",
			Title:      "Normalize parser errors",
			Prompt:     "Do work",
			Complexity: ComplexityMedium,
		}},
	}
	planID, err := store.Create(StoredPlan{
		Plan: initialPlan,
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          newCouncilSeats(),
			CouncilTurns: []CouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.CouncilTurnArtifact{
					Seat:   "A",
					Turn:   1,
					Stance: "propose",
					CandidatePlan: &adapter.PlanArtifact{
						Title: "Parser cleanup",
						Tasks: []adapter.PlanArtifactTask{{
							ID:         "t1",
							Title:      "Normalize parser errors",
							Prompt:     "Do work",
							Complexity: "medium",
						}},
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-nil-adoption"), "kitchen-test")
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
		Prompt:     "plan this change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-plan", "container-w-plan"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-plan"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	workerStateDir := pool.WorkerStateDir(pm.StateDir(), "w-plan")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	raw, err := json.Marshal(adapter.CouncilTurnArtifact{
		Seat:             "B",
		Turn:             2,
		Stance:           "converged",
		AdoptedPriorPlan: true,
		CandidatePlan:    nil,
		Summary:          "No changes needed.",
	})
	if err != nil {
		t.Fatalf("Marshal artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerPlanFile), raw, 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("planned\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := pm.CompleteTask("w-plan", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if err := s.onCouncilTurnCompleted(mustTask(t, pm, taskID)); err != nil {
		t.Fatalf("onCouncilTurnCompleted: %v", err)
	}

	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.State != planStatePendingApproval {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStatePendingApproval)
	}
	if bundle.Execution.CouncilTurnsCompleted != 2 {
		t.Fatalf("council turns completed = %d, want 2", bundle.Execution.CouncilTurnsCompleted)
	}
	if len(bundle.Execution.CouncilTurns) != 2 {
		t.Fatalf("council turns = %d, want 2", len(bundle.Execution.CouncilTurns))
	}
	last := bundle.Execution.CouncilTurns[len(bundle.Execution.CouncilTurns)-1]
	if last.Artifact == nil || last.Artifact.CandidatePlan != nil {
		t.Fatalf("last artifact = %+v, want nil candidate plan adoption record", last.Artifact)
	}
	if bundle.Plan.Title != initialPlan.Title || bundle.Plan.Summary != initialPlan.Summary || len(bundle.Plan.Tasks) != len(initialPlan.Tasks) {
		t.Fatalf("plan = %+v, want unchanged plan %+v", bundle.Plan, initialPlan)
	}
	if bundle.Plan.Tasks[0].Title != initialPlan.Tasks[0].Title {
		t.Fatalf("plan task = %+v, want unchanged task %+v", bundle.Plan.Tasks[0], initialPlan.Tasks[0])
	}
}

func TestOnCouncilTurnCompleted_InvalidNilCandidateReportsArtifactFailure(t *testing.T) {
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
			PlanID:  "plan_council_invalid_nil",
			Lineage: "council-invalid-nil",
			Title:   "Parser cleanup",
			Summary: "Invalid nil candidate should fail parsing path.",
			State:   planStateReviewing,
			Tasks: []PlanTask{{
				ID:         "t1",
				Title:      "Normalize parser errors",
				Prompt:     "Do work",
				Complexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          newCouncilSeats(),
			CouncilTurns: []CouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
				Artifact: &adapter.CouncilTurnArtifact{
					Seat:   "A",
					Turn:   1,
					Stance: "propose",
					CandidatePlan: &adapter.PlanArtifact{
						Title: "Parser cleanup",
						Tasks: []adapter.PlanArtifactTask{{
							ID:         "t1",
							Title:      "Normalize parser errors",
							Prompt:     "Do work",
							Complexity: "medium",
						}},
					},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-invalid-nil"), "kitchen-test")
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
		Prompt:     "plan this change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-plan", "container-w-plan"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.DispatchTask(taskID, "w-plan"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	workerStateDir := pool.WorkerStateDir(pm.StateDir(), "w-plan")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	raw, err := json.Marshal(adapter.CouncilTurnArtifact{
		Seat:             "B",
		Turn:             2,
		Stance:           "revise",
		AdoptedPriorPlan: true,
		CandidatePlan:    nil,
		Summary:          "Malformed adoption.",
	})
	if err != nil {
		t.Fatalf("Marshal artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerPlanFile), raw, 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("planned\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := pm.CompleteTask("w-plan", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if err := s.onCouncilTurnCompleted(mustTask(t, pm, taskID)); err != nil {
		t.Fatalf("onCouncilTurnCompleted: %v", err)
	}

	task := mustTask(t, pm, taskID)
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued retry after invalid nil candidate", task.Status)
	}
	bundle, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if bundle.Execution.CouncilTurnsCompleted != 1 {
		t.Fatalf("council turns completed = %d, want 1", bundle.Execution.CouncilTurnsCompleted)
	}
	if len(bundle.Execution.CouncilTurns) != 1 {
		t.Fatalf("council turns = %d, want 1", len(bundle.Execution.CouncilTurns))
	}
}

func TestHandleNotification_TaskCanceledRevivesCouncilTurn(t *testing.T) {
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
			PlanID:  "plan_council_cancel_recover",
			Lineage: "council-cancel-recover",
			Title:   "Planner council cancel recovery",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			State:                 planStateReviewing,
			CouncilMaxTurns:       4,
			CouncilTurnsCompleted: 1,
			CouncilSeats:          newCouncilSeats(),
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	host := &schedulerHostAPI{}
	pm := newSchedulerPoolManagerWithHost(t, host, filepath.Join(project.PoolsDir, "sched-council-cancel-recover"), "kitchen-test")
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
		Prompt:     "plan this change",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	k := &Kitchen{pm: pm, planStore: store}
	if err := k.CancelTask(taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	s.handleNotification(pool.Notification{Type: "task_canceled", ID: taskID})

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
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
	if len(bundle.Execution.ActiveTaskIDs) != 1 || bundle.Execution.ActiveTaskIDs[0] != taskID {
		t.Fatalf("active task ids = %+v, want [%q]", bundle.Execution.ActiveTaskIDs, taskID)
	}
}
