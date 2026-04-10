package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestKitchenAPIPlanLifecycleAndQueue(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodPost, "/v1/ideas", map[string]any{
		"idea": "Add typed parser errors",
		"auto": false,
	})
	var created map[string]any
	decodeResponse(t, resp, &created)
	planID, _ := created["planId"].(string)
	if planID == "" {
		t.Fatal("expected planId")
	}
	if created["lineage"] == "" {
		t.Fatalf("expected lineage in %+v", created)
	}
	completePlanningTask(t, k, planID, basicPlannedArtifact("Add typed parser errors"))

	resp = apiRequest(t, server, http.MethodPost, "/v1/plans/"+planID+"/approve", map[string]any{})
	var approved map[string]string
	decodeResponse(t, resp, &approved)
	if approved["status"] != planStateActive {
		t.Fatalf("approve status = %q", approved["status"])
	}

	resp = apiRequest(t, server, http.MethodGet, "/v1/queue", nil)
	var queue map[string]any
	decodeResponse(t, resp, &queue)
	if queue["aliveWorkers"] == nil {
		t.Fatalf("queue payload = %+v", queue)
	}

	resp = apiRequest(t, server, http.MethodGet, "/v1/plans/"+planID+"/evidence", nil)
	var evidence map[string]any
	decodeResponse(t, resp, &evidence)
	if evidence["plan"] == nil || evidence["queue"] == nil || evidence["progress"] == nil || evidence["history"] == nil {
		t.Fatalf("evidence payload = %+v", evidence)
	}

	resp = apiRequest(t, server, http.MethodGet, "/v1/plans/"+planID, nil)
	var detail map[string]any
	decodeResponse(t, resp, &detail)
	if detail["plan"] == nil || detail["execution"] == nil || detail["progress"] == nil || detail["history"] == nil {
		t.Fatalf("plan detail payload = %+v", detail)
	}

	resp = apiRequest(t, server, http.MethodGet, "/v1/plans/"+planID+"/history", nil)
	var history map[string]any
	decodeResponse(t, resp, &history)
	if history["planId"] != planID || history["history"] == nil {
		t.Fatalf("history payload = %+v", history)
	}
}

func TestKitchenAPIStatusEndpoint(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	if _, err := k.SubmitIdea("Add typed parser errors", "", false, false); err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	resp := apiRequest(t, server, http.MethodGet, "/v1/status?historyLimit=2", nil)
	var snapshot map[string]any
	decodeResponse(t, resp, &snapshot)
	if snapshot["queue"] == nil || snapshot["plans"] == nil || snapshot["snapshot"] == nil {
		t.Fatalf("status payload = %+v", snapshot)
	}
	meta, ok := snapshot["snapshot"].(map[string]any)
	if !ok || meta["planHistoryLimit"] != float64(2) || meta["historyLimitOverridden"] != true {
		t.Fatalf("snapshot metadata = %#v, want override policy", snapshot["snapshot"])
	}
}

func TestKitchenAPIPlanHistoryCycleFilter(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	bundle, err := k.SubmitIdea("Introduce typed parser errors for lexer failures", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Typed parser errors"))

	resp := apiRequest(t, server, http.MethodGet, "/v1/plans/"+bundle.Plan.PlanID+"/history?cycle=2", nil)
	var payload map[string]any
	decodeResponse(t, resp, &payload)
	if payload["planId"] != bundle.Plan.PlanID {
		t.Fatalf("history payload = %+v, want matching planId", payload)
	}
	history, ok := payload["history"].([]any)
	if !ok || len(history) == 0 {
		t.Fatalf("history payload = %+v, want non-empty filtered history", payload)
	}
	for _, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("history entry = %#v, want object", raw)
		}
		if entry["cycle"] != float64(2) {
			t.Fatalf("history entry = %+v, want cycle 2", entry)
		}
	}
}

