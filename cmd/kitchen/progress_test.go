package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestPlanPhase_ImplementationReview(t *testing.T) {
	bundle := StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_phase_ir",
		},
		Execution: ExecutionRecord{
			State: planStateImplementationReview,
		},
	}
	phase := planPhase(bundle, 0)
	if phase != "reviewing_implementation" {
		t.Fatalf("planPhase = %q, want %q", phase, "reviewing_implementation")
	}
}

func TestPlanPhase_AutoRemediationImplementationReview(t *testing.T) {
	bundle := StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_phase_auto_fix",
		},
		Execution: ExecutionRecord{
			State:                  planStateActive,
			AutoRemediationActive:  true,
			AutoRemediationAttempt: 1,
		},
	}
	phase := planPhase(bundle, 0)
	if phase != "auto_remediating_implementation_review" {
		t.Fatalf("planPhase = %q, want %q", phase, "auto_remediating_implementation_review")
	}
}

func TestOpenPlanProgressWithLimitIncludesExtendableRejectedReviewCouncilPlans(t *testing.T) {
	k := newTestKitchen(t)
	if _, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_extendable_reject",
			Lineage: "extendable-reject",
			Title:   "Extendable reject",
			State:   planStateRejected,
		},
		Execution: ExecutionRecord{
			State:                       planStateRejected,
			ImplReviewRequested:         true,
			RejectedBy:                  rejectedByReviewCouncil,
			ReviewCouncilMaxTurns:       2,
			ReviewCouncilTurnsCompleted: 2,
			ReviewCouncilFinalDecision:  reviewCouncilReject,
		},
	}); err != nil {
		t.Fatalf("Create extendable rejected plan: %v", err)
	}
	if _, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_hidden_reject",
			Lineage: "hidden-reject",
			Title:   "Hidden reject",
			State:   planStateRejected,
		},
		Execution: ExecutionRecord{
			State:                       planStateRejected,
			ImplReviewRequested:         true,
			RejectedBy:                  rejectedByReviewCouncil,
			ReviewCouncilMaxTurns:       ReviewCouncilHardCap,
			ReviewCouncilTurnsCompleted: ReviewCouncilHardCap,
			ReviewCouncilFinalDecision:  reviewCouncilReject,
		},
	}); err != nil {
		t.Fatalf("Create hidden rejected plan: %v", err)
	}

	progress, err := k.OpenPlanProgressWithLimit(10)
	if err != nil {
		t.Fatalf("OpenPlanProgressWithLimit: %v", err)
	}
	if len(progress) != 1 {
		t.Fatalf("progress count = %d, want 1 visible rejected plan", len(progress))
	}
	if progress[0].PlanID != "plan_extendable_reject" {
		t.Fatalf("visible plan = %q, want extendable rejected plan", progress[0].PlanID)
	}
}

func TestCanExtendReviewCouncilAllowsFailedImplementationReviewState(t *testing.T) {
	exec := ExecutionRecord{
		ImplReviewRequested:         true,
		ImplReviewStatus:            planReviewStatusFailed,
		RejectedBy:                  rejectedByReviewCouncil,
		ReviewCouncilMaxTurns:       4,
		ReviewCouncilTurnsCompleted: 4,
		ReviewCouncilFinalDecision:  reviewCouncilReject,
	}
	if !canExtendReviewCouncil(planStateImplementationReviewFailed, exec) {
		t.Fatal("expected failed implementation review state to remain extendable after review-council rejection")
	}
}

func TestPlanCyclesIncludeFailedImplementationReviewTurn(t *testing.T) {
	k := newTestKitchen(t)
	planID := "plan_failed_review_cycle"
	reviewTaskID := reviewCouncilTaskID(planID, 2)
	if _, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  planID,
			Lineage: "failed-review-cycle",
			Title:   "Failed review cycle",
			State:   planStateImplementationReviewFailed,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReviewFailed,
			ImplReviewRequested:         true,
			ImplReviewStatus:            planReviewStatusFailed,
			ReviewCouncilMaxTurns:       4,
			ReviewCouncilTurnsCompleted: 1,
			FailedTaskIDs:               []string{reviewTaskID},
		},
	}); err != nil {
		t.Fatalf("Create failed review plan: %v", err)
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         reviewTaskID,
		PlanID:     planID,
		Prompt:     "review failure",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask review: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-progress", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-progress", "container-w-progress"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(reviewTaskID, "w-progress"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := k.pm.FailTask("w-progress", reviewTaskID, "adapter exited with code 1"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	detail, err := k.PlanDetail(planID)
	if err != nil {
		t.Fatalf("PlanDetail: %v", err)
	}
	if len(detail.Progress.Cycles) < 2 {
		t.Fatalf("cycles = %+v, want implementation review turn 2 included", detail.Progress.Cycles)
	}
	last := detail.Progress.Cycles[len(detail.Progress.Cycles)-1]
	if last.ImplReviewTaskID != reviewTaskID {
		t.Fatalf("last impl review task = %q, want %q", last.ImplReviewTaskID, reviewTaskID)
	}
	if last.ImplReviewTaskState != pool.TaskFailed {
		t.Fatalf("last impl review task state = %q, want %q", last.ImplReviewTaskState, pool.TaskFailed)
	}
}
