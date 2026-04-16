package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestQueueReviewCouncilResumeIfReadyWithoutSchedulerEnqueuesNextTurn(t *testing.T) {
	k := newTestKitchen(t)
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_review_resume",
			Lineage: "review-resume",
			Title:   "Resume implementation review",
			Summary: "Resume after operator answers",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			State:                        planStateImplementationReview,
			ImplReviewRequested:          true,
			ReviewCouncilAwaitingAnswers: true,
			ReviewCouncilMaxTurns:        4,
			ReviewCouncilTurnsCompleted:  1,
			ReviewCouncilSeats:           newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := k.queueReviewCouncilResumeIfReady(planID); err != nil {
		t.Fatalf("queueReviewCouncilResumeIfReady: %v", err)
	}

	taskID := reviewCouncilTaskID(planID, 2)
	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("review council task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}

	bundle, err := k.planStore.Get(planID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if bundle.Execution.ReviewCouncilAwaitingAnswers {
		t.Fatal("expected awaiting answers to be cleared")
	}
	if len(bundle.Execution.ActiveTaskIDs) != 1 || bundle.Execution.ActiveTaskIDs[0] != taskID {
		t.Fatalf("active task ids = %v, want [%s]", bundle.Execution.ActiveTaskIDs, taskID)
	}
	last := bundle.Execution.History[len(bundle.Execution.History)-1]
	if last.Type != planHistoryReviewCouncilResumed {
		t.Fatalf("last history type = %q, want %q", last.Type, planHistoryReviewCouncilResumed)
	}
}

func TestQueuePlannerCouncilResumeIfReadyUsesProjectedReviewingStateCompatibility(t *testing.T) {
	k := newTestKitchen(t)
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_planner_resume_compat",
			Lineage: "planner-resume-compat",
			Title:   "Resume planner review",
			State:   planStateReviewing,
		},
		Execution: ExecutionRecord{
			CouncilAwaitingAnswers: true,
			CouncilTurnsCompleted:  1,
			CouncilMaxTurns:        4,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := k.queuePlannerCouncilResumeIfReady(planID); err != nil {
		t.Fatalf("queuePlannerCouncilResumeIfReady: %v", err)
	}

	taskID := councilTaskID(planID, 2)
	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("planner council task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}
}

func TestQueueReviewCouncilResumeIfReadyUsesProjectedImplementationReviewCompatibility(t *testing.T) {
	k := newTestKitchen(t)
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_review_resume_compat",
			Lineage: "review-resume-compat",
			Title:   "Resume implementation review compat",
			State:   planStateImplementationReview,
		},
		Execution: ExecutionRecord{
			ImplReviewRequested:          true,
			ReviewCouncilAwaitingAnswers: true,
			ReviewCouncilMaxTurns:        4,
			ReviewCouncilTurnsCompleted:  1,
			ReviewCouncilSeats:           newReviewCouncilSeats(),
			ReviewCouncilTurns: []ReviewCouncilTurnRecord{{
				Seat: "A",
				Turn: 1,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := k.queueReviewCouncilResumeIfReady(planID); err != nil {
		t.Fatalf("queueReviewCouncilResumeIfReady: %v", err)
	}

	taskID := reviewCouncilTaskID(planID, 2)
	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("review council task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}
}