func TestKitchenAPITaskOutputEndpoint(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	t.Run("success", func(t *testing.T) {
		taskID := "task_output_1"
		outputDir := filepath.Join(k.pm.StateDir(), "outputs")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatalf("MkdirAll outputs: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, taskID+".txt"), []byte("final response"), 0o644); err != nil {
			t.Fatalf("WriteFile output: %v", err)
		}

		resp := apiRequest(t, server, http.MethodGet, "/v1/tasks/"+taskID+"/output", nil)
		var payload map[string]any
		decodeResponse(t, resp, &payload)
		if payload["taskId"] != taskID || payload["output"] != "final response" {
			t.Fatalf("payload = %+v, want task output response", payload)
		}
	})

	t.Run("missing side file returns 404", func(t *testing.T) {
		resp := apiRequestExpectStatus(t, server, http.MethodGet, "/v1/tasks/missing_output/output", nil, http.StatusNotFound)
		defer resp.Body.Close()
	})

	t.Run("invalid id returns 400", func(t *testing.T) {
		resp := apiRequestExpectStatus(t, server, http.MethodGet, "/v1/tasks/invalid$id/output", nil, http.StatusBadRequest)
		defer resp.Body.Close()
	})
}

func TestKitchenAPIClientTaskOutput(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", r.Method)
			}
			if r.URL.Path != "/v1/tasks/task_a-1/output" {
				t.Fatalf("path = %s, want task output path", r.URL.Path)
			}
			writeAPIJSON(w, http.StatusOK, map[string]any{
				"taskId": "task_a-1",
				"output": "client task output",
			})
		}))
		defer server.Close()

		client := &kitchenAPIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
		}
		output, err := client.TaskOutput("task_a-1")
		if err != nil {
			t.Fatalf("TaskOutput: %v", err)
		}
		if output != "client task output" {
			t.Fatalf("output = %q, want client task output", output)
		}
	})

	t.Run("not found maps to os.ErrNotExist", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusNotFound, "read task output: open missing: no such file or directory")
		}))
		defer server.Close()

		client := &kitchenAPIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
		}
		_, err := client.TaskOutput("task_missing")
		if !os.IsNotExist(err) {
			t.Fatalf("err = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("server errors are preserved", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusBadGateway, "upstream output fetch failed")
		}))
		defer server.Close()

		client := &kitchenAPIClient{
			baseURL:    server.URL,
			httpClient: server.Client(),
		}
		_, err := client.TaskOutput("task_bad_gateway")
		if err == nil || !strings.Contains(err.Error(), "upstream output fetch failed") {
			t.Fatalf("err = %v, want preserved server error", err)
		}
	})
}

func TestKitchenAPIImplReviewRequest(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodPost, "/v1/ideas", map[string]any{
		"idea":       "Add typed parser errors",
		"implReview": true,
	})
	var created map[string]any
	decodeResponse(t, resp, &created)
	if created["planId"] == nil || created["planId"] == "" {
		t.Fatalf("expected planId in %+v", created)
	}
	planID, _ := created["planId"].(string)
	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if !bundle.Execution.ImplReviewRequested {
		t.Fatalf("execution = %+v, want impl review requested", bundle.Execution)
	}
}

func TestKitchenAPIActivePlanCancel(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add typed parser errors"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	resp := apiRequest(t, server, http.MethodDelete, "/v1/plans/"+bundle.Plan.PlanID, nil)
	var canceled map[string]string
	decodeResponse(t, resp, &canceled)
	if canceled["status"] != "cancelled" {
		t.Fatalf("cancel status = %q, want cancelled", canceled["status"])
	}

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != "cancelled" {
		t.Fatalf("execution state = %q, want cancelled", got.Execution.State)
	}
}

