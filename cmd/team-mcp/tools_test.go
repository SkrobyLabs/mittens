package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func writeActivityRecords(t *testing.T, path string, records []pool.WorkerActivityRecord) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, record := range records {
		if err := enc.Encode(record); err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
	}
}

type capturingHostAPI struct {
	lastSpec pool.WorkerSpec
}

func (c *capturingHostAPI) SpawnWorker(_ context.Context, spec pool.WorkerSpec) (string, string, error) {
	c.lastSpec = spec
	return "test-container", "test-container-id", nil
}

func (c *capturingHostAPI) KillWorker(_ context.Context, _ string) error { return nil }

func (c *capturingHostAPI) ListContainers(_ context.Context, _ string) ([]pool.ContainerInfo, error) {
	return nil, nil
}

func (c *capturingHostAPI) CheckSession(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func TestEnqueueTaskSchema_DeclaresArrayItems(t *testing.T) {
	var schema map[string]any
	for _, td := range mcpTools {
		if td.Name == "enqueue_task" {
			schema = td.Schema
			break
		}
	}
	if schema == nil {
		t.Fatal("enqueue_task schema not found")
	}
	props := schema["properties"].(map[string]any)
	dependsOn := props["dependsOn"].(map[string]any)
	if dependsOn["type"] != "array" {
		t.Fatalf("dependsOn.type = %#v, want array", dependsOn["type"])
	}
	items, ok := dependsOn["items"].(map[string]any)
	if !ok {
		t.Fatal("dependsOn.items missing")
	}
	if items["type"] != "string" {
		t.Fatalf("dependsOn.items.type = %#v, want string", items["type"])
	}
}

func TestTopLevelToolSchemas_DisallowAdditionalProperties(t *testing.T) {
	for _, td := range mcpTools {
		if td.Schema["type"] != "object" {
			t.Fatalf("%s schema type = %#v, want object", td.Name, td.Schema["type"])
		}
		if td.Schema["additionalProperties"] != false {
			t.Fatalf("%s additionalProperties = %#v, want false", td.Name, td.Schema["additionalProperties"])
		}
	}
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

	hostAPI := &capturingHostAPI{}
	router := pool.NewModelRouter(map[string]pool.ModelConfig{
		"planner": {Provider: "openai", Model: "gpt-5.3-spark"},
	})
	pm := pool.NewPoolManager(pool.PoolConfig{
		MaxWorkers: 5,
		StateDir:   dir,
		Router:     router,
	}, wal, hostAPI)

	params := json.RawMessage(`{"role":"planner"}`)
	_, err = handleSpawnWorker(pm, params)
	if err != nil {
		t.Fatal(err)
	}
	if hostAPI.lastSpec.Provider != "openai" {
		t.Fatalf("SpawnWorker provider = %q, want openai", hostAPI.lastSpec.Provider)
	}
	if hostAPI.lastSpec.Model != "gpt-5.3-spark" {
		t.Fatalf("SpawnWorker model = %q, want gpt-5.3-spark", hostAPI.lastSpec.Model)
	}
	if hostAPI.lastSpec.Adapter != "openai-codex" {
		t.Fatalf("SpawnWorker adapter = %q, want openai-codex", hostAPI.lastSpec.Adapter)
	}
}

func TestHandleSpawnWorker_PersistsWorkerTokenInWorkerStateAndHostSpec(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "test.wal")
	wal, err := pool.OpenWAL(walPath)
	if err != nil {
		t.Fatal(err)
	}
	walClosed := false
	t.Cleanup(func() {
		if !walClosed {
			_ = wal.Close()
		}
	})

	hostAPI := &capturingHostAPI{}
	pm := pool.NewPoolManager(pool.PoolConfig{
		MaxWorkers: 5,
		StateDir:   dir,
	}, wal, hostAPI)

	result, err := handleSpawnWorker(pm, json.RawMessage(`{"role":"implementer"}`))
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
	if w.Token == "" {
		t.Fatal("expected worker token to be persisted in PoolManager state")
	}
	if hostAPI.lastSpec.Environment == nil {
		t.Fatal("expected hostAPI spawn spec environment to be populated")
	}
	if got := hostAPI.lastSpec.Environment["MITTENS_WORKER_TOKEN"]; got != w.Token {
		t.Fatalf("MITTENS_WORKER_TOKEN = %q, want %q", got, w.Token)
	}

	if err := wal.Close(); err != nil {
		t.Fatalf("close wal: %v", err)
	}
	walClosed = true

	recoveredWAL, err := pool.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("reopen wal: %v", err)
	}
	defer recoveredWAL.Close()

	recoveredPM, err := pool.RecoverPoolManager(pool.PoolConfig{
		MaxWorkers: 5,
		StateDir:   dir,
	}, recoveredWAL, nil)
	if err != nil {
		t.Fatalf("recover pool manager: %v", err)
	}

	recoveredWorker, ok := recoveredPM.Worker(w.ID)
	if !ok {
		t.Fatalf("recovered worker %q not found", w.ID)
	}
	if recoveredWorker.Token != w.Token {
		t.Fatalf("recovered worker token = %q, want %q", recoveredWorker.Token, w.Token)
	}
	if recoveredWorker.ContainerID != w.ContainerID {
		t.Fatalf("recovered container ID = %q, want %q", recoveredWorker.ContainerID, w.ContainerID)
	}
	if workerID, ok := recoveredPM.ValidateWorkerToken(w.Token); !ok || workerID != w.ID {
		t.Fatalf("ValidateWorkerToken(%q) = (%q, %v), want (%q, true)", w.Token, workerID, ok, w.ID)
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

func TestHandleGetStatus_EnrichesLiveWorkerRows(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "inspect repository", Priority: 1})
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "alive", &pool.WorkerActivity{
		Kind:    "status",
		Phase:   "started",
		Name:    "planning",
		Summary: "Inspecting repository state",
	}, ""); err != nil {
		t.Fatal(err)
	}

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-2", Role: "reviewer"})
	pm.RegisterWorker("w-2", "")
	qid, err := pm.AskQuestion("w-2", pool.Question{Question: "Need review policy", Blocking: true})
	if err != nil {
		t.Fatal(err)
	}

	result, err := handleGetStatus(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	sr, ok := result.(statusResult)
	if !ok {
		t.Fatal("expected statusResult")
	}
	if len(sr.Workers) != 2 {
		t.Fatalf("workers = %d, want 2", len(sr.Workers))
	}

	workers := map[string]statusWorkerView{}
	for _, worker := range sr.Workers {
		workers[worker.ID] = worker
	}

	if got := workers["w-1"].ActivitySummary; got != "Inspecting repository state" {
		t.Fatalf("w-1 activitySummary = %q, want %q", got, "Inspecting repository state")
	}
	if got := workers["w-1"].InspectionTool; got != "get_worker_activity" {
		t.Fatalf("w-1 inspectionTool = %q, want get_worker_activity", got)
	}
	if got := workers["w-2"].PendingQuestionID; got != qid {
		t.Fatalf("w-2 pendingQuestionId = %q, want %q", got, qid)
	}
	if got := workers["w-2"].PendingQuestion; got != "Need review policy" {
		t.Fatalf("w-2 pendingQuestion = %q, want %q", got, "Need review policy")
	}
	if got := workers["w-2"].ActivitySummary; got != "Need review policy" {
		t.Fatalf("w-2 activitySummary = %q, want %q", got, "Need review policy")
	}
	if got := workers["w-2"].InspectionTool; got != "get_worker_activity" {
		t.Fatalf("w-2 inspectionTool = %q, want get_worker_activity", got)
	}
}

