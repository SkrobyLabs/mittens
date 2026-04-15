package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestKitchenStatusSnapshotIncludesWorkersProvidersAndLineages(t *testing.T) {
	k := newTestKitchen(t)
	if _, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, true); err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.health.SetCooldown("anthropic/sonnet", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}
	if err := k.lineageMgr.ActivatePlan("parser-errors", "plan_1"); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}

	status, err := k.StatusSnapshot()
	if err != nil {
		t.Fatalf("StatusSnapshot: %v", err)
	}
	if status["queue"] == nil || status["workers"] == nil || status["providers"] == nil || status["lineages"] == nil || status["plans"] == nil {
		t.Fatalf("status payload = %+v", status)
	}
	snapshot, ok := status["snapshot"].(map[string]any)
	if !ok || snapshot["planHistoryLimit"] != defaultPlanProgressHistoryLimit || snapshot["historyLimitOverridden"] != false {
		t.Fatalf("snapshot metadata = %#v, want default snapshot policy", status["snapshot"])
	}
	plans, ok := status["plans"].([]PlanProgress)
	if !ok || len(plans) != 1 {
		t.Fatalf("status plans = %#v, want 1 progress record", status["plans"])
	}
	if plans[0].Phase != "planning" {
		t.Fatalf("plan phase = %q, want planning", plans[0].Phase)
	}
	if !plans[0].ImplReviewRequested {
		t.Fatalf("plan progress = %+v, want impl review metadata", plans[0])
	}
	if len(plans[0].History) == 0 || plans[0].History[0].Type != planHistoryCouncilStarted {
		t.Fatalf("plan history = %+v, want initial planning history", plans[0].History)
	}
}

func TestKitchenStatusSnapshotWindowsPlanHistory(t *testing.T) {
	k := newTestKitchen(t)
	history := make([]PlanHistoryEntry, 0, defaultPlanProgressHistoryLimit+2)
	for i := 1; i <= defaultPlanProgressHistoryLimit+2; i++ {
		history = append(history, PlanHistoryEntry{
			Type:    planHistoryPlanningStarted,
			Cycle:   i,
			TaskID:  fmt.Sprintf("task-%d", i),
			Summary: fmt.Sprintf("entry-%d", i),
		})
	}
	if _, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_windowed_status",
			Lineage: "parser-errors",
			Title:   "Windowed history",
			State:   planStatePlanning,
		},
		Execution: ExecutionRecord{
			State:   planStatePlanning,
			History: history,
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	status, err := k.StatusSnapshot()
	if err != nil {
		t.Fatalf("StatusSnapshot: %v", err)
	}
	plans, ok := status["plans"].([]PlanProgress)
	if !ok || len(plans) != 1 {
		t.Fatalf("status plans = %#v, want one progress record", status["plans"])
	}
	if !plans[0].HistoryTruncated || plans[0].HistoryTotal != defaultPlanProgressHistoryLimit+2 || plans[0].HistoryIncluded != defaultPlanProgressHistoryLimit {
		t.Fatalf("plan progress = %+v, want truncated history metadata", plans[0])
	}
	if len(plans[0].History) != defaultPlanProgressHistoryLimit {
		t.Fatalf("windowed history = %+v, want %d entries", plans[0].History, defaultPlanProgressHistoryLimit)
	}
	if plans[0].History[0].Cycle != 3 {
		t.Fatalf("first windowed history cycle = %d, want 3", plans[0].History[0].Cycle)
	}
}

func TestKitchenStatusCommandOverridesHistoryLimit(t *testing.T) {
	k := newTestKitchen(t)
	history := make([]PlanHistoryEntry, 0, 4)
	for i := 1; i <= 4; i++ {
		history = append(history, PlanHistoryEntry{
			Type:    planHistoryPlanningStarted,
			Cycle:   i,
			TaskID:  fmt.Sprintf("task-%d", i),
			Summary: fmt.Sprintf("entry-%d", i),
		})
	}
	if _, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_status_override",
			Lineage: "parser-errors",
			Title:   "Override history",
			State:   planStatePlanning,
		},
		Execution: ExecutionRecord{
			State:   planStatePlanning,
			History: history,
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	output := runKitchenCommand(t, k, "status", "--history-limit", "2")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, output)
	}
	plans, ok := payload["plans"].([]any)
	if !ok || len(plans) != 1 {
		t.Fatalf("status plans = %#v, want one plan", payload["plans"])
	}
	plan, ok := plans[0].(map[string]any)
	if !ok {
		t.Fatalf("status plan = %#v, want object", plans[0])
	}
	if plan["historyIncluded"] != float64(2) || plan["historyTotal"] != float64(4) || plan["historyTruncated"] != true {
		t.Fatalf("status plan = %+v, want truncated history metadata", plan)
	}
	snapshot, ok := payload["snapshot"].(map[string]any)
	if !ok || snapshot["planHistoryLimit"] != float64(2) || snapshot["historyLimitOverridden"] != true {
		t.Fatalf("snapshot metadata = %#v, want override metadata", payload["snapshot"])
	}
}

func TestKitchenConfigCommandOutputsConfigAndPaths(t *testing.T) {
	k := newTestKitchen(t)

	output := runKitchenCommand(t, k, "config")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("config output is not JSON: %v\n%s", err, output)
	}
	if payload["config"] == nil || payload["paths"] == nil {
		t.Fatalf("config payload = %+v, want config and paths", payload)
	}

	output = runKitchenCommand(t, k, "config", "--paths")
	payload = nil
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("config --paths output is not JSON: %v\n%s", err, output)
	}
	if payload["paths"] == nil {
		t.Fatalf("config --paths payload = %+v, want paths", payload)
	}
	if payload["config"] != nil {
		t.Fatalf("config --paths payload = %+v, want no config block", payload)
	}
}

