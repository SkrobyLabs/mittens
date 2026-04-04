package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestKitchenSubmitIdeaAndApprovePlan(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false, 0, -1)
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
	task, ok := k.pm.Task(planTaskRuntimeID(bundle.Plan.PlanID, plannerTaskID))
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
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2 (planner + implementation)", len(tasks))
	}
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

func TestKitchenSubmitIdeaWithReviewPersistsReviewMetadata(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, true, 2, 3)
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
	completePlanReviewTask(t, k, bundle.Plan.PlanID, pool.ReviewPass, "Plan decomposition looks good.", pool.SeverityMinor)
	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if !got.Execution.ReviewRequested {
		t.Fatal("expected reviewRequested")
	}
	if got.Execution.ReviewRounds != 2 {
		t.Fatalf("review rounds = %d, want 2", got.Execution.ReviewRounds)
	}
	if got.Execution.MaxReviewRevisions != 3 {
		t.Fatalf("max review revisions = %d, want 3", got.Execution.MaxReviewRevisions)
	}
	if got.Execution.ReviewStatus != planReviewStatusPassed {
		t.Fatalf("review status = %q, want %q", got.Execution.ReviewStatus, planReviewStatusPassed)
	}
	if got.Execution.ReviewedAt == nil {
		t.Fatal("expected reviewedAt to be set")
	}
	if len(got.Execution.ReviewFindings) == 0 {
		t.Fatal("expected review findings")
	}
}

func TestKitchenPlannerQuestionsBlockApprovalUntilAnswered(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false, 0, -1)
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
	if questions[0].Category != "planning" {
		t.Fatalf("category = %q, want planning", questions[0].Category)
	}
	if questions[0].Context != "This changes the public API shape and rollout scope." {
		t.Fatalf("context = %q", questions[0].Context)
	}

	if err := k.ApprovePlan(bundle.Plan.PlanID); err == nil || !strings.Contains(err.Error(), "pending questions") {
		t.Fatalf("ApprovePlan err = %v, want pending questions failure", err)
	}

	if err := k.AnswerQuestion(questions[0].ID, "Keep one exported type for now."); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan after answer: %v", err)
	}
}

func TestKitchenAutoApprovedPlanResumesAfterPlannerQuestionsAnswered(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", true, false, 0, -1)
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
	if got.Execution.State != planStatePendingApproval {
		t.Fatalf("state = %q, want %q before answer", got.Execution.State, planStatePendingApproval)
	}

	questions := k.ListQuestions()
	if len(questions) != 1 {
		t.Fatalf("questions = %+v, want 1", questions)
	}
	if err := k.AnswerQuestion(questions[0].ID, "Keep one exported type for now."); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	got, err = k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(after answer): %v", err)
	}
	if got.Execution.State != planStateActive {
		t.Fatalf("state = %q, want %q after auto-approve", got.Execution.State, planStateActive)
	}
}

func TestKitchenAutoApprovedReviewedPlanActivatesAfterReviewPass(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", true, true, 2, -1)
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

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateReviewing {
		t.Fatalf("state = %q, want %q before review completion", got.Execution.State, planStateReviewing)
	}

	completePlanReviewTask(t, k, bundle.Plan.PlanID, pool.ReviewPass, "Plan decomposition looks good.", pool.SeverityMinor)

	got, err = k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(after review): %v", err)
	}
	if got.Execution.State != planStateActive {
		t.Fatalf("state = %q, want %q after auto approval", got.Execution.State, planStateActive)
	}
	if got.Execution.ReviewStatus != planReviewStatusPassed {
		t.Fatalf("review status = %q, want %q", got.Execution.ReviewStatus, planReviewStatusPassed)
	}
}

