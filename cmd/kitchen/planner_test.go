package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestKitchenValidatePlanAllowsConflictingLineage(t *testing.T) {
	// Structural validation no longer checks lineage occupancy —
	// that check moved to the activation path (activatePlanImpl).
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
	if err != nil {
		t.Fatalf("ValidatePlan should not check lineage occupancy: %v", err)
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

// --------------- DependsOn tests ---------------

func TestValidatePlanDependsOnRejectsSelfDependency(t *testing.T) {
	err := validatePlanDependsOn(PlanRecord{
		PlanID:    "plan_abc",
		DependsOn: []string{"plan_abc"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Fatalf("expected self-dependency error, got: %v", err)
	}
}

func TestValidatePlanDependsOnRejectsDuplicates(t *testing.T) {
	err := validatePlanDependsOn(PlanRecord{
		PlanID:    "plan_abc",
		DependsOn: []string{"plan_dep1", "plan_dep1"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got: %v", err)
	}
}

func TestValidatePlanDependsOnRejectsEmpty(t *testing.T) {
	err := validatePlanDependsOn(PlanRecord{
		PlanID:    "plan_abc",
		DependsOn: []string{""},
	})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected empty dep error, got: %v", err)
	}
}

func TestValidatePlanDependsOnAcceptsValid(t *testing.T) {
	err := validatePlanDependsOn(PlanRecord{
		PlanID:    "plan_abc",
		DependsOn: []string{"plan_dep1", "plan_dep2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApproveWithUnmetDepsYieldsWaiting(t *testing.T) {
	k := newTestKitchen(t)

	// Create an unmerged dependency plan.
	depPlanID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep",
			Lineage: "dep-lineage",
			Title:   "Dependency plan",
			State:   planStateActive,
			Tasks: []PlanTask{
				{ID: "t1", Title: "Work", Prompt: "do work", Complexity: ComplexityLow},
			},
		},
		Execution: ExecutionRecord{State: planStateActive},
	})
	if err != nil {
		t.Fatalf("Create dep plan: %v", err)
	}

	// Submit the dependent plan.
	bundle, err := k.SubmitIdea("Dependent work", "dependent-lineage", false, false, depPlanID)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Dependent work",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "Implement", Prompt: "Implement dependent work.",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})

	// Approve — dependency is not merged, should enter waiting state.
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateWaitingOnDependency {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStateWaitingOnDependency)
	}
	if !got.Execution.Approved {
		t.Fatal("expected Approved=true")
	}
	if got.Execution.ApprovedAt == nil {
		t.Fatal("expected ApprovedAt to be set")
	}
	if got.Execution.ActivatedAt != nil {
		t.Fatal("expected ActivatedAt to be nil")
	}
	// Must not have enqueued implementation tasks.
	runtimeID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, ok := k.pm.Task(runtimeID); ok {
		t.Fatalf("implementation task %s should not exist yet", runtimeID)
	}
	// Must not have seized the lineage.
	if activePlan, _ := k.lineageMgr.ActivePlan(got.Plan.Lineage); activePlan == bundle.Plan.PlanID {
		t.Fatal("waiting plan should not seize lineage")
	}
}

func TestApproveWithSatisfiedDepsActivatesNormally(t *testing.T) {
	k := newTestKitchen(t)

	// Create a merged dependency plan.
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep_merged",
			Lineage: "dep-lineage",
			Title:   "Dependency plan",
			State:   planStateMerged,
			Tasks: []PlanTask{
				{ID: "t1", Title: "Work", Prompt: "do work", Complexity: ComplexityLow},
			},
		},
		Execution: ExecutionRecord{State: planStateMerged},
	})
	if err != nil {
		t.Fatalf("Create dep plan: %v", err)
	}

	// Submit the dependent plan.
	bundle, err := k.SubmitIdea("Dependent work", "dependent-lineage-2", false, false, "plan_dep_merged")
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Dependent work",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "Implement", Prompt: "Implement dependent work.",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateActive {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStateActive)
	}
	if got.Execution.ActivatedAt == nil {
		t.Fatal("expected ActivatedAt to be set")
	}
	// Implementation task should be enqueued.
	runtimeID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, ok := k.pm.Task(runtimeID); !ok {
		t.Fatalf("implementation task %s not found", runtimeID)
	}
}

