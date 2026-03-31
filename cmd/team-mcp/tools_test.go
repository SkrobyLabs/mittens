package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

// completeTestTask writes result files to the worker dir and calls CompleteTask.
func completeTestTask(pm *pool.PoolManager, workerID, taskID string, result pool.TaskResult, handover *pool.TaskHandover) {
	workerDir := filepath.Join(pm.StateDir(), "workers", workerID)
	os.MkdirAll(workerDir, 0755)
	if result.Summary != "" {
		os.WriteFile(filepath.Join(workerDir, "result.txt"), []byte(result.Summary), 0644)
	}
	if handover != nil {
		data, _ := json.Marshal(handover)
		os.WriteFile(filepath.Join(workerDir, "handover.json"), data, 0644)
	}
	pm.CompleteTask(workerID, taskID)
}

func newTestPM(t *testing.T) *pool.PoolManager {
	t.Helper()
	dir := t.TempDir()
	wal, err := pool.OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })
	return pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
}

func TestHandleSpawnWorker(t *testing.T) {
	pm := newTestPM(t)

	params := json.RawMessage(`{"role":"implementer"}`)
	result, err := handleSpawnWorker(pm, params)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	w, ok := m["worker"].(*pool.Worker)
	if !ok {
		t.Fatal("expected *pool.Worker in worker key")
	}
	if w.Status != pool.WorkerSpawning {
		t.Errorf("status = %q, want spawning", w.Status)
	}
	if w.Role != "implementer" {
		t.Errorf("role = %q, want implementer", w.Role)
	}
	if len(w.Token) != 64 {
		t.Errorf("expected 64-char hex worker token, got %q (len %d)", w.Token, len(w.Token))
	}
}

func TestHandleSpawnWorkerWithRouter(t *testing.T) {
	dir := t.TempDir()
	wal, err := pool.OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })

	router := pool.NewModelRouter(map[string]pool.ModelConfig{
		"planner": {Provider: "anthropic", Model: "opus-4", Adapter: "claude-code"},
	})
	pm := pool.NewPoolManager(pool.PoolConfig{
		MaxWorkers: 5,
		StateDir:   dir,
		Router:     router,
	}, wal, nil)

	params := json.RawMessage(`{"role":"planner"}`)
	_, err = handleSpawnWorker(pm, params)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHandleKillWorker(t *testing.T) {
	pm := newTestPM(t)

	// Spawn a worker first.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})

	params := json.RawMessage(`{"workerId":"w-1"}`)
	_, err := handleKillWorker(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	w, ok := pm.Worker("w-1")
	if !ok {
		t.Fatal("worker not found")
	}
	if w.Status != pool.WorkerDead {
		t.Errorf("status = %q, want dead", w.Status)
	}
}

func TestHandleKillWorkerMissingID(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{}`)
	_, err := handleKillWorker(pm, params)
	if err == nil {
		t.Fatal("expected error for missing workerId")
	}
}

func TestHandleEnqueueTask(t *testing.T) {
	pm := newTestPM(t)

	params := json.RawMessage(`{"prompt":"implement feature X","role":"implementer","priority":2}`)
	result, err := handleEnqueueTask(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["taskId"] == nil || m["taskId"] == "" {
		t.Error("expected non-empty taskId")
	}
}

func TestHandleEnqueueTaskMissingPrompt(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{"priority":1}`)
	_, err := handleEnqueueTask(pm, params)
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestHandleDispatchTask(t *testing.T) {
	pm := newTestPM(t)

	// Setup: spawn worker, register, enqueue task.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "do something", Priority: 1})

	params := json.RawMessage(`{"taskId":"t-1","workerId":"w-1"}`)
	_, err := handleDispatchTask(pm, params)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHandleGetStatus(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})

	result, err := handleGetStatus(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	sr, ok := result.(statusResult)
	if !ok {
		t.Fatal("expected statusResult")
	}
	if len(sr.Workers) != 1 {
		t.Errorf("workers = %d, want 1", len(sr.Workers))
	}
	if len(sr.Tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(sr.Tasks))
	}
	if sr.Tasks[0].ID != "t-1" {
		t.Errorf("task id = %q, want t-1", sr.Tasks[0].ID)
	}
	if sr.QueuedCount != 1 {
		t.Errorf("queued = %d, want 1", sr.QueuedCount)
	}
	if sr.PendingQuestions != 0 {
		t.Errorf("pendingQuestions = %d, want 0", sr.PendingQuestions)
	}
}

