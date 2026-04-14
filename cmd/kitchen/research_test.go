package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestSubmitResearch(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitResearch("How does the parser handle error recovery?")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}
	if bundle.Plan.PlanID == "" {
		t.Fatal("expected generated plan ID")
	}
	if bundle.Plan.Mode != "research" {
		t.Fatalf("mode = %q, want %q", bundle.Plan.Mode, "research")
	}
	if bundle.Plan.Lineage != "" {
		t.Fatalf("lineage = %q, want empty for research", bundle.Plan.Lineage)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if len(bundle.Execution.ActiveTaskIDs) != 1 {
		t.Fatalf("active tasks = %+v, want 1", bundle.Execution.ActiveTaskIDs)
	}

	researchTaskID := "research_" + bundle.Plan.PlanID
	if bundle.Execution.ActiveTaskIDs[0] != researchTaskID {
		t.Fatalf("active task ID = %q, want %q", bundle.Execution.ActiveTaskIDs[0], researchTaskID)
	}

	task, ok := k.pm.Task(researchTaskID)
	if !ok {
		t.Fatal("research task not found in pool")
	}
	if task.Role != researcherTaskRole {
		t.Fatalf("task role = %q, want %q", task.Role, researcherTaskRole)
	}
	if !strings.Contains(task.Prompt, "READ-ONLY") {
		t.Fatal("research prompt should mention READ-ONLY mode")
	}
}

func TestSubmitResearchEmptyTopic(t *testing.T) {
	k := newTestKitchen(t)

	_, err := k.SubmitResearch("   ")
	if err == nil {
		t.Fatal("expected error for empty topic")
	}
}

func TestResearchTaskCompletion(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitResearch("Explore the adapter pattern in the codebase")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}
	planID := bundle.Plan.PlanID
	researchTaskID := "research_" + planID

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.notify = k.sendNotify

	workerID := "researcher-" + planID
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: workerID, Role: researcherTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker(workerID, "container-"+workerID); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(researchTaskID, workerID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	// Write the research output to the worker state dir (simulates worker writing result.txt).
	workerStateDir := pool.WorkerStateDir(k.pm.StateDir(), workerID)
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	researchFindings := "<research>\nThe adapter pattern is used in pkg/adapter for provider-agnostic AI execution.\n</research>"
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(researchFindings), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := k.pm.CompleteTask(workerID, researchTaskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.onTaskCompleted(researchTaskID); err != nil {
		t.Fatalf("onTaskCompleted: %v", err)
	}

	updated, err := k.planStore.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if updated.Plan.State != planStateResearchComplete {
		t.Fatalf("plan state = %q, want %q", updated.Plan.State, planStateResearchComplete)
	}
	if updated.Execution.State != planStateResearchComplete {
		t.Fatalf("execution state = %q, want %q", updated.Execution.State, planStateResearchComplete)
	}
	if updated.Execution.ResearchOutput == "" {
		t.Fatal("expected research output to be stored")
	}
	if !strings.Contains(updated.Execution.ResearchOutput, "adapter pattern") {
		t.Fatalf("research output = %q, want to contain adapter pattern", updated.Execution.ResearchOutput)
	}
	if updated.Execution.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set")
	}
}

func TestResearchTaskFailure(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitResearch("Investigate broken pipelines")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}
	planID := bundle.Plan.PlanID
	researchTaskID := "research_" + planID

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	s := NewScheduler(k.pm, &schedulerHostAPI{}, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-test")
	s.notify = k.sendNotify

	workerID := "researcher-" + planID
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: workerID, Role: researcherTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker(workerID, "container-"+workerID); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(researchTaskID, workerID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}

	if err := k.pm.FailTask(workerID, researchTaskID, "worker crashed"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	if err := s.onTaskFailed(researchTaskID, FailureInfrastructure); err != nil {
		t.Fatalf("onTaskFailed: %v", err)
	}

	updated, err := k.planStore.Get(planID)
	if err != nil {
		t.Fatalf("Get plan: %v", err)
	}
	if updated.Plan.State != planStatePlanningFailed {
		t.Fatalf("plan state = %q, want %q", updated.Plan.State, planStatePlanningFailed)
	}
	if updated.Execution.State != planStatePlanningFailed {
		t.Fatalf("execution state = %q, want %q", updated.Execution.State, planStatePlanningFailed)
	}
}

func TestDeleteResearchPlanWithoutLineage(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitResearch("Investigate broken pipelines")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}
	researchTaskID := "research_" + bundle.Plan.PlanID

	if err := k.DeletePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	if _, err := k.planStore.Get(bundle.Plan.PlanID); err == nil {
		t.Fatal("expected deleted research plan lookup to fail")
	}
	if _, ok := k.pm.Task(researchTaskID); ok {
		t.Fatalf("task %q should be deleted", researchTaskID)
	}
}

