package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestSanitizeLineageSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "already valid", in: "kitchen-tui-questions", want: "kitchen-tui-questions"},
		{name: "forward slashes collapsed", in: "feat/kitchen-headless", want: "feat-kitchen-headless"},
		{name: "multi-segment branch", in: "kitchen/kitchen-worktree-gone", want: "kitchen-kitchen-worktree-gone"},
		{name: "backslashes collapsed", in: `feat\nested\work`, want: "feat-nested-work"},
		{name: "uppercase lowercased", in: "Kitchen/TUI-Questions", want: "kitchen-tui-questions"},
		{name: "whitespace trimmed and replaced", in: "  my  lineage  ", want: "my-lineage"},
		{name: "leading/trailing dots stripped", in: "..rooted..", want: "rooted"},
		{name: "lone dot rejected", in: ".", want: ""},
		{name: "double dot rejected", in: "..", want: ""},
		{name: "empty stays empty", in: "", want: ""},
		{name: "truncated at 48 chars", in: strings.Repeat("a", 80), want: strings.Repeat("a", 48)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLineageSlug(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeLineageSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPlanFromArtifactSanitizesLineage(t *testing.T) {
	existing := PlanRecord{
		PlanID:  "plan_1",
		Lineage: "original-lineage",
		Title:   "Existing",
	}
	artifact := &adapter.PlanArtifact{
		Lineage: "feat/kitchen-headless",
		Title:   "Updated",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "Work", Prompt: "do", Complexity: "low",
		}},
	}
	planned := planFromArtifact(existing, artifact)
	if planned.Lineage != "feat-kitchen-headless" {
		t.Fatalf("planned.Lineage = %q, want feat-kitchen-headless (slashes collapsed)", planned.Lineage)
	}
}

func TestPlanFromArtifactKeepsExistingLineageWhenArtifactSlugUnusable(t *testing.T) {
	existing := PlanRecord{
		PlanID:  "plan_1",
		Lineage: "original-lineage",
		Title:   "Existing",
	}
	artifact := &adapter.PlanArtifact{
		Lineage: "..",
		Title:   "Updated",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "Work", Prompt: "do", Complexity: "low",
		}},
	}
	planned := planFromArtifact(existing, artifact)
	if planned.Lineage != "original-lineage" {
		t.Fatalf("planned.Lineage = %q, want original-lineage preserved", planned.Lineage)
	}
}

func TestKitchenSubmitIdeaAndApprovePlan(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	if bundle.Plan.PlanID == "" {
		t.Fatal("expected generated plan ID")
	}
	if bundle.Plan.Lineage == "" {
		t.Fatal("expected generated lineage")
	}
	if bundle.Execution.State != planStatePlanning {
		t.Fatalf("state = %q, want %q", bundle.Execution.State, planStatePlanning)
	}
	if len(bundle.Plan.Tasks) != 0 {
		t.Fatalf("tasks = %+v, want planner-generated tasks later", bundle.Plan.Tasks)
	}
	task, ok := k.pm.Task(councilTaskID(bundle.Plan.PlanID, 1))
	if !ok || task.Role != plannerTaskRole {
		t.Fatalf("planner task = %+v, want queued planner task", task)
	}

	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title:   "Typed parser errors",
		Summary: "Break parser error cleanup into one worker task.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement typed parser errors",
			Prompt:           "Introduce typed parser errors for lexer failures.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	})

	planned, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(planned): %v", err)
	}
	if planned.Execution.State != planStatePendingApproval {
		t.Fatalf("planned state = %q, want %q", planned.Execution.State, planStatePendingApproval)
	}
	if len(planned.Plan.Tasks) != 1 {
		t.Fatalf("planned tasks = %+v, want 1 implementation task", planned.Plan.Tasks)
	}
	planned.Plan.Tasks[0].TimeoutMinutes = 15
	if err := k.planStore.UpdatePlan(planned.Plan); err != nil {
		t.Fatalf("UpdatePlan(timeout): %v", err)
	}

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	approved, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if approved.Execution.State != planStateActive {
		t.Fatalf("approved state = %q, want %q", approved.Execution.State, planStateActive)
	}
	if activePlan, err := k.lineageMgr.ActivePlan(approved.Plan.Lineage); err != nil || activePlan != approved.Plan.PlanID {
		t.Fatalf("active plan = %q, %v; want %q", activePlan, err, approved.Plan.PlanID)
	}

	tasks := k.pm.Tasks()
	var implementationTask *pool.Task
	for i := range tasks {
		if tasks[i].ID == planTaskRuntimeID(approved.Plan.PlanID, "t1") {
			implementationTask = &tasks[i]
			break
		}
	}
	if implementationTask == nil {
		t.Fatalf("implementation task %q not found in %+v", planTaskRuntimeID(approved.Plan.PlanID, "t1"), tasks)
	}
	if implementationTask.PlanID != approved.Plan.PlanID {
		t.Fatalf("task plan ID = %q, want %q", implementationTask.PlanID, approved.Plan.PlanID)
	}
	if implementationTask.TimeoutMinutes != 15 {
		t.Fatalf("task timeoutMinutes = %d, want 15", implementationTask.TimeoutMinutes)
	}
}