func TestWaitingPlanAutoActivatesAfterDependencyMerge(t *testing.T) {
	k := newTestKitchen(t)

	// Create the dependency plan (not yet merged).
	depPlanID := "plan_dep_later"
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  depPlanID,
			Lineage: "dep-lineage-3",
			Title:   "Dependency plan",
			State:   planStateCompleted,
			Tasks: []PlanTask{
				{ID: "t1", Title: "Work", Prompt: "do work", Complexity: ComplexityLow},
			},
		},
		Execution: ExecutionRecord{State: planStateCompleted},
	})
	if err != nil {
		t.Fatalf("Create dep plan: %v", err)
	}

	// Submit and approve the dependent plan — should enter waiting.
	bundle, err := k.SubmitIdea("Later dependent work", "dependent-lineage-3", false, false, depPlanID)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Later dependent work",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "Implement", Prompt: "Implement.",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateWaitingOnDependency {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStateWaitingOnDependency)
	}

	// Now mark the dependency as merged.
	depBundle, _ := k.planStore.Get(depPlanID)
	depBundle.Plan.State = planStateMerged
	depBundle.Execution.State = planStateMerged
	if err := k.planStore.UpdatePlan(depBundle.Plan); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if err := k.planStore.UpdateExecution(depPlanID, depBundle.Execution); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}

	// Trigger waiting plan recovery (simulates post-merge scan).
	k.activateWaitingPlans()

	got, err = k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan after activation: %v", err)
	}
	if got.Execution.State != planStateActive {
		t.Fatalf("state after activation = %q, want %q", got.Execution.State, planStateActive)
	}
	if got.Execution.ActivatedAt == nil {
		t.Fatal("expected ActivatedAt to be set after activation")
	}
}

func TestWaitingPlanWithBusyLineageStaysWaitingWithoutError(t *testing.T) {
	k := newTestKitchen(t)

	// Create merged dependency.
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep_busylineage",
			Lineage: "dep-lineage-4",
			Title:   "Dependency",
			State:   planStateMerged,
			Tasks:   []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{State: planStateMerged},
	})
	if err != nil {
		t.Fatalf("Create dep: %v", err)
	}

	// Occupy the lineage with another plan.
	if err := k.lineageMgr.ActivatePlan("dependent-lineage-4", "plan_blocker"); err != nil {
		t.Fatalf("ActivatePlan(blocker): %v", err)
	}

	// Create a waiting plan on the same lineage.
	waitingPlanID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:    "plan_waiting_busy",
			Lineage:   "dependent-lineage-4",
			Title:     "Waiting plan",
			DependsOn: []string{"plan_dep_busylineage"},
			State:     planStateWaitingOnDependency,
			Tasks:     []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{
			State:    planStateWaitingOnDependency,
			Approved: true,
		},
	})
	if err != nil {
		t.Fatalf("Create waiting plan: %v", err)
	}

	// Trigger recovery — should silently stay waiting (lineage busy).
	k.activateWaitingPlans()

	got, err := k.GetPlan(waitingPlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateWaitingOnDependency {
		t.Fatalf("state = %q, want %q (lineage busy)", got.Execution.State, planStateWaitingOnDependency)
	}
}

func TestStartupRecoveryReattemptsWaitingPlans(t *testing.T) {
	k := newTestKitchen(t)

	// Create merged dependency.
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep_startup",
			Lineage: "dep-startup",
			Title:   "Dependency",
			State:   planStateMerged,
			Tasks:   []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{State: planStateMerged},
	})
	if err != nil {
		t.Fatalf("Create dep: %v", err)
	}

	// Create a waiting plan.
	waitingPlanID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:    "plan_waiting_startup",
			Lineage:   "startup-lineage",
			Title:     "Startup waiting plan",
			DependsOn: []string{"plan_dep_startup"},
			State:     planStateWaitingOnDependency,
			Tasks:     []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{
			State:    planStateWaitingOnDependency,
			Approved: true,
		},
	})
	if err != nil {
		t.Fatalf("Create waiting plan: %v", err)
	}

	// Simulate scheduler startup recovery via activatePlan callback.
	gitMgr, _ := k.gitManager()
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.activatePlan = k.ApprovePlan
	s.recoverWaitingPlansOnStartup()

	got, err := k.GetPlan(waitingPlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateActive {
		t.Fatalf("state = %q, want %q after startup recovery", got.Execution.State, planStateActive)
	}
}