func TestPromoteResearch(t *testing.T) {
	k := newTestKitchen(t)

	// Submit and complete a research task.
	researchBundle, err := k.SubmitResearch("Explore how errors are handled across the codebase")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}
	researchPlanID := researchBundle.Plan.PlanID

	// Manually set the plan to research_complete with output (simulates scheduler completion).
	researchBundle.Execution.State = planStateResearchComplete
	researchBundle.Execution.ResearchOutput = "The codebase uses typed errors in the parser and runtime."
	researchBundle.Plan.State = planStateResearchComplete
	if err := k.planStore.UpdatePlan(researchBundle.Plan); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if err := k.planStore.UpdateExecution(researchPlanID, researchBundle.Execution); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}

	// Promote the research into an implementation plan.
	promoted, err := k.PromoteResearch(researchPlanID, "error-handling", false, false)
	if err != nil {
		t.Fatalf("PromoteResearch: %v", err)
	}
	if promoted.Plan.PlanID == "" {
		t.Fatal("expected generated plan ID for promoted plan")
	}
	if promoted.Plan.PlanID == researchPlanID {
		t.Fatal("promoted plan should have a different ID from research plan")
	}
	if promoted.Plan.ResearchPlanID != researchPlanID {
		t.Fatalf("research plan ID = %q, want %q", promoted.Plan.ResearchPlanID, researchPlanID)
	}
	if promoted.Plan.ResearchContext == "" {
		t.Fatal("expected research context to be set on promoted plan")
	}
	if !strings.Contains(promoted.Plan.ResearchContext, "typed errors") {
		t.Fatalf("research context = %q, want to contain research findings", promoted.Plan.ResearchContext)
	}
	if promoted.Execution.State != planStatePlanning {
		t.Fatalf("promoted execution state = %q, want %q", promoted.Execution.State, planStatePlanning)
	}
	if promoted.Plan.Mode != "" {
		t.Fatalf("promoted plan mode = %q, want empty (implementation)", promoted.Plan.Mode)
	}
}

func TestPromoteNonResearchPlanFails(t *testing.T) {
	k := newTestKitchen(t)

	// Submit a normal idea.
	bundle, err := k.SubmitIdea("Introduce typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	// Try to promote it as research — should fail.
	_, err = k.PromoteResearch(bundle.Plan.PlanID, "", false, false)
	if err == nil {
		t.Fatal("expected error when promoting a non-research plan")
	}
	if !strings.Contains(err.Error(), "not a research plan") {
		t.Fatalf("error = %q, want to mention 'not a research plan'", err.Error())
	}
}

func TestPromoteResearchNotCompleteFails(t *testing.T) {
	k := newTestKitchen(t)

	// Submit research but don't complete it.
	bundle, err := k.SubmitResearch("Explore error handling")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}

	_, err = k.PromoteResearch(bundle.Plan.PlanID, "", false, false)
	if err == nil {
		t.Fatal("expected error when promoting incomplete research")
	}
	if !strings.Contains(err.Error(), "not in research_complete state") {
		t.Fatalf("error = %q, want to mention 'not in research_complete state'", err.Error())
	}
}

func TestPromoteResearchCouncilPromptIncludesResearch(t *testing.T) {
	k := newTestKitchen(t)

	// Submit and complete research.
	researchBundle, err := k.SubmitResearch("Investigate the pool manager lifecycle")
	if err != nil {
		t.Fatalf("SubmitResearch: %v", err)
	}
	researchPlanID := researchBundle.Plan.PlanID

	researchBundle.Execution.State = planStateResearchComplete
	researchBundle.Execution.ResearchOutput = "Pool manager uses WAL for crash recovery and supports task pipelines."
	researchBundle.Plan.State = planStateResearchComplete
	if err := k.planStore.UpdatePlan(researchBundle.Plan); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if err := k.planStore.UpdateExecution(researchPlanID, researchBundle.Execution); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}

	promoted, err := k.PromoteResearch(researchPlanID, "pool-lifecycle", false, false)
	if err != nil {
		t.Fatalf("PromoteResearch: %v", err)
	}

	// Build the council prompt for the promoted plan and check that research context is included.
	bundle, err := k.planStore.Get(promoted.Plan.PlanID)
	if err != nil {
		t.Fatalf("Get promoted plan: %v", err)
	}

	prompt, err := buildCouncilTurnPrompt(bundle, 1)
	if err != nil {
		t.Fatalf("buildCouncilTurnPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Prior Research") {
		t.Fatal("council prompt should contain 'Prior Research' section")
	}
	if !strings.Contains(prompt, "WAL for crash recovery") {
		t.Fatal("council prompt should contain the actual research findings")
	}
}

func TestBuildCouncilTurnPromptWithoutResearchContext(t *testing.T) {
	// Verify that a normal plan (no research context) doesn't include the Prior Research section.
	prompt := adapter.BuildCouncilTurnPrompt("Build a parser", nil, "A", 1, "Build a parser")
	if strings.Contains(prompt, "Prior Research") {
		t.Fatal("prompt without research context should not contain 'Prior Research' section")
	}
}

func TestBuildCouncilTurnPromptWithResearchContext(t *testing.T) {
	prompt := adapter.BuildCouncilTurnPrompt("Build a parser", nil, "A", 1, "Build a parser", "The existing parser uses recursive descent.")
	if !strings.Contains(prompt, "Prior Research") {
		t.Fatal("prompt with research context should contain 'Prior Research' section")
	}
	if !strings.Contains(prompt, "recursive descent") {
		t.Fatal("prompt should contain the research findings text")
	}
}