func TestHandleGetTaskResult(t *testing.T) {
	pm := newTestPM(t)
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})

	params := json.RawMessage(`{"taskId":"t-1"}`)
	result, err := handleGetTaskResult(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	// result is a taskWithHint struct wrapping *pool.Task; marshal and check ID
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "t-1" {
		t.Errorf("taskId = %q, want t-1", got.ID)
	}
}

func TestHandleGetTaskResultNotFound(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{"taskId":"nonexistent"}`)
	_, err := handleGetTaskResult(pm, params)
	if err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestHandleAnswerQuestion(t *testing.T) {
	pm := newTestPM(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	qid, _ := pm.AskQuestion("w-1", pool.Question{Question: "what should I do?", Blocking: true})

	params := json.RawMessage(`{"questionId":"` + qid + `","answer":"do X"}`)
	_, err := handleAnswerQuestion(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	q := pm.GetQuestion(qid)
	if !q.Answered {
		t.Error("question not answered")
	}
	if q.Answer != "do X" {
		t.Errorf("answer = %q, want 'do X'", q.Answer)
	}
}

func TestHandlePendingQuestions(t *testing.T) {
	pm := newTestPM(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.AskQuestion("w-1", pool.Question{Question: "q1", Blocking: true})

	result, err := handlePendingQuestions(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["count"] != 1 {
		t.Errorf("count = %v, want 1", m["count"])
	}
}

func TestHandleDispatchReviewAutoPickReviewer(t *testing.T) {
	pm := newTestPM(t)

	// Spawn implementer and reviewer.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-impl", Role: "implementer"})
	pm.RegisterWorker("w-impl", "")
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"})
	pm.RegisterWorker("w-rev", "")

	// Enqueue, dispatch, complete.
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})
	pm.DispatchTask("t-1", "w-impl")
	completeTestTask(pm, "w-impl", "t-1", pool.TaskResult{Summary: "done"}, nil)

	// Dispatch review without specifying reviewer — should auto-pick.
	params := json.RawMessage(`{"taskId":"t-1"}`)
	result, err := handleDispatchReview(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["reviewerId"] != "w-rev" {
		t.Errorf("reviewerId = %q, want w-rev", m["reviewerId"])
	}
}

func TestHandleSubmitPipeline(t *testing.T) {
	pm := newTestPM(t)

	params := json.RawMessage(`{
		"goal": "build feature X",
		"stages": [{"name":"plan","tasks":[{"id":"s0-t0","promptTmpl":"plan {{.Goal}}"}]}]
	}`)
	result, err := handleSubmitPipeline(pm, params)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["pipelineId"] == nil || m["pipelineId"] == "" {
		t.Error("expected non-empty pipelineId")
	}
}

func TestHandleSubmitPipelineMissingGoal(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{"stages":[{"name":"s","tasks":[{"id":"t","promptTmpl":"x"}]}]}`)
	_, err := handleSubmitPipeline(pm, params)
	if err == nil {
		t.Fatal("expected error for missing goal")
	}
}

func TestHandleSubmitPipelineMissingStages(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{"goal":"test"}`)
	_, err := handleSubmitPipeline(pm, params)
	if err == nil {
		t.Fatal("expected error for missing stages")
	}
}

func TestHandleCancelPipeline(t *testing.T) {
	pm := newTestPM(t)

	// Submit a pipeline first.
	pm.SubmitPipeline(pool.Pipeline{
		ID:     "pipe-1",
		Goal:   "test",
		Stages: []pool.Stage{{Name: "s0", Tasks: []pool.StageTask{{ID: "t0", PromptTmpl: "do"}}}},
	})

	params := json.RawMessage(`{"pipelineId":"pipe-1"}`)
	_, err := handleCancelPipeline(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	p, ok := pm.GetPipeline("pipe-1")
	if !ok {
		t.Fatal("pipeline not found")
	}
	if p.Status != pool.PipelineFailed {
		t.Errorf("status = %q, want failed", p.Status)
	}
}

func TestHandleCancelPipelineMissingID(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{}`)
	_, err := handleCancelPipeline(pm, params)
	if err == nil {
		t.Fatal("expected error for missing pipelineId")
	}
}

