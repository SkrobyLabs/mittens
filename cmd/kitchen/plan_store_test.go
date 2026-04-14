package main

import "testing"

func TestPlanStoreCreateGetAndList(t *testing.T) {
	store := NewPlanStore(t.TempDir())
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			Lineage: "parser-errors",
			Title:   "Parser error handling",
			Summary: "Introduce typed parser errors",
		},
		Affinity: AffinityRecord{
			PlannerWorkerID: "w-planner-1",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Plan.PlanID != planID {
		t.Fatalf("planId = %q, want %q", got.Plan.PlanID, planID)
	}
	if got.Execution.State != "pending_approval" {
		t.Fatalf("execution state = %q, want pending_approval", got.Execution.State)
	}
	if got.Affinity.PlannerWorkerID != "w-planner-1" {
		t.Fatalf("plannerWorkerId = %q, want w-planner-1", got.Affinity.PlannerWorkerID)
	}

	plans, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 1 || plans[0].PlanID != planID {
		t.Fatalf("plans = %+v, want one plan %s", plans, planID)
	}
}

func TestPlanStoreUpdateExecutionAndAffinity(t *testing.T) {
	store := NewPlanStore(t.TempDir())
	planID, err := store.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_custom",
			Lineage: "parser-errors",
			Title:   "Parser error handling",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exec := ExecutionRecord{
		State:               "active",
		Approved:            true,
		ImplReviewRequested: true,
		ImplReviewStatus:    "passed",
		ImplReviewFollowups: []string{"single-task draft"},
		CouncilMaxTurns:     4,
		History: []PlanHistoryEntry{
			{
				Type:    planHistoryCouncilConverged,
				Cycle:   1,
				TaskID:  councilTaskID(planID, 2),
				Summary: "Council converged.",
			},
			{
				Type:    planHistoryCouncilAutoConverged,
				Cycle:   2,
				TaskID:  councilTaskID(planID, 3),
				Summary: "Council auto-converged.",
			},
		},
		ActiveTaskIDs: []string{"t1"},
	}
	if err := store.UpdateExecution(planID, exec); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}

	affinity := AffinityRecord{
		PlannerWorkerID: "w-planner-2",
		LastWorkerID:    "w-impl-1",
	}
	if err := store.UpdateAffinity(planID, affinity); err != nil {
		t.Fatalf("UpdateAffinity: %v", err)
	}

	got, err := store.Get(planID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Execution.State != "active" || len(got.Execution.ActiveTaskIDs) != 1 {
		t.Fatalf("execution = %+v, want active task state", got.Execution)
	}
	if !got.Execution.ImplReviewRequested || got.Execution.ImplReviewStatus != "passed" || got.Execution.CouncilMaxTurns != 4 {
		t.Fatalf("execution metadata = %+v, want persisted impl review + council fields", got.Execution)
	}
	if len(got.Execution.History) != 2 || got.Execution.History[0].Type != planHistoryCouncilConverged || got.Execution.History[1].Type != planHistoryCouncilAutoConverged {
		t.Fatalf("execution history = %+v, want persisted council history", got.Execution.History)
	}
	if got.Affinity.LastWorkerID != "w-impl-1" {
		t.Fatalf("affinity = %+v, want last worker set", got.Affinity)
	}
}