func TestKitchenSubmitIdeaWithImplReviewPersistsFlag(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, true)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if !got.Execution.ImplReviewRequested {
		t.Fatal("expected impl review to be requested")
	}
	if got.Execution.CouncilMaxTurns != 4 {
		t.Fatalf("council max turns = %d, want 4", got.Execution.CouncilMaxTurns)
	}
	if len(got.Execution.ActiveTaskIDs) != 1 || got.Execution.ActiveTaskIDs[0] != councilTaskID(bundle.Plan.PlanID, 1) {
		t.Fatalf("active task ids = %+v, want initial council task", got.Execution.ActiveTaskIDs)
	}
}

func TestKitchenPlannerQuestionsBlockApprovalUntilAnswered(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Typed parser errors",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement typed parser errors",
			Prompt:           "Introduce typed parser errors for lexer failures.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
		Questions: []adapter.PlanArtifactQuestion{{
			Question: "Should lexer and parser errors share one exported type?",
			Context:  "This changes the public API shape and rollout scope.",
		}},
	})

	questions := k.ListQuestions()
	if len(questions) != 1 {
		t.Fatalf("questions = %+v, want 1", questions)
	}
	if questions[0].Category != "council" {
		t.Fatalf("category = %q, want council", questions[0].Category)
	}
	if questions[0].Context != "This changes the public API shape and rollout scope." {
		t.Fatalf("context = %q", questions[0].Context)
	}

	if err := k.ApprovePlan(bundle.Plan.PlanID); err == nil || !strings.Contains(err.Error(), "still under review") {
		t.Fatalf("ApprovePlan err = %v, want under review failure", err)
	}

	if err := k.AnswerQuestion(questions[0].ID, "Keep one exported type for now."); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Typed parser errors",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement typed parser errors",
			Prompt:           "Introduce typed parser errors for lexer failures.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	})
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan after answer: %v", err)
	}
}

func TestCancelTaskPreservesReviewingCouncilState(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completeCouncilTurnWithArtifact(t, k, bundle.Plan.PlanID, councilTaskID(bundle.Plan.PlanID, 1), adapter.CouncilTurnArtifact{
		Seat:    "A",
		Turn:    1,
		Stance:  "revise",
		Summary: "Needs one more planning turn.",
		CandidatePlan: &adapter.PlanArtifact{
			Title:   "Typed parser errors",
			Summary: "Refine the plan in a second council turn.",
			Tasks: []adapter.PlanArtifactTask{{
				ID:               "t1",
				Title:            "Implement typed parser errors",
				Prompt:           "Introduce typed parser errors for lexer failures.",
				Complexity:       string(ComplexityMedium),
				ReviewComplexity: string(ComplexityMedium),
			}},
		},
	})

	taskID := councilTaskID(bundle.Plan.PlanID, 2)
	if err := k.CancelTask(taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Plan.State != planStateReviewing {
		t.Fatalf("plan state = %q, want %q", got.Plan.State, planStateReviewing)
	}
	if got.Execution.State != planStateReviewing {
		t.Fatalf("execution state = %q, want %q", got.Execution.State, planStateReviewing)
	}
	if len(got.Execution.ActiveTaskIDs) != 0 {
		t.Fatalf("active task ids = %+v, want none", got.Execution.ActiveTaskIDs)
	}
	if got.Execution.CouncilTurnsCompleted != 1 {
		t.Fatalf("council turns completed = %d, want 1", got.Execution.CouncilTurnsCompleted)
	}
	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskCanceled {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskCanceled)
	}
}