func TestKitchenCapabilitiesCommandOutputsCapabilityMap(t *testing.T) {
	k := newTestKitchen(t)

	output := runKitchenCommand(t, k, "capabilities")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("capabilities output is not JSON: %v\n%s", err, output)
	}
	if payload["meta"] == nil || payload["cli"] == nil || payload["api"] == nil || payload["planning"] == nil {
		t.Fatalf("capabilities payload = %+v, want meta/cli/api/planning sections", payload)
	}
	meta, ok := payload["meta"].(map[string]any)
	if !ok || meta["schemaVersion"] != float64(capabilitySchemaVersion) || meta["compatibility"] == nil {
		t.Fatalf("capabilities meta = %#v, want schemaVersion and compatibility", payload["meta"])
	}
	cliCaps, ok := payload["cli"].(map[string]any)
	if !ok || cliCaps["submit"] == nil || cliCaps["merge"] == nil || cliCaps["reapply"] == nil || cliCaps["retry"] == nil || cliCaps["delete"] == nil {
		t.Fatalf("cli capabilities = %#v, want submit, merge, reapply, retry, and delete sections", payload["cli"])
	}
	submitCaps, ok := cliCaps["submit"].(map[string]any)
	if !ok || submitCaps["options"] == nil {
		t.Fatalf("submit capabilities = %#v, want option metadata", cliCaps["submit"])
	}
	retryCaps, ok := cliCaps["retry"].(map[string]any)
	if !ok || retryCaps["options"] == nil {
		t.Fatalf("retry capabilities = %#v, want option metadata", cliCaps["retry"])
	}
	deleteCaps, ok := cliCaps["delete"].(map[string]any)
	if !ok || deleteCaps["target"] != "plan" {
		t.Fatalf("delete capabilities = %#v, want plan target", cliCaps["delete"])
	}
	runtimeCaps, ok := payload["runtime"].(map[string]any)
	if !ok || runtimeCaps["eventForwarding"] != true {
		t.Fatalf("runtime capabilities = %#v, want runtime event forwarding", payload["runtime"])
	}
	runtimeEndpoints, ok := runtimeCaps["endpoints"].(map[string]any)
	if !ok {
		t.Fatalf("runtime endpoints = %#v, want object", runtimeCaps["endpoints"])
	}
	recycleCaps, ok := runtimeEndpoints["recycle"].(map[string]any)
	if !ok || recycleCaps["status"] != "implemented" || recycleCaps["resetsSession"] != true || recycleCaps["mechanism"] != "broker_poll" {
		t.Fatalf("runtime recycle capabilities = %#v, want implemented recycle marker", runtimeEndpoints["recycle"])
	}
	assignCaps, ok := runtimeEndpoints["assignments"].(map[string]any)
	if !ok || assignCaps["status"] != "persist_only" || assignCaps["consumedByWorkers"] != false {
		t.Fatalf("runtime assignment capabilities = %#v, want persist-only marker", runtimeEndpoints["assignments"])
	}
	evidenceCaps, ok := cliCaps["evidence"].(map[string]any)
	if !ok || evidenceCaps["defaultTier"] != evidenceTierRich {
		t.Fatalf("evidence capabilities = %#v, want default rich tier", cliCaps["evidence"])
	}
	providers, ok := payload["providers"].([]any)
	if !ok || len(providers) != 3 {
		t.Fatalf("providers capabilities = %#v, want three providers", payload["providers"])
	}
	wantModels := map[string]string{
		"anthropic": "sonnet",
		"codex":     "gpt-5.4",
		"gemini":    "gemini-3-flash-preview",
	}
	for _, raw := range providers {
		providerCaps, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("provider capability entry = %#v, want object", raw)
		}
		name, _ := providerCaps["provider"].(string)
		models, ok := providerCaps["models"].([]any)
		if !ok || len(models) != 1 || models[0] != wantModels[name] {
			t.Fatalf("provider models for %q = %#v, want [%s]", name, providerCaps["models"], wantModels[name])
		}
	}

	output = runKitchenCommand(t, k, "capabilities", "--cli")
	payload = nil
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("capabilities --cli output is not JSON: %v\n%s", err, output)
	}
	if payload["meta"] == nil || payload["cli"] == nil {
		t.Fatalf("capabilities --cli payload = %+v, want meta and cli sections", payload)
	}
	if payload["api"] != nil || payload["planning"] != nil {
		t.Fatalf("capabilities --cli payload = %+v, want only meta and cli sections", payload)
	}
}

func TestKitchenMergeLineageMarksPlanMergedAndClearsActivePlan(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	poolStateDir := filepath.Join(k.project.PoolsDir, defaultPoolStateName)
	workerStateDir := pool.WorkerStateDir(poolStateDir, "w-1")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workerStateDir, pool.WorkerResultFile),
		[]byte("done\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := k.pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch(bundle.Plan.Lineage, bundle.Plan.Anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	worktree, err := gitMgr.CreateChildWorktree(bundle.Plan.Lineage, "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(worktree, "feature.txt"), "lineage change\n")
	mustRunGit(t, worktree, "add", "feature.txt")
	mustRunGit(t, worktree, "commit", "-m", "lineage change")
	if err := gitMgr.MergeChild(bundle.Plan.Lineage, "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	resp, err := k.MergeLineage(bundle.Plan.Lineage)
	if err != nil {
		t.Fatalf("MergeLineage: %v", err)
	}
	if resp["status"] != "merged" {
		t.Fatalf("merge response = %+v", resp)
	}

	merged, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if merged.Plan.State != planStateMerged {
		t.Fatalf("plan state = %q, want %q", merged.Plan.State, planStateMerged)
	}
	if merged.Execution.State != planStateMerged {
		t.Fatalf("execution state = %q, want %q", merged.Execution.State, planStateMerged)
	}
	if merged.Execution.CompletedAt == nil {
		t.Fatal("expected completedAt to be set")
	}
	if activePlan, err := k.lineageMgr.ActivePlan(bundle.Plan.Lineage); err == nil || activePlan != "" {
		t.Fatalf("active plan = %q, %v; want cleared", activePlan, err)
	}
}

func TestKitchenMergeLineageBlocksFailedImplementationReview(t *testing.T) {
	k := newTestKitchen(t)
	completedAt := time.Now().UTC()

	anchor, err := k.currentAnchor()
	if err != nil {
		t.Fatalf("currentAnchor: %v", err)
	}
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_impl_review_failed",
			Lineage: "parser-errors",
			Title:   "Impl review gate",
			State:   planStateCompleted,
			Anchor:  anchor,
		},
		Execution: ExecutionRecord{
			State:               planStateCompleted,
			Branch:              lineageBranchName("parser-errors"),
			Anchor:              anchor,
			CompletedAt:         &completedAt,
			ImplReviewRequested: true,
			ImplReviewStatus:    planReviewStatusFailed,
			ImplReviewFindings:  []string{"missing parser error handling"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := k.lineageMgr.ActivatePlan("parser-errors", planID); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}
	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	_, err = k.MergeLineage("parser-errors")
	if err == nil {
		t.Fatal("MergeLineage error = nil, want failed implementation review gate")
	}
	if !strings.Contains(err.Error(), "failed post-implementation review") {
		t.Fatalf("MergeLineage error = %q, want impl review failure", err)
	}
}