func TestReplanPreservesDependsOn(t *testing.T) {
	k := newTestKitchen(t)

	// Create merged dependency.
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep_replan",
			Lineage: "dep-replan",
			Title:   "Dependency",
			State:   planStateMerged,
			Tasks:   []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{State: planStateMerged},
	})
	if err != nil {
		t.Fatalf("Create dep: %v", err)
	}

	// Submit plan with dependency.
	bundle, err := k.SubmitIdea("Replan with deps", "replan-dep-lineage", false, false, "plan_dep_replan")
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Replan with deps",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "W", Prompt: "w",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})

	// Replan — should preserve DependsOn.
	newPlanID, err := k.Replan(bundle.Plan.PlanID, "needs revision")
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}

	// Replan should start a fresh planning pass while preserving plan dependencies.
	newBundle, err := k.GetPlan(newPlanID)
	if err != nil {
		t.Fatalf("GetPlan(new): %v", err)
	}
	if newBundle.Execution.State != planStatePlanning {
		t.Fatalf("execution state = %q, want %q", newBundle.Execution.State, planStatePlanning)
	}
	if len(newBundle.Plan.DependsOn) != 1 || newBundle.Plan.DependsOn[0] != "plan_dep_replan" {
		t.Fatalf("DependsOn = %+v, want [plan_dep_replan]", newBundle.Plan.DependsOn)
	}
	if len(newBundle.Plan.Tasks) != 0 {
		t.Fatalf("tasks = %+v, want no inherited task list on fresh replan", newBundle.Plan.Tasks)
	}
}

func TestReplanPreservesImplReviewRequested(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Replan with impl review", "replan-impl-review-lineage", false, true)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Replan with impl review",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "W", Prompt: "w",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})

	newPlanID, err := k.Replan(bundle.Plan.PlanID, "re-test")
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}

	newBundle, err := k.GetPlan(newPlanID)
	if err != nil {
		t.Fatalf("GetPlan(new): %v", err)
	}
	if !newBundle.Execution.ImplReviewRequested {
		t.Fatalf("execution = %+v, want impl review requested preserved", newBundle.Execution)
	}
}