func TestKitchenFailedPlanReviewQueuesSinglePlannerRevision(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, true, 1, -1)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Typed parser errors"))

	firstReviewTaskID := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, isPlanReviewTask)
	completePlanReviewTask(t, k, bundle.Plan.PlanID, pool.ReviewFail, "Split the work into smaller tasks.", pool.SeverityMajor)

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(after failed review): %v", err)
	}
	if got.Execution.State != planStatePlanning {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStatePlanning)
	}
	if got.Execution.ReviewStatus != planReviewStatusFailed {
		t.Fatalf("review status = %q, want %q", got.Execution.ReviewStatus, planReviewStatusFailed)
	}
	if got.Execution.ReviewRevisions != 1 {
		t.Fatalf("review revisions = %d, want 1", got.Execution.ReviewRevisions)
	}
	if len(got.Execution.History) < 4 {
		t.Fatalf("history = %+v, want planning/review timeline", got.Execution.History)
	}
	last := got.Execution.History[len(got.Execution.History)-1]
	if last.Type != planHistoryPlanningStarted || last.Cycle != 2 {
		t.Fatalf("last history entry = %+v, want cycle 2 planning start", last)
	}
	revisionTaskID := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole && !isPlanReviewTask(task)
	})
	if !strings.Contains(revisionTaskID, planRevisionTaskID+"-1") {
		t.Fatalf("revision task ID = %q, want %q suffix", revisionTaskID, planRevisionTaskID+"-1")
	}

	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Typed parser errors v2",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Add typed parser errors",
			Prompt:           "Introduce typed parser errors and update callers.",
			Complexity:       string(ComplexityLow),
			ReviewComplexity: string(ComplexityLow),
		}, {
			ID:               "t2",
			Title:            "Update callers",
			Prompt:           "Update callers to use typed parser errors.",
			Complexity:       string(ComplexityMedium),
			Dependencies:     []string{"t1"},
			ReviewComplexity: string(ComplexityMedium),
		}},
	})

	got, err = k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(after revision planning): %v", err)
	}
	if got.Execution.State != planStateReviewing {
		t.Fatalf("state = %q, want %q after revision plan", got.Execution.State, planStateReviewing)
	}
	secondReviewTaskID := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, isPlanReviewTask)
	if secondReviewTaskID == firstReviewTaskID {
		t.Fatalf("review task ID reused: %q", secondReviewTaskID)
	}
	if !strings.Contains(secondReviewTaskID, planReviewTaskID+"-2") {
		t.Fatalf("second review task ID = %q, want %q suffix", secondReviewTaskID, planReviewTaskID+"-2")
	}
	history := got.Execution.History
	if history[0].Type != planHistoryPlanningStarted || history[0].Cycle != 1 {
		t.Fatalf("first history entry = %+v, want initial planning start", history[0])
	}
	if history[1].Type != planHistoryPlanningCompleted || history[1].Cycle != 1 {
		t.Fatalf("second history entry = %+v, want initial planning completion", history[1])
	}
	if history[2].Type != planHistoryReviewRequested || history[2].Cycle != 1 {
		t.Fatalf("third history entry = %+v, want initial review request", history[2])
	}
	if history[3].Type != planHistoryReviewFailed || history[3].Cycle != 1 {
		t.Fatalf("fourth history entry = %+v, want failed first review", history[3])
	}
	if len(history[3].Findings) == 0 || !strings.Contains(history[3].Findings[0], "Severity:") {
		t.Fatalf("failed review history findings = %+v, want persisted findings", history[3].Findings)
	}
}

func TestKitchenReviewWithZeroMaxRevisionsStopsAfterFailedReview(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, true, 1, 0)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Typed parser errors"))
	reviewTaskID := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, isPlanReviewTask)
	completePlanReviewTask(t, k, bundle.Plan.PlanID, pool.ReviewFail, "Split the work into smaller tasks.", pool.SeverityMajor)

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(after failed review): %v", err)
	}
	if got.Execution.State != planStatePendingApproval {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStatePendingApproval)
	}
	if got.Execution.ReviewRevisions != 0 {
		t.Fatalf("review revisions = %d, want 0", got.Execution.ReviewRevisions)
	}
	if len(got.Execution.FailedTaskIDs) != 0 {
		t.Fatalf("failed task IDs = %+v, want none", got.Execution.FailedTaskIDs)
	}
	for _, task := range k.pm.Tasks() {
		if task.PlanID != bundle.Plan.PlanID || task.ID == reviewTaskID {
			continue
		}
		if task.Role == plannerTaskRole && !isPlanReviewTask(task) && (task.Status == pool.TaskQueued || task.Status == pool.TaskDispatched) {
			t.Fatalf("unexpected revision task queued: %+v", task)
		}
	}
}

func TestKitchenCancelActivePlanCancelsRuntimeTasks(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false, 0, -1)
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
		return task.Role == plannerTaskRole && !isPlanReviewTask(task)
	})
	workerID := "planner-" + planID
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
	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("Marshal planner artifact: %v", err)
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
}

func completePlanReviewTask(t *testing.T, k *Kitchen, planID, verdict, feedback, severity string) {
	t.Helper()

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.notify = k.sendNotify
	s.activatePlan = k.ApprovePlan

	taskID := currentPlanControlTaskID(t, k, planID, isPlanReviewTask)
	workerID := "reviewer-" + planID
	if _, ok := k.pm.Worker(workerID); !ok {
		if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: workerID, Role: "reviewer"}); err != nil {
			t.Fatalf("SpawnWorker: %v", err)
		}
		if err := k.pm.RegisterWorker(workerID, "container-"+workerID); err != nil {
			t.Fatalf("RegisterWorker: %v", err)
		}
	}
	if err := k.pm.DispatchTask(taskID, workerID); err != nil {
		t.Fatalf("DispatchTask(review): %v", err)
	}

	workerStateDir := pool.WorkerStateDir(k.pm.StateDir(), workerID)
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	output := "<review><verdict>" + verdict + "</verdict><feedback>" + feedback + "</feedback><severity>" + severity + "</severity></review>"
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(output), 0o644); err != nil {
		t.Fatalf("WriteFile review result: %v", err)
	}
	if err := k.pm.CompleteTask(workerID, taskID); err != nil {
		t.Fatalf("CompleteTask(review): %v", err)
	}
	if err := s.onTaskCompleted(taskID); err != nil {
		t.Fatalf("onTaskCompleted(review): %v", err)
	}
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