func TestKitchenAutoApprovedPlanResumesAfterPlannerQuestionsAnswered(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", true, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Typed parser errors",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement typed parser errors",
			Prompt:           "Introduce typed parser errors for lexer failures.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
		Questions: []adapter.PlanArtifactQuestion{{
			Question: "Should lexer and parser errors share one exported type?",
		}},
	})

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateReviewing {
		t.Fatalf("state = %q, want %q before answer", got.Execution.State, planStateReviewing)
	}

	questions := k.ListQuestions()
	if len(questions) != 1 {
		t.Fatalf("questions = %+v, want 1", questions)
	}
	if err := k.AnswerQuestion(questions[0].ID, "Keep one exported type for now."); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Typed parser errors",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement typed parser errors",
			Prompt:           "Introduce typed parser errors for lexer failures.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	})

	got, err = k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(after answer): %v", err)
	}
	if got.Execution.State != planStateActive {
		t.Fatalf("state = %q, want %q after auto-approve", got.Execution.State, planStateActive)
	}
}

func TestKitchenCancelActivePlanCancelsRuntimeTasks(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Typed parser errors",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement typed parser errors",
			Prompt:           "Introduce typed parser errors for lexer failures.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	})
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if err := k.pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	if err := k.CancelPlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("CancelPlan: %v", err)
	}

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Plan.State != planStateClosed {
		t.Fatalf("plan state = %q, want %q", got.Plan.State, planStateClosed)
	}
	if got.Execution.State != "cancelled" {
		t.Fatalf("execution state = %q, want cancelled", got.Execution.State)
	}
	if got.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set")
	}
	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %s not found", taskID)
	}
	if task.Status != pool.TaskCanceled {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskCanceled)
	}
	worker, ok := k.pm.Worker("w-1")
	if !ok {
		t.Fatal("worker w-1 not found")
	}
	if worker.Status != pool.WorkerIdle {
		t.Fatalf("worker status = %q, want %q", worker.Status, pool.WorkerIdle)
	}
	if activePlan, err := k.lineageMgr.ActivePlan(bundle.Plan.Lineage); err == nil || activePlan != "" {
		t.Fatalf("active plan = %q, %v; want cleared", activePlan, err)
	}
}

func TestKitchenValidatePlanRejectsConflictingActiveLineage(t *testing.T) {
	k := newTestKitchen(t)
	if err := k.lineageMgr.ActivatePlan("parser-errors", "plan_existing"); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}

	err := k.ValidatePlan(PlanRecord{
		Lineage: "parser-errors",
		Title:   "Parser error cleanup",
		Tasks: []PlanTask{
			{
				ID:         "t1",
				Title:      "Do work",
				Prompt:     "Do work",
				Complexity: ComplexityLow,
			},
		},
	})
	if err == nil {
		t.Fatal("expected active lineage conflict")
	}
}

func TestKitchenQuestionAnswerAndUnhelpfulFlow(t *testing.T) {
	k := newTestKitchen(t)

	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_questions",
			Lineage: "parser-errors",
			Title:   "Parser question flow",
			Tasks: []PlanTask{
				{
					ID:         "t1",
					Title:      "Implement",
					Prompt:     "Implement",
					Complexity: ComplexityLow,
				},
			},
		},
		Affinity: AffinityRecord{
			PlannerWorkerID:    "w-planner-1",
			PreferredProviders: []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	taskID, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "Implement",
		Complexity: string(ComplexityLow),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	questionID, err := k.RouteQuestion("w-1", taskID, "Need clarification")
	if err != nil {
		t.Fatalf("RouteQuestion: %v", err)
	}
	if err := k.AnswerQuestion(questionID, "Use typed errors"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	question := k.pm.GetQuestion(questionID)
	if question == nil || !question.Answered || question.Answer != "Use typed errors" {
		t.Fatalf("question = %+v, want answered question", question)
	}

	if err := k.MarkUnhelpful(questionID); err != nil {
		t.Fatalf("MarkUnhelpful: %v", err)
	}

	bundle, err := k.planStore.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if !bundle.Affinity.Invalidated {
		t.Fatal("expected affinity invalidation")
	}
	if bundle.Affinity.PlannerWorkerID != "" {
		t.Fatalf("planner worker = %q, want cleared", bundle.Affinity.PlannerWorkerID)
	}
	if len(bundle.Affinity.PreferredProviders) != 0 {
		t.Fatalf("preferred providers = %+v, want cleared", bundle.Affinity.PreferredProviders)
	}
	if bundle.Affinity.LastQuestionID != questionID {
		t.Fatalf("last question = %q, want %q", bundle.Affinity.LastQuestionID, questionID)
	}
}

func newTestKitchen(t *testing.T) *Kitchen {
	t.Helper()

	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(project.PoolsDir, defaultPoolStateName)
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	wal, err := pool.OpenWAL(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wal.Close() })

	pm := pool.NewPoolManager(pool.PoolConfig{
		SessionID:  "kitchen-test",
		MaxWorkers: 8,
		StateDir:   stateDir,
	}, wal, nil)

	health, err := NewProviderHealth(filepath.Join(project.RootDir, "provider_health.json"))
	if err != nil {
		t.Fatal(err)
	}

	return &Kitchen{
		pm:         pm,
		wal:        wal,
		router:     NewComplexityRouter(DefaultKitchenConfig(), health),
		health:     health,
		planStore:  NewPlanStore(project.PlansDir),
		lineageMgr: NewLineageManager(project.LineagesDir, project.PlansDir),
		cfg:        DefaultKitchenConfig(),
		repoPath:   repo,
		paths:      paths,
		project:    project,
	}
}