func TestHandleReportReview(t *testing.T) {
	pm := newTestPM(t)

	// Setup: spawn workers, enqueue, dispatch, complete, dispatch review.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-impl"})
	pm.RegisterWorker("w-impl", "")
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"})
	pm.RegisterWorker("w-rev", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})
	pm.DispatchTask("t-1", "w-impl")
	completeTestTask(pm, "w-impl", "t-1", pool.TaskResult{Summary: "done"}, nil)
	pm.DispatchReview("t-1", "w-rev")

	params := json.RawMessage(`{"taskId":"t-1","verdict":"pass"}`)
	_, err := handleReportReview(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	task, ok := pm.Task("t-1")
	if !ok {
		t.Fatal("task not found")
	}
	if task.Status != pool.TaskAccepted {
		t.Errorf("status = %q, want accepted", task.Status)
	}
}

func TestHandleReportReviewMissingVerdict(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{"taskId":"t-1"}`)
	_, err := handleReportReview(pm, params)
	if err == nil {
		t.Fatal("expected error for missing verdict")
	}
}

func TestHandleResolveEscalation(t *testing.T) {
	pm := newTestPM(t)

	// Setup: spawn workers, complete task, review with fail until escalation.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-impl"})
	pm.RegisterWorker("w-impl", "")
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"})
	pm.RegisterWorker("w-rev", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1, MaxReviews: 1})
	pm.DispatchTask("t-1", "w-impl")
	completeTestTask(pm, "w-impl", "t-1", pool.TaskResult{Summary: "done"}, nil)

	// Fail review to reach escalation (maxReviews=1, cycles=1 after review).
	pm.DispatchReview("t-1", "w-rev")
	pm.ReportReview("t-1", "fail", "bad", "major")

	// Task should be escalated now.
	task, _ := pm.Task("t-1")
	if task.Status != pool.TaskEscalated {
		t.Fatalf("expected escalated, got %q", task.Status)
	}

	params := json.RawMessage(`{"taskId":"t-1","action":"accept"}`)
	_, err := handleResolveEscalation(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	task, _ = pm.Task("t-1")
	if task.Status != pool.TaskAccepted {
		t.Errorf("status = %q, want accepted", task.Status)
	}
}

func TestHandleResolveEscalationMissingAction(t *testing.T) {
	pm := newTestPM(t)
	params := json.RawMessage(`{"taskId":"t-1"}`)
	_, err := handleResolveEscalation(pm, params)
	if err == nil {
		t.Fatal("expected error for missing action")
	}
}

func TestToolSchemaCount(t *testing.T) {
	if len(mcpTools) != 22 {
		t.Errorf("expected 22 tools, got %d", len(mcpTools))
	}
}

// --- Plan handler tests ---

func newTestPMWithPlans(t *testing.T) *pool.PoolManager {
	t.Helper()
	dir := t.TempDir()
	wal, err := pool.OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })
	ps := pool.NewPlanStore(filepath.Join(dir, "plans"))
	return pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 5, StateDir: dir, PlanStore: ps}, wal, nil)
}

func TestHandleCreatePlan(t *testing.T) {
	pm := newTestPMWithPlans(t)

	params := json.RawMessage(`{"title":"Test Plan","content":"# Steps\n1. Do X"}`)
	result, err := handleCreatePlan(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	if m["planId"] == nil || m["planId"] == "" {
		t.Error("expected non-empty planId")
	}
}

func TestHandleCreatePlan_MissingFields(t *testing.T) {
	pm := newTestPMWithPlans(t)

	_, err := handleCreatePlan(pm, json.RawMessage(`{"title":"only title"}`))
	if err == nil {
		t.Error("expected error for missing content")
	}
	_, err = handleCreatePlan(pm, json.RawMessage(`{"content":"only content"}`))
	if err == nil {
		t.Error("expected error for missing title")
	}
}

func TestHandleCreatePlan_NoPlanStore(t *testing.T) {
	pm := newTestPM(t) // no plan store configured
	_, err := handleCreatePlan(pm, json.RawMessage(`{"title":"T","content":"C"}`))
	if err == nil {
		t.Error("expected error when plans not configured")
	}
}

func TestHandleListPlans(t *testing.T) {
	pm := newTestPMWithPlans(t)

	// Create a few plans.
	handleCreatePlan(pm, json.RawMessage(`{"title":"A","content":"a"}`))
	handleCreatePlan(pm, json.RawMessage(`{"title":"B","content":"b"}`))

	result, err := handleListPlans(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	plans, ok := m["plans"].([]pool.Plan)
	if !ok {
		t.Fatal("expected []pool.Plan")
	}
	if len(plans) != 2 {
		t.Errorf("plans = %d, want 2", len(plans))
	}
}

func TestHandleListPlans_NoPlanStore(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleListPlans(pm, nil)
	if err == nil {
		t.Error("expected error when plans not configured")
	}
}

func TestHandleReadPlan(t *testing.T) {
	pm := newTestPMWithPlans(t)

	// Create a plan.
	res, _ := handleCreatePlan(pm, json.RawMessage(`{"title":"Read Me","content":"hello world"}`))
	planID := res.(map[string]any)["planId"].(string)

	result, err := handleReadPlan(pm, json.RawMessage(`{"planId":"`+planID+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	m := result.(map[string]any)
	if m["content"] != "hello world" {
		t.Errorf("content = %q, want 'hello world'", m["content"])
	}
}

func TestHandleReadPlan_MissingID(t *testing.T) {
	pm := newTestPMWithPlans(t)
	_, err := handleReadPlan(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing planId")
	}
}

func TestHandleClaimPlan(t *testing.T) {
	pm := newTestPMWithPlans(t)

	res, _ := handleCreatePlan(pm, json.RawMessage(`{"title":"Claim","content":"c"}`))
	planID := res.(map[string]any)["planId"].(string)

	// Set env for session ID.
	old := os.Getenv("MITTENS_SESSION_ID")
	os.Setenv("MITTENS_SESSION_ID", "sess-test")
	defer os.Setenv("MITTENS_SESSION_ID", old)

	result, err := handleClaimPlan(pm, json.RawMessage(`{"planId":"`+planID+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	m := result.(map[string]any)
	if m["ok"] != true {
		t.Error("expected ok=true")
	}
}

func TestHandleClaimPlan_MissingID(t *testing.T) {
	pm := newTestPMWithPlans(t)
	_, err := handleClaimPlan(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing planId")
	}
}

func TestHandleUpdatePlanProgress(t *testing.T) {
	pm := newTestPMWithPlans(t)

	res, _ := handleCreatePlan(pm, json.RawMessage(`{"title":"Progress","content":"c"}`))
	planID := res.(map[string]any)["planId"].(string)

	result, err := handleUpdatePlanProgress(pm, json.RawMessage(`{"planId":"`+planID+`","entry":"step 1 done"}`))
	if err != nil {
		t.Fatal(err)
	}

	m := result.(map[string]any)
	if m["ok"] != true {
		t.Error("expected ok=true")
	}
}

func TestHandleUpdatePlanProgress_MissingFields(t *testing.T) {
	pm := newTestPMWithPlans(t)
	_, err := handleUpdatePlanProgress(pm, json.RawMessage(`{"planId":"abc"}`))
	if err == nil {
		t.Error("expected error for missing entry")
	}
	_, err = handleUpdatePlanProgress(pm, json.RawMessage(`{"entry":"x"}`))
	if err == nil {
		t.Error("expected error for missing planId")
	}
}

func TestHandleCompletePlan(t *testing.T) {
	pm := newTestPMWithPlans(t)

	res, _ := handleCreatePlan(pm, json.RawMessage(`{"title":"Complete","content":"c"}`))
	planID := res.(map[string]any)["planId"].(string)

	result, err := handleCompletePlan(pm, json.RawMessage(`{"planId":"`+planID+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	m := result.(map[string]any)
	if m["ok"] != true {
		t.Error("expected ok=true")
	}
}

func TestHandleCompletePlan_MissingID(t *testing.T) {
	pm := newTestPMWithPlans(t)
	_, err := handleCompletePlan(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing planId")
	}
}

// --- get_task_output tests ---

func TestHandleGetTaskOutput(t *testing.T) {
	pm := newTestPM(t)

	// Enqueue, dispatch, complete.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", pool.TaskResult{Summary: "done"}, nil)

	// Write the output file AFTER CompleteTask (which may overwrite).
	outputDir := filepath.Join(pm.StateDir(), "outputs")
	os.MkdirAll(outputDir, 0755)
	os.WriteFile(filepath.Join(outputDir, "t-1.txt"), []byte("full output here"), 0644)

	result, err := handleGetTaskOutput(pm, json.RawMessage(`{"taskId":"t-1"}`))
	if err != nil {
		t.Fatal(err)
	}

	m := result.(map[string]any)
	if m["output"] != "full output here" {
		t.Errorf("output = %q, want 'full output here'", m["output"])
	}
}

func TestHandleGetTaskOutput_MissingID(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleGetTaskOutput(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing taskId")
	}
}

func TestHandleGetTaskOutput_NotFound(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleGetTaskOutput(pm, json.RawMessage(`{"taskId":"nonexistent"}`))
	if err == nil {
		t.Error("expected error for unknown task")
	}
}

func TestHandleGetTaskOutput_NoOutputFile(t *testing.T) {
	pm := newTestPM(t)
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})

	_, err := handleGetTaskOutput(pm, json.RawMessage(`{"taskId":"t-1"}`))
	if err == nil {
		t.Error("expected error when output file missing")
	}
}

// --- wait_for_task tests ---

func TestHandleWaitForTask_AlreadyDone(t *testing.T) {
	pm := newTestPM(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", pool.TaskResult{Summary: "done"}, nil)

	result, err := handleWaitForTask(pm, json.RawMessage(`{"taskId":"t-1","timeoutSec":1}`))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*pool.Task)
	if task.Status != pool.TaskCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
}

func TestHandleWaitForTask_MissingID(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleWaitForTask(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing taskId")
	}
}

func TestHandleWaitForTask_NotFound(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleWaitForTask(pm, json.RawMessage(`{"taskId":"nonexistent","timeoutSec":1}`))
	if err == nil {
		t.Error("expected error for unknown task")
	}
}

// --- check_session tests ---

func TestHandleCheckSession_MissingID(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleCheckSession(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing sessionId")
	}
}

func TestHandleCheckSession_NoHostAPI(t *testing.T) {
	pm := newTestPM(t) // no host API configured
	_, err := handleCheckSession(pm, json.RawMessage(`{"sessionId":"s-1"}`))
	if err == nil {
		t.Error("expected error when host API not configured")
	}
}

// --- Invalid enum / boundary tests ---

func TestHandleReportReview_InvalidVerdict(t *testing.T) {
	pm := newTestPM(t)

	// Setup with completed task.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-impl"})
	pm.RegisterWorker("w-impl", "")
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"})
	pm.RegisterWorker("w-rev", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1})
	pm.DispatchTask("t-1", "w-impl")
	completeTestTask(pm, "w-impl", "t-1", pool.TaskResult{Summary: "done"}, nil)
	pm.DispatchReview("t-1", "w-rev")

	// "invalid_verdict" is not a valid verdict.
	_, err := handleReportReview(pm, json.RawMessage(`{"taskId":"t-1","verdict":"invalid_verdict"}`))
	if err == nil {
		t.Error("expected error for invalid verdict value")
	}
}

func TestHandleResolveEscalation_InvalidAction(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-impl"})
	pm.RegisterWorker("w-impl", "")
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-rev", Role: "reviewer"})
	pm.RegisterWorker("w-rev", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "test", Priority: 1, MaxReviews: 1})
	pm.DispatchTask("t-1", "w-impl")
	completeTestTask(pm, "w-impl", "t-1", pool.TaskResult{Summary: "done"}, nil)
	pm.DispatchReview("t-1", "w-rev")
	pm.ReportReview("t-1", "fail", "bad", "major")

	task, _ := pm.Task("t-1")
	if task.Status != pool.TaskEscalated {
		t.Fatalf("expected escalated, got %q", task.Status)
	}

	_, err := handleResolveEscalation(pm, json.RawMessage(`{"taskId":"t-1","action":"invalid_action"}`))
	if err == nil {
		t.Error("expected error for invalid action value")
	}
}

func TestHandleDispatchTask_MissingFields(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleDispatchTask(pm, json.RawMessage(`{"taskId":"t-1"}`))
	if err == nil {
		t.Error("expected error for missing workerId")
	}
	_, err = handleDispatchTask(pm, json.RawMessage(`{"workerId":"w-1"}`))
	if err == nil {
		t.Error("expected error for missing taskId")
	}
}

func TestHandleGetTaskResult_MissingID(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleGetTaskResult(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing taskId")
	}
}

func TestHandleAnswerQuestion_MissingFields(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleAnswerQuestion(pm, json.RawMessage(`{"questionId":"q-1"}`))
	if err == nil {
		t.Error("expected error for missing answer")
	}
	_, err = handleAnswerQuestion(pm, json.RawMessage(`{"answer":"yes"}`))
	if err == nil {
		t.Error("expected error for missing questionId")
	}
}

func TestHandleDispatchReview_MissingTaskID(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleDispatchReview(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing taskId")
	}
}