func TestHandleGetStatus_NonBlockingQuestionDoesNotOverrideLiveActivity(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "inspect repository", Priority: 1})
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "alive", &pool.WorkerActivity{
		Kind:    "status",
		Phase:   "started",
		Name:    "planning",
		Summary: "Inspecting repository state",
	}, ""); err != nil {
		t.Fatal(err)
	}
	qid, err := pm.AskQuestion("w-1", pool.Question{Question: "Need preference on docs wording", Blocking: false})
	if err != nil {
		t.Fatal(err)
	}

	result, err := handleGetStatus(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	sr, ok := result.(statusResult)
	if !ok {
		t.Fatal("expected statusResult")
	}
	if len(sr.Workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(sr.Workers))
	}

	worker := sr.Workers[0]
	if got := worker.PendingQuestionID; got != qid {
		t.Fatalf("pendingQuestionId = %q, want %q", got, qid)
	}
	if got := worker.PendingQuestion; got != "Need preference on docs wording" {
		t.Fatalf("pendingQuestion = %q, want question metadata", got)
	}
	if got := worker.ActivitySummary; got != "Inspecting repository state" {
		t.Fatalf("activitySummary = %q, want live activity summary", got)
	}
	if got := worker.InspectionTool; got != "get_worker_activity" {
		t.Fatalf("inspectionTool = %q, want get_worker_activity", got)
	}
}