func TestKitchenMergeLineageBlocksLineageWithNoChanges(t *testing.T) {
	k := newTestKitchen(t)
	completedAt := time.Now().UTC()

	anchor, err := k.currentAnchor()
	if err != nil {
		t.Fatalf("currentAnchor: %v", err)
	}
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_merge_no_changes",
			Lineage: "parser-errors",
			Title:   "No lineage changes",
			State:   planStateCompleted,
			Anchor:  anchor,
		},
		Execution: ExecutionRecord{
			State:       planStateCompleted,
			Branch:      lineageBranchName("parser-errors"),
			Anchor:      anchor,
			CompletedAt: &completedAt,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := k.lineageMgr.ActivatePlan("parser-errors", planID); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}
	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	_, err = k.MergeLineage("parser-errors")
	if err == nil || !strings.Contains(err.Error(), "has no changes to merge") {
		t.Fatalf("MergeLineage error = %v, want no changes to merge", err)
	}

	updated, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if updated.Plan.State != planStateCompleted {
		t.Fatalf("plan state = %q, want %q", updated.Plan.State, planStateCompleted)
	}
	if updated.Execution.State != planStateCompleted {
		t.Fatalf("execution state = %q, want %q", updated.Execution.State, planStateCompleted)
	}
}

func TestKitchenMergeLineageIgnoresHistoricalCouncilFailuresOnCompletedPlan(t *testing.T) {
	k := newTestKitchen(t)
	completedAt := time.Now().UTC()

	anchor, err := k.currentAnchor()
	if err != nil {
		t.Fatalf("currentAnchor: %v", err)
	}
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_merge_ignore_historical_council_failures",
			Lineage: "parser-errors",
			Title:   "Ignore historical council failures",
			State:   planStateCompleted,
			Anchor:  anchor,
			Tasks: []PlanTask{{
				ID:               "t1",
				Title:            "Implement",
				Prompt:           "implement",
				Complexity:       ComplexityMedium,
				ReviewComplexity: ComplexityMedium,
			}},
		},
		Execution: ExecutionRecord{
			State:                 planStateCompleted,
			Branch:                lineageBranchName("parser-errors"),
			Anchor:                anchor,
			CompletedAt:           &completedAt,
			CouncilTurnsCompleted: 3,
			CouncilFinalDecision:  councilConverged,
			CompletedTaskIDs:      []string{planTaskRuntimeID("plan_merge_ignore_historical_council_failures", "t1")},
			FailedTaskIDs:         []string{"council_plan_merge_ignore_historical_council_failures_t1", "council_plan_merge_ignore_historical_council_failures_t3"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := k.lineageMgr.ActivatePlan("parser-errors", planID); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         "council_plan_merge_ignore_historical_council_failures_t1",
		PlanID:     planID,
		Prompt:     "planner turn 1",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask planner t1: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan-a", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker planner t1: %v", err)
	}
	if err := k.pm.RegisterWorker("w-plan-a", "container-plan-a"); err != nil {
		t.Fatalf("RegisterWorker planner t1: %v", err)
	}
	if err := k.pm.DispatchTask("council_plan_merge_ignore_historical_council_failures_t1", "w-plan-a"); err != nil {
		t.Fatalf("DispatchTask planner t1: %v", err)
	}
	if err := k.pm.FailTask("w-plan-a", "council_plan_merge_ignore_historical_council_failures_t1", "old planning failure"); err != nil {
		t.Fatalf("FailTask planner t1: %v", err)
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         "council_plan_merge_ignore_historical_council_failures_t3",
		PlanID:     planID,
		Prompt:     "planner turn 3",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       plannerTaskRole,
	}); err != nil {
		t.Fatalf("EnqueueTask planner t3: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-plan-b", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker planner t3: %v", err)
	}
	if err := k.pm.RegisterWorker("w-plan-b", "container-plan-b"); err != nil {
		t.Fatalf("RegisterWorker planner t3: %v", err)
	}
	if err := k.pm.DispatchTask("council_plan_merge_ignore_historical_council_failures_t3", "w-plan-b"); err != nil {
		t.Fatalf("DispatchTask planner t3: %v", err)
	}
	if err := k.pm.FailTask("w-plan-b", "council_plan_merge_ignore_historical_council_failures_t3", "superseded planning failure"); err != nil {
		t.Fatalf("FailTask planner t3: %v", err)
	}
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "implement",
		Complexity: string(ComplexityMedium),
		Priority:   1,
		Role:       "implementer",
	}); err != nil {
		t.Fatalf("EnqueueTask implementer: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-impl", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker implementer: %v", err)
	}
	if err := k.pm.RegisterWorker("w-impl", "container-impl"); err != nil {
		t.Fatalf("RegisterWorker implementer: %v", err)
	}
	if err := k.pm.DispatchTask(planTaskRuntimeID(planID, "t1"), "w-impl"); err != nil {
		t.Fatalf("DispatchTask implementer: %v", err)
	}
	if err := k.pm.CompleteTask("w-impl", planTaskRuntimeID(planID, "t1")); err != nil {
		t.Fatalf("CompleteTask implementer: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	worktree, err := gitMgr.CreateChildWorktree("parser-errors", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(worktree, "feature.txt"), "lineage change\n")
	mustRunGit(t, worktree, "add", "feature.txt")
	mustRunGit(t, worktree, "commit", "-m", "lineage change")
	if err := gitMgr.MergeChild("parser-errors", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	resp, err := k.MergeLineage("parser-errors")
	if err != nil {
		t.Fatalf("MergeLineage: %v", err)
	}
	if resp["status"] != "merged" {
		t.Fatalf("merge response = %+v", resp)
	}
}

func TestKitchenReapplyLineageUpdatesPlanAnchor(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	poolStateDir := filepath.Join(k.project.PoolsDir, defaultPoolStateName)
	workerStateDir := pool.WorkerStateDir(poolStateDir, "w-1")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := k.pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch(bundle.Plan.Lineage, bundle.Plan.Anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	worktree, err := gitMgr.CreateChildWorktree(bundle.Plan.Lineage, "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(worktree, "feature.txt"), "lineage change\n")
	mustRunGit(t, worktree, "add", "feature.txt")
	mustRunGit(t, worktree, "commit", "-m", "lineage change")
	if err := gitMgr.MergeChild(bundle.Plan.Lineage, "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}
	writeFile(t, filepath.Join(k.repoPath, "base.txt"), "base change\n")
	mustRunGit(t, k.repoPath, "add", "base.txt")
	mustRunGit(t, k.repoPath, "commit", "-m", "base change")

	resp, err := k.ReapplyLineage(bundle.Plan.Lineage)
	if err != nil {
		t.Fatalf("ReapplyLineage: %v", err)
	}
	if resp["status"] != "reapplied" {
		t.Fatalf("reapply response = %+v, want status=reapplied", resp)
	}

	updated, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	lineageHead, err := runGit(k.repoPath, "rev-parse", lineageBranchName(bundle.Plan.Lineage))
	if err != nil {
		t.Fatalf("rev-parse lineage: %v", err)
	}
	lineageHead = strings.TrimSpace(lineageHead)
	if updated.Plan.Anchor.Branch != bundle.Plan.Anchor.Branch {
		t.Fatalf("anchor branch = %q, want %q", updated.Plan.Anchor.Branch, bundle.Plan.Anchor.Branch)
	}
	if updated.Plan.Anchor.Commit != lineageHead {
		t.Fatalf("anchor commit = %q, want %q", updated.Plan.Anchor.Commit, lineageHead)
	}
}

func TestKitchenReapplyLineageBlocksActiveTasks(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	_, err = k.ReapplyLineage(bundle.Plan.Lineage)
	if err == nil {
		t.Fatal("ReapplyLineage error = nil, want active task gate")
	}
	if !strings.Contains(err.Error(), "active tasks") {
		t.Fatalf("ReapplyLineage error = %q, want active tasks", err)
	}
}

func TestKitchenReapplyLineageQueuesFixTaskOnConflicts(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	poolStateDir := filepath.Join(k.project.PoolsDir, defaultPoolStateName)
	workerStateDir := pool.WorkerStateDir(poolStateDir, "w-1")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := k.pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch(bundle.Plan.Lineage, bundle.Plan.Anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	writeFile(t, filepath.Join(k.repoPath, "shared.txt"), "main change\n")
	mustRunGit(t, k.repoPath, "add", "shared.txt")
	mustRunGit(t, k.repoPath, "commit", "-m", "main change")

	mustRunGit(t, k.repoPath, "checkout", lineageBranchName(bundle.Plan.Lineage))
	writeFile(t, filepath.Join(k.repoPath, "shared.txt"), "lineage change\n")
	mustRunGit(t, k.repoPath, "add", "shared.txt")
	mustRunGit(t, k.repoPath, "commit", "-m", "lineage change")
	mustRunGit(t, k.repoPath, "checkout", "main")

	resp, err := k.ReapplyLineage(bundle.Plan.Lineage)
	if err != nil {
		t.Fatalf("ReapplyLineage: %v", err)
	}
	if resp["status"] != "fix-merge-queued" {
		t.Fatalf("reapply response = %+v, want status=fix-merge-queued", resp)
	}
	newTaskID, _ := resp["newTaskId"].(string)
	if strings.TrimSpace(newTaskID) == "" {
		t.Fatalf("reapply response missing newTaskId: %+v", resp)
	}
	conflicts, ok := resp["conflicts"].([]string)
	if !ok || len(conflicts) != 1 || conflicts[0] != "shared.txt" {
		t.Fatalf("reapply conflicts = %#v, want [shared.txt]", resp["conflicts"])
	}

	gotPlan, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if gotPlan.Plan.State != planStateActive || gotPlan.Execution.State != planStateActive {
		t.Fatalf("plan/execution state = %q/%q, want active/active", gotPlan.Plan.State, gotPlan.Execution.State)
	}
	if !containsString(gotPlan.Execution.ActiveTaskIDs, newTaskID) {
		t.Fatalf("active task IDs = %+v, want %s", gotPlan.Execution.ActiveTaskIDs, newTaskID)
	}
	found := false
	for _, task := range gotPlan.Plan.Tasks {
		if task.ID == strings.TrimPrefix(newTaskID, bundle.Plan.PlanID+"-") {
			found = true
			if task.Title == "" || task.Outputs == nil || len(task.Outputs.Files) != 1 || task.Outputs.Files[0] != "shared.txt" {
				t.Fatalf("queued fix task = %+v", task)
			}
		}
	}
	if !found {
		t.Fatalf("plan tasks missing queued fix task for %s", newTaskID)
	}
}

func TestKitchenCleanWorktreesRemovesOnlyOrphans(t *testing.T) {
	k := newTestKitchen(t)

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	activeWorktree, err := gitMgr.CreateChildWorktree("parser-errors", "active-task")
	if err != nil {
		t.Fatalf("CreateChildWorktree(active): %v", err)
	}
	orphanWorktree, err := gitMgr.CreateChildWorktree("parser-errors", "orphan-task")
	if err != nil {
		t.Fatalf("CreateChildWorktree(orphan): %v", err)
	}

	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         "active-task",
		Prompt:     "Active task",
		Complexity: string(ComplexityLow),
		Priority:   1,
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	removed, err := k.CleanWorktrees()
	if err != nil {
		t.Fatalf("CleanWorktrees: %v", err)
	}
	if len(removed) != 1 || removed[0] != orphanWorktree {
		t.Fatalf("removed = %+v, want [%s]", removed, orphanWorktree)
	}
	if _, err := os.Stat(activeWorktree); err != nil {
		t.Fatalf("active worktree missing: %v", err)
	}
	if _, err := os.Stat(orphanWorktree); !os.IsNotExist(err) {
		t.Fatalf("orphan worktree still exists: %v", err)
	}
}

func TestKitchenStatusCommandOutputsJSON(t *testing.T) {
	k := newTestKitchen(t)

	output := runKitchenCommand(t, k, "status")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, output)
	}
	if payload["queue"] == nil || payload["workers"] == nil {
		t.Fatalf("status payload = %+v", payload)
	}
}

func TestKitchenStatusIncludesRuntimeActivity(t *testing.T) {
	k := newTestKitchen(t)
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	k.hostAPI = &eventingRuntimeAPI{
		activity: map[string]*pool.WorkerActivity{
			"w-1": {Kind: "tool", Phase: "started", Name: "apply_patch"},
		},
	}

	payload, err := k.StatusSnapshot()
	if err != nil {
		t.Fatalf("StatusSnapshot: %v", err)
	}
	runtimeActivity, ok := payload["runtimeActivity"].(map[string]*pool.WorkerActivity)
	if !ok || runtimeActivity["w-1"] == nil {
		t.Fatalf("runtimeActivity = %#v, want entry for w-1", payload["runtimeActivity"])
	}
}

func TestKitchenEvidenceCommandOutputsPlanEvidence(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	output := runKitchenCommand(t, k, "evidence", bundle.Plan.PlanID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("evidence output is not JSON: %v\n%s", err, output)
	}
	if payload["tier"] != evidenceTierRich || payload["plan"] == nil || payload["queue"] == nil || payload["workers"] == nil || payload["progress"] == nil || payload["history"] == nil {
		t.Fatalf("evidence payload = %+v", payload)
	}
}

func TestKitchenEvidenceCommandOutputsCompactTier(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	output := runKitchenCommand(t, k, "evidence", "--compact", bundle.Plan.PlanID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("evidence --compact output is not JSON: %v\n%s", err, output)
	}
	if payload["tier"] != evidenceTierCompact || payload["taskCounts"] == nil || payload["phase"] == nil {
		t.Fatalf("compact evidence payload = %+v", payload)
	}
	if payload["plan"] != nil || payload["queue"] != nil {
		t.Fatalf("compact evidence payload = %+v, want no rich-only sections", payload)
	}
}

func TestKitchenEvidenceIncludesRuntimeActivity(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "planner-1", Role: plannerTaskRole}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("planner-1", "container-planner-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	taskID := currentPlanControlTaskID(t, k, bundle.Plan.PlanID, func(task pool.Task) bool {
		return task.Role == plannerTaskRole
	})
	if err := k.pm.DispatchTask(taskID, "planner-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	k.hostAPI = &eventingRuntimeAPI{
		activity: map[string]*pool.WorkerActivity{
			"planner-1": {Kind: "status", Phase: "active", Name: "planning"},
		},
	}

	payload, err := k.Evidence(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("Evidence: %v", err)
	}
	runtimeActivity, ok := payload["runtimeActivity"].(map[string]*pool.WorkerActivity)
	if !ok || runtimeActivity["planner-1"] == nil {
		t.Fatalf("runtimeActivity = %#v, want entry for planner-1", payload["runtimeActivity"])
	}
}

func TestKitchenPlanCommandOutputsProgress(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, true)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	output := runKitchenCommand(t, k, "plan", bundle.Plan.PlanID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("plan output is not JSON: %v\n%s", err, output)
	}
	progress, ok := payload["progress"].(map[string]any)
	if !ok {
		t.Fatalf("plan payload progress = %#v, want object", payload["progress"])
	}
	if progress["phase"] != "planning" {
		t.Fatalf("plan progress phase = %v, want planning", progress["phase"])
	}
	if payload["history"] == nil {
		t.Fatalf("plan payload history = %#v, want history", payload["history"])
	}
}

func TestKitchenHistoryCommandShowsTimelineAndCycleFilter(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Typed parser errors"))
	output := runKitchenCommand(t, k, "history", bundle.Plan.PlanID)
	if !strings.Contains(output, "1\tcouncil_started\t") {
		t.Fatalf("history output = %q, want initial council line", output)
	}
	if !strings.Contains(output, "1\tcouncil_turn_completed\t") {
		t.Fatalf("history output = %q, want first council turn line", output)
	}
	if !strings.Contains(output, "2\tcouncil_turn_completed\t") {
		t.Fatalf("history output = %q, want second council turn line", output)
	}

	cycleOutput := runKitchenCommand(t, k, "history", "--cycle", "2", bundle.Plan.PlanID)
	for _, line := range strings.Split(strings.TrimSpace(cycleOutput), "\n") {
		if strings.HasPrefix(line, "1\t") {
			t.Fatalf("cycle-filtered history output = %q, want no cycle 1 entries", cycleOutput)
		}
	}
	if !strings.Contains(cycleOutput, "2\tcouncil_turn_completed\t") {
		t.Fatalf("cycle-filtered history output = %q, want cycle 2 entry", cycleOutput)
	}
}

func TestKitchenHistoryCommandOutputsJSON(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	output := runKitchenCommand(t, k, "history", "--json", bundle.Plan.PlanID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("history output is not JSON: %v\n%s", err, output)
	}
	if payload["planId"] != bundle.Plan.PlanID {
		t.Fatalf("history payload = %+v, want matching planId", payload)
	}
	history, ok := payload["history"].([]any)
	if !ok || len(history) == 0 {
		t.Fatalf("history payload = %+v, want non-empty history array", payload)
	}
}

func TestKitchenProviderResetCommandResetsHealth(t *testing.T) {
	k := newTestKitchen(t)
	if err := k.health.SetCooldown("anthropic/sonnet", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}

	output := runKitchenCommand(t, k, "provider", "reset", "anthropic/sonnet")
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("provider reset output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "reset" {
		t.Fatalf("provider reset payload = %+v", payload)
	}
	reopened, closeFn, err := openKitchen(k.repoPath)
	if err != nil {
		t.Fatalf("openKitchen: %v", err)
	}
	defer func() { _ = closeFn() }()
	if got := reopened.health.Get("anthropic/sonnet"); got != (HealthEntry{}) {
		t.Fatalf("provider health entry still present after reset: %+v", got)
	}
}

func TestKitchenSubmitCommandUsesDetectedServerMetadata(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	if err := writeServeMetadata(k.project, k.repoPath, server.URL, ""); err != nil {
		t.Fatalf("writeServeMetadata: %v", err)
	}

	output := runKitchenCommand(t, k, "submit", "Add typed parser errors")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, output)
	}
	planID, _ := payload["planId"].(string)
	if planID == "" {
		t.Fatalf("submit payload = %+v, want planId", payload)
	}
	if payload["lineage"] == nil || payload["lineage"] == "" {
		t.Fatalf("submit payload = %+v, want lineage", payload)
	}

	taskID := councilTaskID(planID, 1)
	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("server pool manager missing planner task %q; submit likely bypassed the running API", taskID)
	}
	if task.Status != pool.TaskQueued || task.Role != plannerTaskRole {
		t.Fatalf("planner task = %+v, want queued planner task", task)
	}
}