func TestKitchenAPIDeletePlanEndpointRemovesPlanTasksAndQuestions(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
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

	resp := apiRequest(t, server, http.MethodDelete, "/v1/plans/"+bundle.Plan.PlanID+"/purge", nil)
	var deleted map[string]string
	decodeResponse(t, resp, &deleted)
	if deleted["status"] != "deleted" {
		t.Fatalf("delete status = %q, want deleted", deleted["status"])
	}
	if _, err := k.GetPlan(bundle.Plan.PlanID); err == nil {
		t.Fatal("expected deleted plan lookup to fail")
	}
	if _, ok := k.pm.Task(taskID); ok {
		t.Fatalf("task %q should be deleted", taskID)
	}
	if got := k.pm.GetQuestion(questionID); got != nil {
		t.Fatalf("question = %+v, want deleted", got)
	}
}

func TestKitchenAPIRetryTaskEndpoint(t *testing.T) {
	k := newTestKitchen(t)
	taskID, planID := seedFailedImplementationTask(t, k)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodPost, "/v1/tasks/"+taskID+"/retry", map[string]any{})
	var payload map[string]any
	decodeResponse(t, resp, &payload)
	if payload["status"] != "retried" || payload["taskId"] != taskID || payload["requireFreshWorker"] != true {
		t.Fatalf("retry payload = %+v", payload)
	}

	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != pool.TaskQueued || !task.RequireFreshWorker {
		t.Fatalf("task = %+v, want queued with fresh-worker requirement", task)
	}

	bundle, err := k.GetPlan(planID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if containsString(bundle.Execution.FailedTaskIDs, taskID) {
		t.Fatalf("failed task ids = %+v, want %q removed", bundle.Execution.FailedTaskIDs, taskID)
	}
	last := bundle.Execution.History[len(bundle.Execution.History)-1]
	if last.Type != planHistoryManualRetried || last.TaskID != taskID {
		t.Fatalf("last history entry = %+v, want manual retry entry", last)
	}
}

func TestKitchenAPIRetryTaskEndpointSupportsSameWorker(t *testing.T) {
	k := newTestKitchen(t)
	taskID, _ := seedFailedImplementationTask(t, k)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodPost, "/v1/tasks/"+taskID+"/retry", map[string]any{
		"requireFreshWorker": false,
	})
	var payload map[string]any
	decodeResponse(t, resp, &payload)
	if payload["requireFreshWorker"] != false {
		t.Fatalf("retry payload = %+v, want requireFreshWorker=false", payload)
	}

	task, ok := k.pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.RequireFreshWorker {
		t.Fatalf("task = %+v, want requireFreshWorker=false", task)
	}
}

func TestKitchenAPIRetryTaskEndpointRejectsNonFailedTask(t *testing.T) {
	k := newTestKitchen(t)
	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}
	completePlanningTask(t, k, bundle.Plan.PlanID, basicPlannedArtifact("Add typed parser errors"))
	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}
	taskID := planTaskRuntimeID(bundle.Plan.PlanID, "t1")
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequestExpectStatus(t, server, http.MethodPost, "/v1/tasks/"+taskID+"/retry", map[string]any{}, http.StatusConflict)
	var payload map[string]any
	decodeResponse(t, resp, &payload)
	if payload["error"] != "task_not_failed" || payload["taskId"] != taskID || payload["currentStatus"] != pool.TaskQueued {
		t.Fatalf("retry payload = %+v", payload)
	}
}

func TestKitchenAPIRetryTaskEndpointIsIdempotentAfterRetry(t *testing.T) {
	k := newTestKitchen(t)
	taskID, _ := seedFailedImplementationTask(t, k)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodPost, "/v1/tasks/"+taskID+"/retry", map[string]any{})
	var first map[string]any
	decodeResponse(t, resp, &first)
	if first["status"] != "retried" {
		t.Fatalf("first retry payload = %+v", first)
	}

	resp = apiRequestExpectStatus(t, server, http.MethodPost, "/v1/tasks/"+taskID+"/retry", map[string]any{
		"requireFreshWorker": false,
	}, http.StatusOK)
	var second map[string]any
	decodeResponse(t, resp, &second)
	if second["alreadyRetried"] != true || second["taskId"] != taskID || second["requireFreshWorker"] != true {
		t.Fatalf("second retry payload = %+v", second)
	}
}

