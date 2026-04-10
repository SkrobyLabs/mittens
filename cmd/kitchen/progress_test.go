package main

import (
	"testing"
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