func TestKitchenSubmitCommandReadsIdeaFromFile(t *testing.T) {
	k := newTestKitchen(t)
	ideaFile := filepath.Join(t.TempDir(), "idea.md")
	idea := "Add typed parser errors from a design note"
	if err := os.WriteFile(ideaFile, []byte(idea), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	output := runKitchenCommand(t, k, "submit", "--file", ideaFile)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, output)
	}

	planID, _ := payload["planId"].(string)
	if planID == "" {
		t.Fatalf("planId missing from payload: %+v", payload)
	}
	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Plan.Summary != idea {
		t.Fatalf("plan summary = %q, want %q", bundle.Plan.Summary, idea)
	}
}

func TestKitchenSubmitCommandPersistsExplicitAnchorRef(t *testing.T) {
	k := newTestKitchen(t)
	mustRunGit(t, k.repoPath, "checkout", "-b", "feature-anchor")
	writeFile(t, filepath.Join(k.repoPath, "feature.txt"), "feature\n")
	mustRunGit(t, k.repoPath, "add", "feature.txt")
	mustRunGit(t, k.repoPath, "commit", "-m", "feature")

	output := runKitchenCommand(t, k, "submit", "--anchor-ref", "main", "Add typed parser errors")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, output)
	}

	planID, _ := payload["planId"].(string)
	if planID == "" {
		t.Fatalf("planId missing from payload: %+v", payload)
	}
	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	mainHead, err := runGit(k.repoPath, "rev-parse", "main^{commit}")
	if err != nil {
		t.Fatalf("rev-parse main: %v", err)
	}
	if bundle.Plan.Anchor.Branch != "main" {
		t.Fatalf("anchor branch = %q, want main", bundle.Plan.Anchor.Branch)
	}
	if bundle.Plan.Anchor.Commit != strings.TrimSpace(mainHead) {
		t.Fatalf("anchor commit = %q, want %q", bundle.Plan.Anchor.Commit, strings.TrimSpace(mainHead))
	}
}