func TestKitchenAPIQuestionAndAffinityEndpoints(t *testing.T) {
	k := newTestKitchen(t)
	seedQuestion(t, k)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodGet, "/v1/questions", nil)
	var listed map[string][]map[string]any
	decodeResponse(t, resp, &listed)
	if len(listed["questions"]) != 1 {
		t.Fatalf("questions = %+v", listed["questions"])
	}
	qid, _ := listed["questions"][0]["id"].(string)
	if qid == "" {
		t.Fatalf("question payload = %+v", listed["questions"][0])
	}

	resp = apiRequest(t, server, http.MethodPost, "/v1/questions/"+qid+"/answer", map[string]any{
		"answer": "Use typed errors",
	})
	var answered map[string]string
	decodeResponse(t, resp, &answered)
	if answered["status"] != "answered" {
		t.Fatalf("answered status = %q", answered["status"])
	}

	resp = apiRequest(t, server, http.MethodPost, "/v1/questions/"+qid+"/unhelpful", map[string]any{})
	var recorded map[string]string
	decodeResponse(t, resp, &recorded)
	if recorded["status"] != "recorded" {
		t.Fatalf("recorded status = %q", recorded["status"])
	}
}

func TestKitchenAPIProviderAndLineageEndpoints(t *testing.T) {
	k := newTestKitchen(t)
	if err := k.health.SetCooldown("anthropic/sonnet", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}
	server := httptest.NewServer(k.NewAPIHandler("secret"))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/providers/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Kitchen-Token", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var providers map[string]map[string]any
	decodeResponse(t, resp, &providers)
	if providers["providers"] == nil {
		t.Fatalf("providers payload = %+v", providers)
	}

	resp = apiRequestWithToken(t, server, http.MethodPost, "/v1/providers/anthropic/models/sonnet/reset", map[string]any{}, "secret")
	var reset map[string]string
	decodeResponse(t, resp, &reset)
	if reset["status"] != "reset" {
		t.Fatalf("reset status = %q", reset["status"])
	}

	if err := k.lineageMgr.ActivatePlan("parser-errors", "plan_1"); err != nil {
		t.Fatalf("ActivatePlan: %v", err)
	}
	resp = apiRequestWithToken(t, server, http.MethodGet, "/v1/lineages", nil, "secret")
	var lineages map[string][]map[string]any
	decodeResponse(t, resp, &lineages)
	if len(lineages["lineages"]) != 1 {
		t.Fatalf("lineages payload = %+v", lineages)
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		t.Fatalf("gitManager: %v", err)
	}
	if err := gitMgr.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	resp = apiRequestWithToken(t, server, http.MethodPost, "/v1/lineages/parser-errors/merge", map[string]any{
		"mode":     "direct",
		"noCommit": true,
	}, "secret")
	var preview map[string]any
	decodeResponse(t, resp, &preview)
	if preview["status"] != "preview" {
		t.Fatalf("preview payload = %+v, want status=preview", preview)
	}
	if preview["previewHead"] == nil || preview["previewHead"] == "" {
		t.Fatalf("preview payload missing previewHead: %+v", preview)
	}
}

