package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestProjectPlan_NormalizesClosedCancelledCompatibility(t *testing.T) {
	proj := projectPlan(StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_cancelled",
			Title:  "Cancelled plan",
			State:  planStateClosed,
		},
		Execution: ExecutionRecord{},
	}, nil, 0)

	if proj.State != "cancelled" {
		t.Fatalf("projection state = %q, want cancelled", proj.State)
	}
	if proj.ExecutionDisposition != planExecutionDispositionCanceled {
		t.Fatalf("execution disposition = %q, want %q", proj.ExecutionDisposition, planExecutionDispositionCanceled)
	}
}

func TestProjectPlan_PreservesResearchCompleteCompatibility(t *testing.T) {
	proj := projectPlan(StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_research",
			Mode:   "research",
			Title:  "Research",
		},
		Execution: ExecutionRecord{
			ResearchOutput: "Findings",
		},
	}, nil, 0)

	if proj.State != planStateResearchComplete {
		t.Fatalf("projection state = %q, want %q", proj.State, planStateResearchComplete)
	}
	if proj.Phase != planStateResearchComplete {
		t.Fatalf("projection phase = %q, want %q", proj.Phase, planStateResearchComplete)
	}
}

func TestProjectPlan_UsesPlanStateCompatibilityWhenExecutionStateBlank(t *testing.T) {
	proj := projectPlan(StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_waiting",
			Title:  "Waiting plan",
			State:  planStateWaitingOnDependency,
		},
		Execution: ExecutionRecord{},
	}, nil, 0)

	if proj.State != planStateWaitingOnDependency {
		t.Fatalf("projection state = %q, want %q", proj.State, planStateWaitingOnDependency)
	}
	if proj.ExecutionDisposition != planExecutionDispositionWaiting {
		t.Fatalf("execution disposition = %q, want %q", proj.ExecutionDisposition, planExecutionDispositionWaiting)
	}
}

func TestProjectPlan_LiveTaskOverridesStaleImplementationFailedState(t *testing.T) {
	bundle := StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_retrying",
			Title:  "Retrying plan",
			State:  planStateImplementationFailed,
		},
		Execution: ExecutionRecord{
			State: planStateImplementationFailed,
		},
	}
	tasks := []pool.Task{{
		ID:     "plan_retrying-t1",
		PlanID: "plan_retrying",
		Role:   "implementer",
		Status: pool.TaskQueued,
	}}

	proj := projectPlan(bundle, tasks, 0)

	if proj.State != planStateActive {
		t.Fatalf("projection state = %q, want %q", proj.State, planStateActive)
	}
	if len(proj.ActiveTaskIDs) != 1 || proj.ActiveTaskIDs[0] != "plan_retrying-t1" {
		t.Fatalf("active task ids = %v, want [plan_retrying-t1]", proj.ActiveTaskIDs)
	}
}

func TestProjectPlan_LiveReviewCouncilTaskOverridesStaleReviewFailure(t *testing.T) {
	bundle := StoredPlan{
		Plan: PlanRecord{
			PlanID: "plan_review_retry",
			Title:  "Review retry",
			State:  planStateImplementationReviewFailed,
		},
		Execution: ExecutionRecord{
			State:               planStateImplementationReviewFailed,
			ImplReviewRequested: true,
		},
	}
	tasks := []pool.Task{{
		ID:     reviewCouncilTaskID("plan_review_retry", 2),
		PlanID: "plan_review_retry",
		Role:   "reviewer",
		Status: pool.TaskQueued,
	}}

	proj := projectPlan(bundle, tasks, 0)

	if proj.State != planStateImplementationReview {
		t.Fatalf("projection state = %q, want %q", proj.State, planStateImplementationReview)
	}
}