func TestKitchenSubmitCommandReadsIdeaFromStdin(t *testing.T) {
	k := newTestKitchen(t)
	idea := "Add typed parser errors from stdin"

	output := runKitchenCommandWithInput(t, k, bytes.NewBufferString(idea), "submit")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, output)
	}

	planID, _ := payload["planId"].(string)
	if planID == "" {
		t.Fatalf("planId missing from payload: %+v", payload)
	}
	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Plan.Summary != idea {
		t.Fatalf("plan summary = %q, want %q", bundle.Plan.Summary, idea)
	}
}

func TestKitchenSubmitCommandReadsIdeaFromEditor(t *testing.T) {
	k := newTestKitchen(t)
	editorScript := filepath.Join(t.TempDir(), "editor.sh")
	idea := "Add typed parser errors from editor"
	script := "#!/bin/sh\nprintf '%s' > \"$1\"\n"
	if err := os.WriteFile(editorScript, []byte(fmt.Sprintf(script, idea)), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("EDITOR", editorScript)

	output := runKitchenCommandWithInput(t, k, bytes.NewBuffer(nil), "submit")
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, output)
	}

	planID, _ := payload["planId"].(string)
	if planID == "" {
		t.Fatalf("planId missing from payload: %+v", payload)
	}
	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Plan.Summary != idea {
		t.Fatalf("plan summary = %q, want %q", bundle.Plan.Summary, idea)
	}
}