func TestKitchenAPIMetaEndpoint(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	resp := apiRequest(t, server, http.MethodGet, "/v1/meta", nil)
	var payload map[string]any
	decodeResponse(t, resp, &payload)
	if payload["version"] == nil || payload["commit"] == nil || payload["date"] == nil {
		t.Fatalf("meta payload = %+v, want build metadata", payload)
	}
	if payload["config"] == nil || payload["paths"] == nil || payload["capabilities"] == nil {
		t.Fatalf("meta payload = %+v, want config, paths, and capabilities", payload)
	}
	caps, ok := payload["capabilities"].(map[string]any)
	if !ok || caps["meta"] == nil || caps["api"] == nil || caps["planning"] == nil {
		t.Fatalf("meta capabilities = %#v, want meta, api, and planning sections", payload["capabilities"])
	}
	meta, ok := caps["meta"].(map[string]any)
	if !ok || meta["schemaVersion"] != float64(capabilitySchemaVersion) || meta["sections"] == nil {
		t.Fatalf("meta capabilities metadata = %#v, want schemaVersion and sections", caps["meta"])
	}
	apiCaps, ok := caps["api"].(map[string]any)
	if !ok {
		t.Fatalf("api capabilities = %#v, want object", caps["api"])
	}
	endpoints, ok := apiCaps["endpoints"].(map[string]any)
	if !ok || endpoints["taskRetry"] != "/v1/tasks/{id}/retry" || endpoints["taskOutput"] != "/v1/tasks/{id}/output" || endpoints["planDelete"] != "/v1/plans/{id}/purge" {
		t.Fatalf("api endpoints = %#v, want taskRetry, taskOutput, and planDelete endpoints", apiCaps["endpoints"])
	}
	eventsCaps, ok := apiCaps["events"].(map[string]any)
	if !ok || eventsCaps["query"] == nil {
		t.Fatalf("events capabilities = %#v, want query metadata", apiCaps["events"])
	}
	planEvidenceCaps, ok := apiCaps["planEvidence"].(map[string]any)
	if !ok || planEvidenceCaps["defaultTier"] != evidenceTierRich || planEvidenceCaps["query"] == nil {
		t.Fatalf("plan evidence capabilities = %#v, want tier metadata", apiCaps["planEvidence"])
	}
}

func TestKitchenAPIPlanEvidenceCompactTier(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	resp := apiRequest(t, server, http.MethodGet, "/v1/plans/"+bundle.Plan.PlanID+"/evidence?tier=compact", nil)
	var evidence map[string]any
	decodeResponse(t, resp, &evidence)
	if evidence["tier"] != evidenceTierCompact || evidence["taskCounts"] == nil || evidence["phase"] == nil {
		t.Fatalf("compact evidence payload = %+v", evidence)
	}
	if evidence["plan"] != nil || evidence["queue"] != nil {
		t.Fatalf("compact evidence payload = %+v, want no rich-only sections", evidence)
	}
}

func TestKitchenAPIPlanEvidenceRejectsUnknownTier(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/plans/"+bundle.Plan.PlanID+"/evidence?tier=verbose", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var payload map[string]any
	decodeResponse(t, resp, &payload)
	if payload["error"] == nil {
		t.Fatalf("error payload = %+v", payload)
	}
}

func TestKitchenAPIEventsStreamsLiveNotifications(t *testing.T) {
	k := newTestKitchen(t)
	taskID := seedQuestionTarget(t, k)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	event, payload := readSSEEvent(t, reader)
	if event != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", event)
	}
	if payload["queue"] == nil {
		t.Fatalf("snapshot payload = %+v", payload)
	}

	if _, err := k.RouteQuestion("w-1", taskID, "Need clarification"); err != nil {
		t.Fatalf("RouteQuestion: %v", err)
	}

	event, payload = readSSEEvent(t, reader)
	if event != "question" {
		t.Fatalf("second event = %q, want question", event)
	}
	if payload["type"] != "question" {
		t.Fatalf("payload type = %v, want question", payload["type"])
	}
	if payload["formatted"] == nil || payload["formatted"] == "" {
		t.Fatalf("payload formatted = %#v, want non-empty", payload["formatted"])
	}
	if payload["queue"] == nil {
		t.Fatalf("payload queue = %#v, want snapshot", payload["queue"])
	}
	if payload["planId"] == nil || payload["planId"] == "" {
		t.Fatalf("payload planId = %#v, want attached plan", payload["planId"])
	}
	if payload["progress"] == nil {
		t.Fatalf("payload progress = %#v, want plan progress", payload["progress"])
	}
	if payload["historyEntry"] != nil {
		t.Fatalf("payload historyEntry = %#v, want no history delta for question", payload["historyEntry"])
	}
}