func TestRequestReviewOnCompletedPlan(t *testing.T) {
	k := newTestKitchen(t)
	attachTestScheduler(t, k)
	now := time.Now().UTC()
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_manual_review_completed",
			Lineage: "manual-review-completed",
			Title:   "Manual review completed",
			State:   planStateCompleted,
			Tasks: []PlanTask{{
				ID:               "t1",
				Title:            "Implement",
				Prompt:           "implement",
				Complexity:       ComplexityMedium,
				ReviewComplexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:       planStateCompleted,
			CompletedAt: &now,
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	if err := k.RequestReview(planID); err != nil {
		t.Fatalf("RequestReview: %v", err)
	}

	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if !bundle.Execution.ImplReviewRequested {
		t.Fatalf("execution = %+v, want impl review requested", bundle.Execution)
	}
	if bundle.Execution.CompletedAt != nil {
		t.Fatalf("completedAt = %v, want cleared", bundle.Execution.CompletedAt)
	}
	if bundle.Execution.ReviewCouncilMaxTurns != 4 {
		t.Fatalf("review council max turns = %d, want 4", bundle.Execution.ReviewCouncilMaxTurns)
	}
	if bundle.Execution.ReviewCouncilCycle != 1 {
		t.Fatalf("review council cycle = %d, want 1", bundle.Execution.ReviewCouncilCycle)
	}
	task, ok := k.pm.Task(reviewCouncilTaskID(planID, 1))
	if !ok {
		t.Fatalf("expected review council task %q to be queued", reviewCouncilTaskID(planID, 1))
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}
}

func TestRequestReviewOnNonCompletedPlanReturnsError(t *testing.T) {
	k := newTestKitchen(t)
	attachTestScheduler(t, k)

	bundle, err := k.SubmitIdea("Still planning", "manual-review-invalid", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	err = k.RequestReview(bundle.Plan.PlanID)
	if err == nil {
		t.Fatal("expected RequestReview to reject non-completed plan")
	}
	if !strings.Contains(err.Error(), "invalid plan state") {
		t.Fatalf("error = %q, want invalid plan state", err)
	}
}

func TestRequestReviewOnFailedImplReviewRestartsCouncil(t *testing.T) {
	k := newTestKitchen(t)
	attachTestScheduler(t, k)
	now := time.Now().UTC()
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_manual_review_failed",
			Lineage: "manual-review-failed",
			Title:   "Manual review failed",
			State:   planStateImplementationReviewFailed,
			Tasks: []PlanTask{{
				ID:               "t1",
				Title:            "Implement",
				Prompt:           "implement",
				Complexity:       ComplexityMedium,
				ReviewComplexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReviewFailed,
			CompletedAt:                 &now,
			ImplReviewRequested:         true,
			ImplReviewStatus:            planReviewStatusFailed,
			ImplReviewFindings:          []string{"missing tests"},
			ImplReviewedAt:              &now,
			ReviewCouncilMaxTurns:       6,
			ReviewCouncilCycle:          1,
			ReviewCouncilTurnsCompleted: 5,
			ReviewCouncilFinalDecision:  reviewCouncilReject,
			RejectedBy:                  rejectedByReviewCouncil,
			AutoRemediationAttempt:      2,
			AutoRemediationActive:       true,
			AutoRemediationPlanTaskID:   "review-fix-r2",
			AutoRemediationTaskID:       planTaskRuntimeID("plan_manual_review_failed", "review-fix-r2"),
			AutoRemediationSourceTaskID: reviewCouncilTaskID("plan_manual_review_failed", 6),
			AutoRemediationSource: &AutoRemediationSourceRecord{
				Decision:     reviewCouncilConverged,
				Verdict:      pool.ReviewFail,
				Seat:         "B",
				Turn:         6,
				ReviewTaskID: reviewCouncilTaskID("plan_manual_review_failed", 6),
				Summary:      "Previous remediation loop should clear on manual review request.",
				Findings: []adapter.ReviewFinding{{
					ID:          "f1",
					Category:    "correctness",
					Description: "Manual request review should clear remediation metadata.",
					Severity:    pool.SeverityMajor,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:       reviewCouncilTaskID(planID, 1),
		PlanID:   planID,
		Prompt:   "stale prior review",
		Priority: 1,
		Role:     "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask stale review: %v", err)
	}

	if err := k.RequestReview(planID); err != nil {
		t.Fatalf("RequestReview: %v", err)
	}

	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if bundle.Execution.ImplReviewStatus != "" {
		t.Fatalf("impl review status = %q, want cleared", bundle.Execution.ImplReviewStatus)
	}
	if len(bundle.Execution.ImplReviewFindings) != 0 {
		t.Fatalf("impl review findings = %v, want cleared", bundle.Execution.ImplReviewFindings)
	}
	if bundle.Execution.ImplReviewedAt != nil {
		t.Fatalf("impl reviewed at = %v, want cleared", bundle.Execution.ImplReviewedAt)
	}
	if bundle.Execution.RejectedBy != "" {
		t.Fatalf("rejectedBy = %q, want cleared", bundle.Execution.RejectedBy)
	}
	if bundle.Execution.CompletedAt != nil {
		t.Fatalf("completedAt = %v, want cleared", bundle.Execution.CompletedAt)
	}
	if bundle.Execution.ReviewCouncilCycle != 2 {
		t.Fatalf("review council cycle = %d, want 2", bundle.Execution.ReviewCouncilCycle)
	}
	if bundle.Execution.AutoRemediationActive {
		t.Fatal("expected auto-remediation to clear on manual request review")
	}
	if bundle.Execution.AutoRemediationAttempt != 0 {
		t.Fatalf("auto remediation attempt = %d, want 0", bundle.Execution.AutoRemediationAttempt)
	}
	task, ok := k.pm.Task(reviewCouncilTaskIDForCycle(planID, 2, 1))
	if !ok {
		t.Fatalf("expected review council task %q to be queued", reviewCouncilTaskIDForCycle(planID, 2, 1))
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}
}

func TestExtendCouncilClearsAutoRemediationState(t *testing.T) {
	k := newTestKitchen(t)
	attachTestScheduler(t, k)
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_extend_clears_auto_fix",
			Lineage: "extend-clears-auto-fix",
			Title:   "Extend clears auto remediation",
			State:   planStateRejected,
		},
		Execution: ExecutionRecord{
			State:                       planStateRejected,
			ImplReviewRequested:         true,
			ImplReviewStatus:            planReviewStatusFailed,
			ReviewCouncilMaxTurns:       4,
			ReviewCouncilTurnsCompleted: 4,
			ReviewCouncilFinalDecision:  reviewCouncilReject,
			RejectedBy:                  rejectedByReviewCouncil,
			AutoRemediationAttempt:      2,
			AutoRemediationActive:       true,
			AutoRemediationPlanTaskID:   "review-fix-r2",
			AutoRemediationTaskID:       planTaskRuntimeID("plan_extend_clears_auto_fix", "review-fix-r2"),
			AutoRemediationSourceTaskID: reviewCouncilTaskID("plan_extend_clears_auto_fix", 4),
			AutoRemediationSource: &AutoRemediationSourceRecord{
				Decision:     reviewCouncilReject,
				Verdict:      pool.ReviewFail,
				Seat:         "B",
				Turn:         4,
				ReviewTaskID: reviewCouncilTaskID("plan_extend_clears_auto_fix", 4),
				Summary:      "Extension should clear remediation state.",
				Findings: []adapter.ReviewFinding{{
					ID:          "f1",
					Category:    "correctness",
					Description: "Clears remediation state on extension.",
					Severity:    pool.SeverityMajor,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	if err := k.ExtendCouncil(planID, 1); err != nil {
		t.Fatalf("ExtendCouncil: %v", err)
	}

	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Execution.AutoRemediationActive {
		t.Fatal("expected auto-remediation state to clear on review council extension")
	}
	if bundle.Execution.AutoRemediationAttempt != 0 {
		t.Fatalf("auto remediation attempt = %d, want 0", bundle.Execution.AutoRemediationAttempt)
	}
}

func TestRetryTaskPreservesAutoRemediationStateForRemediationTask(t *testing.T) {
	k := newTestKitchen(t)
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_retry_auto_fix",
			Lineage: "retry-auto-fix",
			Title:   "Retry remediation task",
			State:   planStateActive,
		},
		Execution: ExecutionRecord{
			State:                       planStateActive,
			ImplReviewRequested:         true,
			AutoRemediationAttempt:      1,
			AutoRemediationActive:       true,
			AutoRemediationPlanTaskID:   "review-fix-r1",
			AutoRemediationTaskID:       planTaskRuntimeID("plan_retry_auto_fix", "review-fix-r1"),
			AutoRemediationSourceTaskID: reviewCouncilTaskID("plan_retry_auto_fix", 2),
			AutoRemediationSource: &AutoRemediationSourceRecord{
				Decision:     reviewCouncilConverged,
				Verdict:      pool.ReviewFail,
				Seat:         "B",
				Turn:         2,
				ReviewTaskID: reviewCouncilTaskID("plan_retry_auto_fix", 2),
				Summary:      "Retry should preserve remediation metadata.",
				Findings: []adapter.ReviewFinding{{
					ID:          "f1",
					Category:    "correctness",
					Description: "Preserve remediation metadata across retry.",
					Severity:    pool.SeverityMajor,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	taskID := planTaskRuntimeID(planID, "review-fix-r1")
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:                 taskID,
		PlanID:             planID,
		Prompt:             "fix review findings",
		Complexity:         string(ComplexityMedium),
		Priority:           1,
		Role:               "implementer",
		RequireFreshWorker: true,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-fix", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-fix", "container-w-fix"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-fix"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := k.pm.FailTask("w-fix", taskID, "transient failure"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if err := k.RetryTask(taskID, true); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}

	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if !bundle.Execution.AutoRemediationActive {
		t.Fatal("expected auto-remediation to remain active after retrying the remediation task")
	}
	if bundle.Execution.AutoRemediationAttempt != 1 {
		t.Fatalf("auto remediation attempt = %d, want 1", bundle.Execution.AutoRemediationAttempt)
	}
}

func TestApproveAPIReturnsWaitingState(t *testing.T) {
	k := newTestKitchen(t)

	// Create unmerged dependency.
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep_api",
			Lineage: "dep-api",
			Title:   "Dependency",
			State:   planStateActive,
			Tasks:   []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{State: planStateActive},
	})
	if err != nil {
		t.Fatalf("Create dep: %v", err)
	}

	bundle, err := k.SubmitIdea("API waiting", "api-dep-lineage", false, false, "plan_dep_api")
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "API waiting",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "W", Prompt: "w",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	// Re-read to verify the API would return the correct state.
	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateWaitingOnDependency {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStateWaitingOnDependency)
	}
}

func TestPlanProgressRecognizesWaitingState(t *testing.T) {
	k := newTestKitchen(t)

	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:    "plan_progress_waiting",
			Lineage:   "progress-lineage",
			Title:     "Waiting plan",
			DependsOn: []string{"plan_nonexistent"},
			State:     planStateWaitingOnDependency,
			Tasks:     []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{
			State:    planStateWaitingOnDependency,
			Approved: true,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	bundle, _ := k.GetPlan("plan_progress_waiting")
	progress, err := k.planProgress(bundle)
	if err != nil {
		t.Fatalf("planProgress: %v", err)
	}
	if progress.Phase != planStateWaitingOnDependency {
		t.Fatalf("phase = %q, want %q", progress.Phase, planStateWaitingOnDependency)
	}
	if len(progress.DependsOn) != 1 || progress.DependsOn[0] != "plan_nonexistent" {
		t.Fatalf("DependsOn = %+v, want [plan_nonexistent]", progress.DependsOn)
	}

	// Verify it appears in open plans.
	open, err := k.OpenPlanProgress()
	if err != nil {
		t.Fatalf("OpenPlanProgress: %v", err)
	}
	found := false
	for _, p := range open {
		if p.PlanID == "plan_progress_waiting" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("waiting plan should appear in open plan progress")
	}
}

func TestSubmitIdeaWithDependsOnPersists(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Test deps persist", "deps-persist", false, false, "plan_a", "plan_b")
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if len(got.Plan.DependsOn) != 2 || got.Plan.DependsOn[0] != "plan_a" || got.Plan.DependsOn[1] != "plan_b" {
		t.Fatalf("DependsOn = %+v, want [plan_a, plan_b]", got.Plan.DependsOn)
	}
}

func TestApproveWaitingPlanIdempotent(t *testing.T) {
	k := newTestKitchen(t)

	// Create unmerged dependency.
	_, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_dep_idem",
			Lineage: "dep-idem",
			Title:   "Dependency",
			State:   planStateActive,
			Tasks:   []PlanTask{{ID: "t1", Title: "W", Prompt: "w", Complexity: ComplexityLow}},
		},
		Execution: ExecutionRecord{State: planStateActive},
	})
	if err != nil {
		t.Fatalf("Create dep: %v", err)
	}

	bundle, err := k.SubmitIdea("Idempotent approve", "idem-lineage", false, false, "plan_dep_idem")
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, adapter.PlanArtifact{
		Title: "Idempotent approve",
		Tasks: []adapter.PlanArtifactTask{{
			ID: "t1", Title: "W", Prompt: "w",
			Complexity: string(ComplexityMedium), ReviewComplexity: string(ComplexityMedium),
		}},
	})

	// First approve -> waiting.
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan(1): %v", err)
	}
	first, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan(first): %v", err)
	}
	// Second approve (re-entry) -> still waiting (deps still unmet), no error.
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan(2): %v", err)
	}
	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateWaitingOnDependency {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStateWaitingOnDependency)
	}
	if !got.Plan.UpdatedAt.Equal(first.Plan.UpdatedAt) {
		t.Fatalf("plan UpdatedAt changed on idempotent re-approve: first=%s second=%s", first.Plan.UpdatedAt, got.Plan.UpdatedAt)
	}
	if !got.Execution.UpdatedAt.Equal(first.Execution.UpdatedAt) {
		t.Fatalf("execution UpdatedAt changed on idempotent re-approve: first=%s second=%s", first.Execution.UpdatedAt, got.Execution.UpdatedAt)
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

func attachTestScheduler(t *testing.T, k *Kitchen) {
	t.Helper()

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.notify = k.sendNotify
	s.activatePlan = k.ApprovePlan
	k.scheduler = s
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