func TestKitchenCancelCommandCancelsActivePlan(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add typed parser errors"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	output := runKitchenCommand(t, k, "cancel", bundle.Plan.PlanID)
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("cancel output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "cancelled" {
		t.Fatalf("cancel payload = %+v", payload)
	}

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != "cancelled" {
		t.Fatalf("execution state = %q, want cancelled", got.Execution.State)
	}
}

func TestKitchenReplanCommandStartsFreshPlanningPass(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add typed parser errors", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add typed parser errors"))
	oldRuntimeTaskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")

	output := runKitchenCommand(t, k, "replan", bundle.Plan.PlanID, "--reason", "Need a narrower rollout")
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("replan output is not JSON: %v\n%s", err, output)
	}

	newPlanID := payload["newPlanId"]
	if newPlanID == "" {
		t.Fatalf("newPlanId missing from payload: %+v", payload)
	}
	replanned, err := k.GetPlan(newPlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if replanned.Execution.State != planStatePlanning {
		t.Fatalf("execution state = %q, want %q", replanned.Execution.State, planStatePlanning)
	}
	if replanned.Plan.PlanID == bundle.Plan.PlanID {
		t.Fatalf("replanned plan reused source plan ID %q", replanned.Plan.PlanID)
	}
	if !bytes.Contains([]byte(replanned.Plan.Summary), []byte("Need a narrower rollout")) {
		t.Fatalf("replanned summary = %q, want appended reason", replanned.Plan.Summary)
	}
	if len(replanned.Plan.Tasks) != 0 {
		t.Fatalf("replanned plan tasks = %+v, want fresh planner-generated tasks", replanned.Plan.Tasks)
	}
	if _, ok := k.pm.Task(oldRuntimeTaskID); ok {
		t.Fatalf("superseded runtime task %q still exists after replan", oldRuntimeTaskID)
	}
	// Replan supersedes the source plan; the old record must be gone
	// so the operator doesn't have to prune it manually.
	if _, err := k.GetPlan(bundle.Plan.PlanID); err == nil {
		t.Fatalf("superseded plan %s still exists after replan", bundle.Plan.PlanID)
	}
}

func TestKitchenDeleteCommandRemovesPlanTasksAndQuestions(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add typed parser errors", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add typed parser errors"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-delete", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-delete", "container-w-delete"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	questionID, err := k.RouteQuestion("w-delete", taskID, "Need clarification")
	if err != nil {
		t.Fatalf("RouteQuestion: %v", err)
	}

	output := runKitchenCommand(t, k, "delete", bundle.Plan.PlanID)
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("delete output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "deleted" {
		t.Fatalf("delete payload = %+v", payload)
	}

	reopened, closeFn, err := openKitchen(k.repoPath)
	if err != nil {
		t.Fatalf("openKitchen: %v", err)
	}
	defer func() { _ = closeFn() }()

	if _, err := reopened.GetPlan(bundle.Plan.PlanID); err == nil {
		t.Fatal("expected deleted plan lookup to fail")
	}
	if _, ok := reopened.pm.Task(taskID); ok {
		t.Fatalf("task %q should be deleted", taskID)
	}
	if reopened.pm.GetQuestion(questionID) != nil {
		t.Fatalf("question %q should be deleted", questionID)
	}
	if _, err := reopened.lineageMgr.ActivePlan(bundle.Plan.Lineage); !os.IsNotExist(err) {
		t.Fatalf("ActivePlan err = %v, want not exists", err)
	}
}

func TestKitchenRetryCommandRevivesFailedTask(t *testing.T) {
	k := newTestKitchen(t)
	taskID, planID := seedFailedImplementationTask(t, k)

	output := runKitchenCommand(t, k, "retry", taskID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("retry output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "retried" || payload["taskId"] != taskID || payload["requireFreshWorker"] != true {
		t.Fatalf("retry payload = %+v", payload)
	}

	reopened, closeFn, err := openKitchen(k.repoPath)
	if err != nil {
		t.Fatalf("openKitchen: %v", err)
	}
	defer func() { _ = closeFn() }()

	task, ok := reopened.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
	if !task.RequireFreshWorker {
		t.Fatal("expected retried task to require a fresh worker")
	}
	if task.RetryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", task.RetryCount)
	}

	bundle, err := reopened.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Execution.State != planStateActive {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateActive)
	}
	if !containsString(bundle.Execution.ActiveTaskIDs, taskID) {
		t.Fatalf("active task ids = %+v, want %q", bundle.Execution.ActiveTaskIDs, taskID)
	}
	if containsString(bundle.Execution.FailedTaskIDs, taskID) {
		t.Fatalf("failed task ids = %+v, want %q removed", bundle.Execution.FailedTaskIDs, taskID)
	}
	if len(bundle.Execution.History) == 0 {
		t.Fatal("expected retry history entry")
	}
	last := bundle.Execution.History[len(bundle.Execution.History)-1]
	if last.Type != planHistoryManualRetried || last.TaskID != taskID || !strings.Contains(last.Summary, "fresh worker required=true") {
		t.Fatalf("last history entry = %+v, want manual retry entry", last)
	}
}