func TestKitchenAPIEventsStreamsPlanLifecycleNotifications(t *testing.T) {
	k := newTestKitchen(t)
	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	event, _ := readSSEEvent(t, reader)
	if event != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", event)
	}

	bundle, err := k.SubmitIdea("Add typed parser errors", "", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	var payload map[string]any
	for {
		event, payload = readSSEEvent(t, reader)
		if event == "task_created" {
			continue
		}
		if event != "plan_submitted" {
			t.Fatalf("unexpected event = %q, want plan_submitted", event)
		}
		break
	}
	if payload["type"] != "plan_submitted" {
		t.Fatalf("payload type = %v, want plan_submitted", payload["type"])
	}
	if payload["id"] != bundle.Plan.PlanID {
		t.Fatalf("payload id = %v, want %q", payload["id"], bundle.Plan.PlanID)
	}
	if payload["planId"] != bundle.Plan.PlanID {
		t.Fatalf("payload planId = %v, want %q", payload["planId"], bundle.Plan.PlanID)
	}
	progress, ok := payload["progress"].(map[string]any)
	if !ok {
		t.Fatalf("payload progress = %#v, want object", payload["progress"])
	}
	if progress["phase"] != "planning" {
		t.Fatalf("payload progress phase = %v, want planning", progress["phase"])
	}
	if payload["historyEntry"] != nil {
		t.Fatalf("payload historyEntry = %#v, want no history delta for submission event", payload["historyEntry"])
	}
}

func TestKitchenAPIEventSnapshotIncludesPlanHistory(t *testing.T) {
	k := newTestKitchen(t)
	history := make([]PlanHistoryEntry, 0, defaultPlanProgressHistoryLimit+2)
	for i := 1; i <= defaultPlanProgressHistoryLimit+2; i++ {
		history = append(history, PlanHistoryEntry{
			Type:    planHistoryPlanningStarted,
			Cycle:   i,
			TaskID:  "task",
			Summary: "entry",
		})
	}
	bundleID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_snapshot_history",
			Lineage: "parser-errors",
			Title:   "Snapshot history",
			State:   planStatePlanning,
		},
		Execution: ExecutionRecord{
			State:   planStatePlanning,
			History: history,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	event, payload := readSSEEvent(t, reader)
	if event != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", event)
	}
	snapshot, ok := payload["snapshot"].(map[string]any)
	if !ok || snapshot["planHistoryLimit"] != float64(defaultPlanProgressHistoryLimit) || snapshot["historyLimitOverridden"] != false {
		t.Fatalf("snapshot metadata = %#v, want default snapshot policy", payload["snapshot"])
	}
	plans, ok := payload["plans"].([]any)
	if !ok || len(plans) != 1 {
		t.Fatalf("snapshot plans = %#v, want one open plan", payload["plans"])
	}
	plan, ok := plans[0].(map[string]any)
	if !ok {
		t.Fatalf("snapshot plan = %#v, want object", plans[0])
	}
	if plan["planId"] != bundleID {
		t.Fatalf("snapshot plan = %+v, want matching planId", plan)
	}
	historyItems, ok := plan["history"].([]any)
	if !ok || len(historyItems) != defaultPlanProgressHistoryLimit {
		t.Fatalf("snapshot plan history = %#v, want %d windowed entries", plan["history"], defaultPlanProgressHistoryLimit)
	}
	if plan["historyTruncated"] != true {
		t.Fatalf("snapshot plan = %+v, want historyTruncated=true", plan)
	}
	if plan["historyTotal"] != float64(defaultPlanProgressHistoryLimit+2) {
		t.Fatalf("snapshot plan historyTotal = %v, want %d", plan["historyTotal"], defaultPlanProgressHistoryLimit+2)
	}
	if plan["historyIncluded"] != float64(defaultPlanProgressHistoryLimit) {
		t.Fatalf("snapshot plan historyIncluded = %v, want %d", plan["historyIncluded"], defaultPlanProgressHistoryLimit)
	}
	entry, ok := historyItems[0].(map[string]any)
	if !ok || entry["cycle"] != float64(3) {
		t.Fatalf("snapshot first history entry = %#v, want cycle 3", historyItems[0])
	}
}

