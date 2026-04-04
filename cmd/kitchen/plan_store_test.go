package main

import (
	"encoding/json"
	"testing"
)

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
		State:           "active",
		Approved:        true,
		ReviewRequested: true,
		ReviewRounds:    2,
		ReviewStatus:    "passed",
		ReviewFindings:  []string{"single-task draft"},
		History: []PlanHistoryEntry{{
			Type:    planHistoryReviewPassed,
			Cycle:   1,
			TaskID:  "plan_custom-plan-review-1",
			Verdict: "pass",
			Findings: []string{
				"single-task draft",
			},
		}},
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
	if !got.Execution.ReviewRequested || got.Execution.ReviewRounds != 2 || got.Execution.ReviewStatus != "passed" {
		t.Fatalf("execution review metadata = %+v, want persisted review fields", got.Execution)
	}
	if len(got.Execution.History) != 1 || got.Execution.History[0].Type != planHistoryReviewPassed {
		t.Fatalf("execution history = %+v, want persisted review history", got.Execution.History)
	}
	if got.Affinity.LastWorkerID != "w-impl-1" {
		t.Fatalf("affinity = %+v, want last worker set", got.Affinity)
	}
}

func TestPlanDependencyUnmarshalSupportsLegacyStrings(t *testing.T) {
	var task PlanTask
	if err := json.Unmarshal([]byte(`{
		"id":"t2",
		"prompt":"update callers",
		"complexity":"medium",
		"dependencies":["t1",{"task":"t0","type":"ordering","consumes":["plan.md"]}]
	}`), &task); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(task.Dependencies) != 2 {
		t.Fatalf("dependencies = %+v, want 2 entries", task.Dependencies)
	}
	if task.Dependencies[0].Task != "t1" {
		t.Fatalf("dependencies[0] = %+v, want legacy string task t1", task.Dependencies[0])
	}
	if task.Dependencies[1].Task != "t0" || task.Dependencies[1].Type != "ordering" {
		t.Fatalf("dependencies[1] = %+v, want typed dependency", task.Dependencies[1])
	}
	if len(task.Dependencies[1].Consumes) != 1 || task.Dependencies[1].Consumes[0] != "plan.md" {
		t.Fatalf("dependencies[1].Consumes = %+v, want plan.md", task.Dependencies[1].Consumes)
	}
}