func completePlanningTask(t *testing.T, k *Kitchen, planID string, artifact adapter.PlanArtifact) {
	t.Helper()

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.notify = k.sendNotify
	s.activatePlan = k.ApprovePlan

	taskID := currentPlanControlTaskID(t, k, planID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole
	})
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
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("planned\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	currentTask, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("planner task %q not found", taskID)
	}
	raw, err := json.Marshal(testCouncilArtifactForTask(*currentTask, artifact))
	if err != nil {
		t.Fatalf("Marshal council artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerPlanFile), raw, 0o644); err != nil {
		t.Fatalf("WriteFile plan: %v", err)
	}
	if err := k.pm.CompleteTask(workerID, taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.onTaskCompleted(taskID); err != nil {
		t.Fatalf("onTaskCompleted: %v", err)
	}
	for i := 0; i < 4; i++ {
		bundle, err := k.GetPlan(planID)
		if err != nil {
			t.Fatalf("GetPlan(after council turn): %v", err)
		}
		if bundle.Execution.CouncilAwaitingAnswers ||
			bundle.Execution.State == planStatePendingApproval ||
			bundle.Execution.State == planStateActive ||
			bundle.Execution.State == planStateRejected {
			return
		}
		taskID = currentPlanControlTaskID(t, k, planID, func(task pool.Task) bool {
			return task.Role == plannerTaskRole
		})
		currentTask, ok = k.pm.Task(taskID)
		if !ok {
			t.Fatalf("planner task %q not found", taskID)
		}
		workerID = "planner-" + planID + "-" + currentTask.ID
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
		workerStateDir = pool.WorkerStateDir(k.pm.StateDir(), workerID)
		if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
			t.Fatalf("MkdirAll worker state: %v", err)
		}
		raw, err = json.Marshal(testCouncilArtifactForTask(*currentTask, artifact))
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
	t.Fatalf("council planning did not reach a terminal planning state for plan %s", planID)
}

func basicPlannedArtifact(title string) adapter.PlanArtifact {
	return adapter.PlanArtifact{
		Title:   title,
		Summary: title,
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            title,
			Prompt:           "Implement " + title + " in this repository.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	}
}

func testCouncilArtifactForTask(task pool.Task, artifact adapter.PlanArtifact) adapter.CouncilTurnArtifact {
	turn := councilTurnNumberFromTaskID(task.PlanID, task.ID)
	questions := make([]adapter.CouncilUserQuestion, 0, len(artifact.Questions))
	for i, item := range artifact.Questions {
		questions = append(questions, adapter.CouncilUserQuestion{
			ID:           fmt.Sprintf("q%d", i+1),
			Question:     item.Question,
			WhyItMatters: item.Context,
			Blocking:     true,
		})
	}
	stance := "propose"
	adopted := false
	if turn >= 2 && len(questions) == 0 {
		stance = "converged"
		adopted = true
	}
	planCopy := artifact
	return adapter.CouncilTurnArtifact{
		Seat:             councilSeatForTurn(turn),
		Turn:             turn,
		Stance:           stance,
		CandidatePlan:    &planCopy,
		AdoptedPriorPlan: adopted,
		QuestionsForUser: questions,
		SeatMemo:         firstNonEmpty(artifact.Summary, artifact.Title),
		Summary:          firstNonEmpty(artifact.Summary, artifact.Title),
	}
}

func currentPlanControlTaskID(t *testing.T, k *Kitchen, planID string, match func(pool.Task) bool) string {
	t.Helper()
	bundle, err := k.GetPlan(planID)
	if err == nil {
		for _, taskID := range bundle.Execution.ActiveTaskIDs {
			task, ok := k.pm.Task(taskID)
			if ok && task.PlanID == planID && match(*task) {
				return taskID
			}
		}
	}
	for _, task := range k.pm.Tasks() {
		if task.PlanID != planID || !match(task) {
			continue
		}
		switch task.Status {
		case pool.TaskQueued, pool.TaskDispatched:
			return task.ID
		}
	}
	t.Fatalf("no matching active control task found for plan %s", planID)
	return ""
}