func TestHandleGetStatus_DoesNotSurfaceStaleIdleOrDeadActivity(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "finish work", Priority: 1})
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "alive", &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "Reading repository state",
	}, ""); err != nil {
		t.Fatal(err)
	}
	completeTestTask(pm, "w-1", "t-1", pool.TaskResult{Summary: "done"}, nil)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-2", Role: "reviewer"})
	pm.RegisterWorker("w-2", "")
	if err := pm.Heartbeat("w-2", "alive", &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "Reading review context",
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.KillWorker("w-2"); err != nil {
		t.Fatal(err)
	}

	for _, workerID := range []string{"w-1", "w-2"} {
		worker, ok := pm.Worker(workerID)
		if !ok {
			t.Fatalf("worker %s not found", workerID)
		}
		if worker.CurrentActivity != nil {
			t.Fatalf("%s currentActivity = %+v, want nil after terminal transition", workerID, worker.CurrentActivity)
		}
		if worker.CurrentTool != "" {
			t.Fatalf("%s currentTool = %q, want empty after terminal transition", workerID, worker.CurrentTool)
		}
		if worker.CurrentTaskID != "" {
			t.Fatalf("%s currentTaskID = %q, want empty after terminal transition", workerID, worker.CurrentTaskID)
		}
	}

	result, err := handleGetStatus(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	sr, ok := result.(statusResult)
	if !ok {
		t.Fatal("expected statusResult")
	}

	workers := map[string]statusWorkerView{}
	for _, worker := range sr.Workers {
		workers[worker.ID] = worker
	}

	for _, workerID := range []string{"w-1", "w-2"} {
		worker := workers[workerID]
		if worker.CurrentActivity != nil {
			t.Fatalf("%s status currentActivity = %+v, want nil", workerID, worker.CurrentActivity)
		}
		if worker.CurrentTool != "" {
			t.Fatalf("%s status currentTool = %q, want empty", workerID, worker.CurrentTool)
		}
		if worker.ActivitySummary != "" {
			t.Fatalf("%s activitySummary = %q, want empty", workerID, worker.ActivitySummary)
		}
		if worker.InspectionTool != "" {
			t.Fatalf("%s inspectionTool = %q, want empty", workerID, worker.InspectionTool)
		}
	}
}

func TestHandleGetStatus_DoesNotSurfaceLiveActivityForCanceledDispatchedTask(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")

	pipeID, err := pm.SubmitPipeline(pool.Pipeline{
		Goal: "cancel in-flight work",
		Stages: []pool.Stage{
			{
				Name:        "impl",
				Role:        "implementer",
				Fan:         pool.FanOut,
				AutoAdvance: true,
				Tasks: []pool.StageTask{
					{ID: "a", PromptTmpl: "do work"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	taskID := pipeID + "-s0-t0"
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "alive", &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "Reading active task context",
	}, ""); err != nil {
		t.Fatal(err)
	}

	if err := pm.CancelPipeline(pipeID); err != nil {
		t.Fatal(err)
	}

	worker, ok := pm.Worker("w-1")
	if !ok {
		t.Fatal("worker w-1 not found")
	}
	if worker.Status != pool.WorkerIdle {
		t.Fatalf("worker status = %q, want idle after cancellation", worker.Status)
	}
	if worker.CurrentTaskID != "" {
		t.Fatalf("worker currentTaskID = %q, want empty after cancellation", worker.CurrentTaskID)
	}
	if worker.CurrentActivity != nil {
		t.Fatalf("worker currentActivity = %+v, want nil after cancellation", worker.CurrentActivity)
	}
	if worker.CurrentTool != "" {
		t.Fatalf("worker currentTool = %q, want empty after cancellation", worker.CurrentTool)
	}

	result, err := handleGetStatus(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	sr, ok := result.(statusResult)
	if !ok {
		t.Fatal("expected statusResult")
	}
	if len(sr.Workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(sr.Workers))
	}

	got := sr.Workers[0]
	if got.CurrentActivity != nil {
		t.Fatalf("status currentActivity = %+v, want nil", got.CurrentActivity)
	}
	if got.CurrentTool != "" {
		t.Fatalf("status currentTool = %q, want empty", got.CurrentTool)
	}
	if got.ActivitySummary != "" {
		t.Fatalf("activitySummary = %q, want empty", got.ActivitySummary)
	}
	if got.InspectionTool != "" {
		t.Fatalf("inspectionTool = %q, want empty", got.InspectionTool)
	}
}

func TestHandleGetPoolState(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "planner"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-2", Role: "implementer"})
	pm.RegisterWorker("w-2", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "plan", Role: "planner", Priority: 1})
	pm.EnqueueTask(pool.TaskSpec{ID: "t-2", Prompt: "implement", Role: "implementer", Priority: 1})
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}

	result, err := handleGetPoolState(pm, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := result.(poolStateResult)
	if !ok {
		t.Fatal("expected poolStateResult")
	}
	if got.TotalWorkers != 2 {
		t.Fatalf("totalWorkers = %d, want 2", got.TotalWorkers)
	}
	if got.AliveWorkers != 2 {
		t.Fatalf("aliveWorkers = %d, want 2", got.AliveWorkers)
	}
	if got.MaxWorkers != 5 {
		t.Fatalf("maxWorkers = %d, want 5", got.MaxWorkers)
	}
	if got.IdleWorkers != 1 {
		t.Fatalf("idleWorkers = %d, want 1", got.IdleWorkers)
	}
	if got.WorkingWorkers != 1 {
		t.Fatalf("workingWorkers = %d, want 1", got.WorkingWorkers)
	}
	if got.QueuedTasks != 1 {
		t.Fatalf("queuedTasks = %d, want 1", got.QueuedTasks)
	}
	if got.ActiveTasks != 1 {
		t.Fatalf("activeTasks = %d, want 1", got.ActiveTasks)
	}
	if got.TerminalTasks != 0 {
		t.Fatalf("terminalTasks = %d, want 0", got.TerminalTasks)
	}
	if got.WorkersByRole["planner"] != 1 || got.WorkersByRole["implementer"] != 1 {
		t.Fatalf("workersByRole = %#v", got.WorkersByRole)
	}
	if got.IdleWorkersByRole["implementer"] != 1 {
		t.Fatalf("idleWorkersByRole = %#v", got.IdleWorkersByRole)
	}
}

func TestHandleGetWorkerActivity(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{
		ID:       "t-1",
		Prompt:   strings.Repeat("implement carefully ", 20),
		Role:     "implementer",
		Priority: 1,
	})
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "alive", &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "Reading current implementation",
	}, ""); err != nil {
		t.Fatal(err)
	}
	qid, err := pm.AskQuestion("w-1", pool.Question{Question: "Need signoff on approach", Blocking: true})
	if err != nil {
		t.Fatal(err)
	}

	workerDir := filepath.Join(pm.StateDir(), "workers", "w-1")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "task.md"), []byte("---\ntaskId: t-1\n---\n\nwork item details"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "result.txt"), []byte("partial output"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "handover.json"), []byte(`{"summary":"handover"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "error.txt"), []byte("temporary failure"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeActivityRecords(t, filepath.Join(workerDir, pool.WorkerActivityArchiveFile), []pool.WorkerActivityRecord{
		{
			RecordedAt: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
			TaskID:     "t-0",
			Activity: pool.WorkerActivity{
				Kind:    "tool",
				Phase:   "completed",
				Name:    "Glob",
				Summary: "Scanned docs",
			},
		},
	})
	writeActivityRecords(t, filepath.Join(workerDir, pool.WorkerActivityLogFile), []pool.WorkerActivityRecord{
		{
			RecordedAt: time.Date(2026, 4, 1, 10, 1, 0, 0, time.UTC),
			TaskID:     "t-1",
			Activity: pool.WorkerActivity{
				Kind:    "tool",
				Phase:   "started",
				Name:    "Read",
				Summary: "Reading current implementation",
			},
		},
		{
			RecordedAt: time.Date(2026, 4, 1, 10, 2, 0, 0, time.UTC),
			TaskID:     "t-1",
			Activity: pool.WorkerActivity{
				Kind:    "status",
				Phase:   "completed",
				Name:    "response",
				Summary: "Prepared draft patch",
			},
		},
	})

	result, err := handleGetWorkerActivity(pm, json.RawMessage(`{"workerId":"w-1"}`))
	if err != nil {
		t.Fatal(err)
	}

	got, ok := result.(workerActivityResult)
	if !ok {
		t.Fatal("expected workerActivityResult")
	}
	if got.Worker.ID != "w-1" {
		t.Fatalf("worker.id = %q, want w-1", got.Worker.ID)
	}
	if got.Worker.CurrentActivity == nil || got.Worker.CurrentActivity.Name != "Read" {
		t.Fatalf("worker currentActivity = %+v, want tool Read", got.Worker.CurrentActivity)
	}
	if got.Worker.PendingQuestionID != qid {
		t.Fatalf("worker pendingQuestionId = %q, want %q", got.Worker.PendingQuestionID, qid)
	}
	if got.PendingQuestion == nil || got.PendingQuestion.ID != qid {
		t.Fatalf("pendingQuestion = %+v, want %q", got.PendingQuestion, qid)
	}
	if got.Task == nil || got.Task.ID != "t-1" {
		t.Fatalf("task = %+v, want t-1", got.Task)
	}
	if got.Task.PromptPreview == "" {
		t.Fatal("task promptPreview should be populated")
	}
	if !got.Task.PromptTruncated {
		t.Fatal("task promptPreview should be truncated for long prompts")
	}
	if got.Artifacts == nil {
		t.Fatal("artifacts should be populated")
	}
	if !got.Artifacts.HasTaskFile || !got.Artifacts.HasResultFile || !got.Artifacts.HasHandoverFile || !got.Artifacts.HasErrorFile {
		t.Fatalf("artifacts = %+v, want all file markers", got.Artifacts)
	}
	if got.Artifacts.TaskPreview == "" {
		t.Fatal("taskPreview should be populated")
	}
	if got.Artifacts.ErrorPreview != "temporary failure" {
		t.Fatalf("errorPreview = %q, want %q", got.Artifacts.ErrorPreview, "temporary failure")
	}
	if len(got.RecentActivity) != 3 {
		t.Fatalf("recentActivity len = %d, want 3", len(got.RecentActivity))
	}
	if got.RecentActivity[0].TaskID != "t-0" || got.RecentActivity[0].Activity == nil || got.RecentActivity[0].Activity.Name != "Glob" {
		t.Fatalf("recentActivity[0] = %+v, want archive entry", got.RecentActivity[0])
	}
	if got.RecentActivity[2].Activity == nil || got.RecentActivity[2].Activity.Summary != "Prepared draft patch" {
		t.Fatalf("recentActivity[2] = %+v, want latest current-log entry", got.RecentActivity[2])
	}
}

func TestHandleGetWorkerActivity_SuppressesStaleArtifactsForIdleWorker(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "finish work", Priority: 1})
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "alive", &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "Reading active task context",
	}, ""); err != nil {
		t.Fatal(err)
	}

	workerDir := filepath.Join(pm.StateDir(), "workers", "w-1")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "task.md"), []byte("---\ntaskId: t-1\n---\n\nstale work item details"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "error.txt"), []byte("stale failure"), 0o644); err != nil {
		t.Fatal(err)
	}
	completeTestTask(pm, "w-1", "t-1", pool.TaskResult{Summary: "done"}, nil)

	result, err := handleGetWorkerActivity(pm, json.RawMessage(`{"workerId":"w-1"}`))
	if err != nil {
		t.Fatal(err)
	}

	got, ok := result.(workerActivityResult)
	if !ok {
		t.Fatal("expected workerActivityResult")
	}
	if got.Worker.Status != pool.WorkerIdle {
		t.Fatalf("worker.status = %q, want idle", got.Worker.Status)
	}
	if got.Worker.ActivitySummary != "" {
		t.Fatalf("worker.activitySummary = %q, want empty", got.Worker.ActivitySummary)
	}
	if got.Worker.InspectionTool != "" {
		t.Fatalf("worker.inspectionTool = %q, want empty", got.Worker.InspectionTool)
	}
	if got.Task != nil {
		t.Fatalf("task = %+v, want nil for idle worker", got.Task)
	}
	if got.Artifacts != nil {
		t.Fatalf("artifacts = %+v, want nil for idle worker with stale files", got.Artifacts)
	}
}

func TestHandleGetWorkerActivity_ShowsRecentHistoryForIdleWorker(t *testing.T) {
	pm := newTestPM(t)

	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")

	workerDir := filepath.Join(pm.StateDir(), "workers", "w-1")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var archive []pool.WorkerActivityRecord
	for i := 0; i < 5; i++ {
		archive = append(archive, pool.WorkerActivityRecord{
			RecordedAt: time.Date(2026, 4, 1, 9, i, 0, 0, time.UTC),
			TaskID:     "t-archive",
			Activity: pool.WorkerActivity{
				Kind:    "tool",
				Phase:   "completed",
				Name:    "Archive",
				Summary: "archive entry",
			},
		})
	}
	writeActivityRecords(t, filepath.Join(workerDir, pool.WorkerActivityArchiveFile), archive)

	var current []pool.WorkerActivityRecord
	for i := 0; i < 6; i++ {
		current = append(current, pool.WorkerActivityRecord{
			RecordedAt: time.Date(2026, 4, 1, 10, i, 0, 0, time.UTC),
			TaskID:     "t-current",
			Activity: pool.WorkerActivity{
				Kind:    "tool",
				Phase:   "started",
				Name:    fmt.Sprintf("Tool-%d", i),
				Summary: fmt.Sprintf("summary-%d", i),
			},
		})
	}
	writeActivityRecords(t, filepath.Join(workerDir, pool.WorkerActivityLogFile), current)

	result, err := handleGetWorkerActivity(pm, json.RawMessage(`{"workerId":"w-1"}`))
	if err != nil {
		t.Fatal(err)
	}

	got, ok := result.(workerActivityResult)
	if !ok {
		t.Fatal("expected workerActivityResult")
	}
	if got.Artifacts != nil {
		t.Fatalf("artifacts = %+v, want nil for idle worker without live artifacts", got.Artifacts)
	}
	if len(got.RecentActivity) != workerActivityHistoryTail {
		t.Fatalf("recentActivity len = %d, want %d", len(got.RecentActivity), workerActivityHistoryTail)
	}
	if got.RecentActivity[0].Activity == nil || got.RecentActivity[0].Activity.Name != "Archive" {
		t.Fatalf("recentActivity[0] = %+v, want oldest kept archive entry", got.RecentActivity[0])
	}
	if got.RecentActivity[len(got.RecentActivity)-1].Activity == nil || got.RecentActivity[len(got.RecentActivity)-1].Activity.Name != "Tool-5" {
		t.Fatalf("recentActivity last = %+v, want newest current entry", got.RecentActivity[len(got.RecentActivity)-1])
	}
}

func TestHandleGetWorkerActivityMissingID(t *testing.T) {
	pm := newTestPM(t)
	_, err := handleGetWorkerActivity(pm, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing workerId")
	}
}

func TestHandleGetTaskResult(t *testing.T) {
	pm := newTestPM(t)
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: strings.Repeat("x", 300), Priority: 1})

	params := json.RawMessage(`{"taskId":"t-1"}`)
	result, err := handleGetTaskResult(pm, params)
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID              string `json:"id"`
		Prompt          string `json:"prompt"`
		PromptPreview   string `json:"promptPreview"`
		PromptTruncated bool   `json:"promptTruncated"`
		OutputHint      string `json:"_outputHint"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "t-1" {
		t.Errorf("taskId = %q, want t-1", got.ID)
	}
	if got.Prompt != "" {
		t.Fatal("get_task_result should omit full prompt by default")
	}
	if got.PromptPreview == "" {
		t.Fatal("get_task_result should include promptPreview by default")
	}
	if !got.PromptTruncated {
		t.Fatal("get_task_result should mark long prompt previews as truncated")
	}
	if got.OutputHint != "" {
		t.Fatal("get_task_result should not include output hints")
	}
}

func TestHandleGetTaskResult_IncludePrompt(t *testing.T) {
	pm := newTestPM(t)
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "full prompt", Priority: 1})

	result, err := handleGetTaskResult(pm, json.RawMessage(`{"taskId":"t-1","includePrompt":true}`))
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Prompt        string `json:"prompt"`
		PromptPreview string `json:"promptPreview"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "full prompt" {
		t.Fatalf("prompt = %q, want full prompt", got.Prompt)
	}
	if got.PromptPreview != "" {
		t.Fatal("includePrompt response should omit promptPreview")
	}
}

func TestHandleGetTaskState(t *testing.T) {
	pm := newTestPM(t)
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: strings.Repeat("x", 300), Role: "implementer", Priority: 1})

	result, err := handleGetTaskState(pm, json.RawMessage(`{"taskId":"t-1"}`))
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Role          string `json:"role"`
		Prompt        string `json:"prompt"`
		PromptPreview string `json:"promptPreview"`
		HasOutput     bool   `json:"hasOutput"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "t-1" {
		t.Fatalf("id = %q, want t-1", got.ID)
	}
	if got.Status != pool.TaskQueued {
		t.Fatalf("status = %q, want queued", got.Status)
	}
	if got.Role != "implementer" {
		t.Fatalf("role = %q, want implementer", got.Role)
	}
	if got.Prompt != "" || got.PromptPreview != "" {
		t.Fatal("get_task_state should not include prompt fields")
	}
	if got.HasOutput {
		t.Fatal("get_task_state should not include output availability flags")
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
	if len(mcpTools) != 25 {
		t.Errorf("expected 25 tools, got %d", len(mcpTools))
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