func TestKitchenRetryCommandSameWorkerClearsFreshWorkerRequirement(t *testing.T) {
	k := newTestKitchen(t)
	taskID, _ := seedFailedImplementationTask(t, k)

	output := runKitchenCommand(t, k, "retry", "--same-worker", taskID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("retry --same-worker output is not JSON: %v\n%s", err, output)
	}
	if payload["requireFreshWorker"] != false {
		t.Fatalf("retry payload = %+v, want requireFreshWorker=false", payload)
	}

	reopened, closeFn, err := openKitchen(k.repoPath)
	if err != nil {
		t.Fatalf("openKitchen: %v", err)
	}
	defer func() { _ = closeFn() }()

	task, ok := reopened.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.RequireFreshWorker {
		t.Fatal("expected retried task to allow reuse of an eligible idle worker")
	}

	bundle, err := reopened.GetPlan(task.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	last := bundle.Execution.History[len(bundle.Execution.History)-1]
	if last.Type != planHistoryManualRetried || !strings.Contains(last.Summary, "fresh worker required=false") {
		t.Fatalf("last history entry = %+v, want manual retry entry with reuse", last)
	}
}

func TestKitchenRetryCommandRequeuesFailedReviewCouncilTaskIntoImplementationReview(t *testing.T) {
	k := newTestKitchen(t)
	taskID, planID := seedFailedReviewCouncilTask(t, k)

	output := runKitchenCommand(t, k, "retry", taskID)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("retry output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "retried" || payload["taskId"] != taskID {
		t.Fatalf("retry payload = %+v", payload)
	}

	reopened, closeFn, err := openKitchen(k.repoPath)
	if err != nil {
		t.Fatalf("openKitchen: %v", err)
	}
	defer func() { _ = closeFn() }()

	task, ok := reopened.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued {
		t.Fatalf("task status = %q, want %q", task.Status, pool.TaskQueued)
	}

	bundle, err := reopened.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if bundle.Execution.State != planStateImplementationReview {
		t.Fatalf("execution state = %q, want %q", bundle.Execution.State, planStateImplementationReview)
	}
	if bundle.Plan.State != planStateImplementationReview {
		t.Fatalf("plan state = %q, want %q", bundle.Plan.State, planStateImplementationReview)
	}
	if bundle.Execution.ImplReviewStatus != "" || len(bundle.Execution.ImplReviewFindings) != 0 {
		t.Fatalf("impl review metadata = %+v, want cleared before retry", bundle.Execution)
	}
}

func TestKitchenReplanCommandRequeuesPlanningWhenSourcePlanHasNoTasks(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Review this branch and draft squash message", "review-branch", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	bundle.Plan.State = planStatePlanningFailed
	bundle.Execution.State = planStatePlanningFailed
	bundle.Execution.ActiveTaskIDs = nil
	bundle.Execution.FailedTaskIDs = []string{councilTaskID(bundle.Plan.PlanID, 1)}
	if err := k.planStore.UpdatePlan(bundle.Plan); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	if err := k.planStore.UpdateExecution(bundle.Plan.PlanID, bundle.Execution); err != nil {
		t.Fatalf("UpdateExecution: %v", err)
	}

	output := runKitchenCommand(t, k, "replan", bundle.Plan.PlanID, "--reason", "Try again")
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("replan output is not JSON: %v\n%s", err, output)
	}
	newPlanID := payload["newPlanId"]
	if newPlanID == "" {
		t.Fatalf("newPlanId missing from payload: %+v", payload)
	}
	replanned, err := k.GetPlan(newPlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if replanned.Execution.State != planStatePlanning {
		t.Fatalf("execution state = %q, want %q", replanned.Execution.State, planStatePlanning)
	}
	if len(replanned.Execution.ActiveTaskIDs) != 1 {
		t.Fatalf("active task ids = %+v, want one planner task", replanned.Execution.ActiveTaskIDs)
	}
	// Even on the no-tasks path, the original plan should be deleted
	// once the new planning run is queued.
	if _, err := k.GetPlan(bundle.Plan.PlanID); err == nil {
		t.Fatalf("superseded plan %s still exists after replan", bundle.Plan.PlanID)
	}
}

func TestKitchenQuestionCommandsManagePendingQuestions(t *testing.T) {
	k := newTestKitchen(t)
	seedQuestion(t, k)

	output := runKitchenCommand(t, k, "questions")
	var listed map[string][]map[string]any
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		t.Fatalf("questions output is not JSON: %v\n%s", err, output)
	}
	if len(listed["questions"]) != 1 {
		t.Fatalf("questions payload = %+v", listed)
	}
	questionID, _ := listed["questions"][0]["id"].(string)
	if questionID == "" {
		t.Fatalf("question payload missing id: %+v", listed["questions"][0])
	}

	output = runKitchenCommand(t, k, "answer", questionID, "Use typed errors")
	var answered map[string]string
	if err := json.Unmarshal([]byte(output), &answered); err != nil {
		t.Fatalf("answer output is not JSON: %v\n%s", err, output)
	}
	if answered["status"] != "answered" {
		t.Fatalf("answer payload = %+v", answered)
	}

	reopened, closeFn, err := openKitchen(k.repoPath)
	if err != nil {
		t.Fatalf("openKitchen: %v", err)
	}
	defer func() { _ = closeFn() }()
	if got := reopened.pm.GetQuestion(questionID); got == nil || got.Answer == "" {
		t.Fatalf("question not answered in reopened pool state: %+v", got)
	}
}

func TestKitchenUnhelpfulCommandInvalidatesAffinity(t *testing.T) {
	k := newTestKitchen(t)
	seedQuestion(t, k)
	questions := k.ListQuestions()
	if len(questions) != 1 {
		t.Fatalf("questions = %+v, want 1 pending question", questions)
	}
	questionID := questions[0].ID

	output := runKitchenCommand(t, k, "unhelpful", questionID)
	var payload map[string]string
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unhelpful output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "recorded" {
		t.Fatalf("unhelpful payload = %+v", payload)
	}

	planID, err := k.planIDForQuestion(questionID)
	if err != nil {
		t.Fatalf("planIDForQuestion: %v", err)
	}
	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if !bundle.Affinity.Invalidated {
		t.Fatal("expected affinity invalidation after unhelpful command")
	}
	if bundle.Affinity.InvalidationReason != "operator_marked_unhelpful" {
		t.Fatalf("invalidation reason = %q, want operator_marked_unhelpful", bundle.Affinity.InvalidationReason)
	}
}