func TestKitchenAPIEventSnapshotHistoryLimitOverride(t *testing.T) {
	k := newTestKitchen(t)
	history := make([]PlanHistoryEntry, 0, 4)
	for i := 1; i <= 4; i++ {
		history = append(history, PlanHistoryEntry{
			Type:    planHistoryPlanningStarted,
			Cycle:   i,
			TaskID:  "task",
			Summary: "entry",
		})
	}
	if _, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_snapshot_override",
			Lineage: "parser-errors",
			Title:   "Snapshot override",
			State:   planStatePlanning,
		},
		Execution: ExecutionRecord{
			State:   planStatePlanning,
			History: history,
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	server := httptest.NewServer(k.NewAPIHandler(""))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events?historyLimit=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	event, payload := readSSEEvent(t, reader)
	if event != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", event)
	}
	snapshot, ok := payload["snapshot"].(map[string]any)
	if !ok || snapshot["planHistoryLimit"] != float64(2) || snapshot["historyLimitOverridden"] != true {
		t.Fatalf("snapshot metadata = %#v, want override snapshot policy", payload["snapshot"])
	}
	plans, ok := payload["plans"].([]any)
	if !ok || len(plans) != 1 {
		t.Fatalf("snapshot plans = %#v, want one plan", payload["plans"])
	}
	plan, ok := plans[0].(map[string]any)
	if !ok {
		t.Fatalf("snapshot plan = %#v, want object", plans[0])
	}
	if plan["historyIncluded"] != float64(2) || plan["historyTotal"] != float64(4) || plan["historyTruncated"] != true {
		t.Fatalf("snapshot plan = %+v, want overridden history window", plan)
	}
	items, ok := plan["history"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("snapshot plan history = %#v, want 2 entries", plan["history"])
	}
}

func apiRequest(t *testing.T, server *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	return apiRequestWithToken(t, server, method, path, body, "")
}

func apiRequestWithToken(t *testing.T, server *httptest.Server, method, path string, body any, token string) *http.Response {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, server.URL+path, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Kitchen-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("%s %s returned %d", method, path, resp.StatusCode)
	}
	return resp
}

func decodeResponse(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func apiRequestExpectStatus(t *testing.T, server *httptest.Server, method, path string, body any, wantStatus int) *http.Response {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, server.URL+path, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		var got map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&got)
		t.Fatalf("%s %s returned %d, want %d: %v", method, path, resp.StatusCode, wantStatus, got)
	}
	return resp
}

func seedQuestion(t *testing.T, k *Kitchen) {
	t.Helper()
	taskID := seedQuestionTarget(t, k)
	if _, err := k.RouteQuestion("w-1", taskID, "Need clarification"); err != nil {
		t.Fatal(err)
	}
}

func seedQuestionTarget(t *testing.T, k *Kitchen) string {
	t.Helper()
	planID, err := k.planStore.Create(StoredPlan{
		Plan: PlanRecord{
			PlanID:  "plan_api_question",
			Lineage: "parser-errors",
			Title:   "Question seed",
			Tasks: []PlanTask{{
				ID:         "t1",
				Title:      "Implement",
				Prompt:     "Implement",
				Complexity: ComplexityLow,
			}},
		},
		Affinity: AffinityRecord{
			PlannerWorkerID: "w-planner-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         planTaskRuntimeID(planID, "t1"),
		PlanID:     planID,
		Prompt:     "Implement",
		Complexity: string(ComplexityLow),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatal(err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) (string, map[string]any) {
	t.Helper()
	var event string
	var data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE line: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	if event == "" {
		t.Fatal("missing SSE event name")
	}
	var payload map[string]any
	if data != "" {
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("decode SSE payload %q: %v", data, err)
		}
	}
	return event, payload
}
