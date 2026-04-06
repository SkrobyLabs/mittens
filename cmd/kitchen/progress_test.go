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