func TestKitchenLineagesCommandListsActiveLineages(t *testing.T) {
	k := newTestKitchen(t)
	if err := k.lineageMgr.ActivatePlan("parser-errors", "plan_1"); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}

	output := runKitchenCommand(t, k, "lineages")
	var payload map[string][]map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("lineages output is not JSON: %v\n%s", err, output)
	}
	if len(payload["lineages"]) != 1 {
		t.Fatalf("lineages payload = %+v", payload)
	}
	if payload["lineages"][0]["name"] != "parser-errors" {
		t.Fatalf("lineage name = %v, want parser-errors", payload["lineages"][0]["name"])
	}
}

func TestKitchenMergeCheckCommandBlocksLineageWithNoChanges(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch(bundle.Plan.Lineage, bundle.Plan.Anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(k.repoPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})
	t.Setenv("KITCHEN_HOME", k.paths.HomeDir)

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"merge-check", bundle.Plan.Lineage})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "has no changes to merge") {
		t.Fatalf("merge-check error = %v, want no changes to merge\n%s", err, out.String())
	}
}

func TestKitchenReapplyCommandReportsUpToDate(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	poolStateDir := filepath.Join(k.project.PoolsDir, defaultPoolStateName)
	workerStateDir := pool.WorkerStateDir(poolStateDir, "w-1")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := k.pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch(bundle.Plan.Lineage, bundle.Plan.Anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	lineageWT, err := gitMgr.CreateChildWorktree(bundle.Plan.Lineage, "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(lineageWT, "feature.txt"), "lineage change\n")
	mustRunGit(t, lineageWT, "add", "feature.txt")
	mustRunGit(t, lineageWT, "commit", "-m", "lineage change")
	if err := gitMgr.MergeChild(bundle.Plan.Lineage, "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	output := runKitchenCommand(t, k, "reapply", bundle.Plan.Lineage)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("reapply output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "up-to-date" {
		t.Fatalf("reapply payload = %+v, want status=up-to-date", payload)
	}
}

func TestKitchenMergeCommandNoCommitPreviewsWithoutUpdatingBase(t *testing.T) {
	k := newTestKitchen(t)

	bundle, err := k.SubmitIdea("Add parser error normalization", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add parser error normalization"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	poolStateDir := filepath.Join(k.project.PoolsDir, defaultPoolStateName)
	workerStateDir := pool.WorkerStateDir(poolStateDir, "w-1")
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile result: %v", err)
	}
	if err := k.pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch(bundle.Plan.Lineage, bundle.Plan.Anchor.Commit); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	worktree, err := gitMgr.CreateChildWorktree(bundle.Plan.Lineage, "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(worktree, "feature.txt"), "lineage change\n")
	mustRunGit(t, worktree, "add", "feature.txt")
	mustRunGit(t, worktree, "commit", "-m", "lineage change")
	if err := gitMgr.MergeChild(bundle.Plan.Lineage, "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	beforeBaseHead, err := runGit(k.repoPath, "rev-parse", bundle.Plan.Anchor.Branch)
	if err != nil {
		t.Fatalf("rev-parse base before: %v", err)
	}

	output := runKitchenCommand(t, k, "merge", "--no-commit", bundle.Plan.Lineage)
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("merge --no-commit output is not JSON: %v\n%s", err, output)
	}
	if payload["status"] != "preview" {
		t.Fatalf("preview payload = %+v, want status=preview", payload)
	}
	if payload["previewHead"] == "" {
		t.Fatalf("preview payload missing previewHead: %+v", payload)
	}
	afterBaseHead, err := runGit(k.repoPath, "rev-parse", bundle.Plan.Anchor.Branch)
	if err != nil {
		t.Fatalf("rev-parse base after: %v", err)
	}
	if strings.TrimSpace(beforeBaseHead) != strings.TrimSpace(afterBaseHead) {
		t.Fatalf("base branch head changed during preview: before=%q after=%q", beforeBaseHead, afterBaseHead)
	}
}

func runKitchenCommand(t *testing.T, k *Kitchen, args ...string) string {
	return runKitchenCommandWithInput(t, k, nil, args...)
}

func seedFailedImplementationTask(t *testing.T, k *Kitchen) (string, string) {
	t.Helper()

	bundle, err := k.SubmitIdea("Add typed parser errors", "parser-errors", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add typed parser errors"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-retry", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-retry", "container-w-retry"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-retry"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := k.pm.FailTask("w-retry", taskID, "operator requested retry"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	return taskID, bundle.Plan.PlanID
}

func seedFailedReviewCouncilTask(t *testing.T, k *Kitchen) (string, string) {
	t.Helper()

	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_failed_review_retry",
			Lineage: "failed-review-retry",
			Title:   "Failed review retry",
			State:   planStateImplementationReviewFailed,
		},
		Execution: ExecutionRecord{
			State:                       planStateImplementationReviewFailed,
			ImplReviewRequested:         true,
			ImplReviewStatus:            planReviewStatusFailed,
			ImplReviewFindings:          []string{"adapter exited with code 1"},
			ReviewCouncilMaxTurns:       6,
			ReviewCouncilTurnsCompleted: 5,
			FailedTaskIDs:               []string{reviewCouncilTaskID("plan_failed_review_retry", 6)},
			ReviewCouncilSeats:          newReviewCouncilSeats(),
		},
	})
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	taskID := reviewCouncilTaskID(planID, 6)
	if _, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         taskID,
		PlanID:     planID,
		Prompt:     "review the implementation",
		Complexity: string(ComplexityMedium),
		Priority:   10,
		Role:       "reviewer",
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-review-retry", Role: "reviewer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-review-retry", "container-w-review-retry"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := k.pm.DispatchTask(taskID, "w-review-retry"); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	if err := k.pm.FailTask("w-review-retry", taskID, "adapter exited with code 1"); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	return taskID, planID
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func runKitchenCommandWithInput(t *testing.T, k *Kitchen, input *bytes.Buffer, args ...string) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(k.repoPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})
	t.Setenv("KITCHEN_HOME", k.paths.HomeDir)

	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if input != nil {
		cmd.SetIn(input)
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run kitchen %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}
