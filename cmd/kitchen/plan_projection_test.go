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

func TestProjectPlan_CanceledLineageFixMergeProjectsCompletedWithPendingMerge(t *testing.T) {
	bundle := StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_fix_merge_canceled",
			Title:   "Canceled merge fix",
			Lineage: "feat/canceled-merge-fix",
			State:   planStateActive,
			Tasks: []PlanTask{
				{
					ID:               "t1",
					Title:            "Implement",
					Prompt:           "implement",
					Complexity:       ComplexityMedium,
					ReviewComplexity: ComplexityMedium,
				},
				{
					ID:               "fix-merge-123",
					Title:            "Fix merge conflicts",
					Prompt:           "resolve merge conflicts",
					Complexity:       ComplexityMedium,
					ReviewComplexity: ComplexityMedium,
				},
			},
		},
		Execution: ExecutionRecord{
			State:               planStateActive,
			ImplReviewRequested: true,
			ImplReviewStatus:    planReviewStatusPassed,
		},
	}
	tasks := []pool.Task{
		{
			ID:     planTaskRuntimeID("plan_fix_merge_canceled", "t1"),
			PlanID: "plan_fix_merge_canceled",
			Role:   "implementer",
			Status: pool.TaskCompleted,
		},
		{
			ID:     planTaskRuntimeID("plan_fix_merge_canceled", "fix-merge-123"),
			PlanID: "plan_fix_merge_canceled",
			Role:   lineageFixMergeRole,
			Status: pool.TaskCanceled,
		},
	}

	proj := projectPlan(bundle, tasks, 0)

	if proj.State != planStateCompleted {
		t.Fatalf("projection state = %q, want %q", proj.State, planStateCompleted)
	}
	if proj.ExecutionDisposition != planExecutionDispositionSucceeded {
		t.Fatalf("execution disposition = %q, want %q", proj.ExecutionDisposition, planExecutionDispositionSucceeded)
	}
	if proj.MergeDisposition != planMergeDispositionPending {
		t.Fatalf("merge disposition = %q, want %q", proj.MergeDisposition, planMergeDispositionPending)
	}
	if got := proj.ActiveTaskIDs; len(got) != 0 {
		t.Fatalf("active task ids = %v, want none", got)
	}
}
