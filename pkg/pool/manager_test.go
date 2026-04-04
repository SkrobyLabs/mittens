package pool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestPoolManager(t *testing.T) *PoolManager {
	t.Helper()
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })
	return NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
}

// completeTestTask writes result files to the worker dir and calls CompleteTask.
// This helper bridges the old test pattern (passing result/handover directly)
// with the new filesystem-based CompleteTask.
func completeTestTask(pm *PoolManager, workerID, taskID string, result TaskResult, handover *TaskHandover) error {
	workerDir := WorkerStateDir(pm.cfg.StateDir, workerID)
	os.MkdirAll(workerDir, 0755)
	if result.Summary != "" {
		os.WriteFile(filepath.Join(workerDir, WorkerResultFile), []byte(result.Summary), 0644)
	}
	if handover != nil {
		data, _ := json.Marshal(handover)
		os.WriteFile(filepath.Join(workerDir, WorkerHandoverFile), data, 0644)
	}
	return pm.CompleteTask(workerID, taskID)
}

// --- Worker lifecycle tests ---

func TestSpawnAndRegisterWorker(t *testing.T) {
	pm := newTestPoolManager(t)

	w, err := pm.SpawnWorker(WorkerSpec{ID: "w-1", Role: "impl"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != WorkerSpawning {
		t.Errorf("status = %q, want spawning", w.Status)
	}
	if w.Role != "impl" {
		t.Errorf("role = %q, want impl", w.Role)
	}

	if err := pm.RegisterWorker("w-1", "container-abc"); err != nil {
		t.Fatal(err)
	}
	w2, ok := pm.Worker("w-1")
	if !ok {
		t.Fatal("worker not found after register")
	}
	if w2.Status != WorkerIdle {
		t.Errorf("status = %q, want idle", w2.Status)
	}
	if w2.ContainerID != "container-abc" {
		t.Errorf("containerId = %q, want container-abc", w2.ContainerID)
	}
}

func TestSpawnWorkerAutoID(t *testing.T) {
	pm := newTestPoolManager(t)
	w, err := pm.SpawnWorker(WorkerSpec{Role: "general"})
	if err != nil {
		t.Fatal(err)
	}
	if w.ID == "" {
		t.Error("auto-generated ID should not be empty")
	}
}

func TestSpawnWorkerDuplicate(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	_, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	if err == nil {
		t.Error("expected error for duplicate worker ID")
	}
}

func TestSpawnWorkerMaxCapacity(t *testing.T) {
	pm := newTestPoolManager(t)
	for i := 0; i < 5; i++ {
		if _, err := pm.SpawnWorker(WorkerSpec{}); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
	_, err := pm.SpawnWorker(WorkerSpec{})
	if err == nil {
		t.Error("expected max workers error")
	}
}

func TestRegisterWorkerNotFound(t *testing.T) {
	pm := newTestPoolManager(t)
	err := pm.RegisterWorker("nope", "")
	if err == nil {
		t.Error("expected error for non-existent worker")
	}
}

func TestRegisterWorkerWrongState(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	err := pm.RegisterWorker("w-1", "") // already idle
	if err == nil {
		t.Error("expected error for already-registered worker")
	}
}

func TestKillWorker(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	w, _ := pm.Worker("w-1")
	token := w.Token
	if err := pm.KillWorker("w-1"); err != nil {
		t.Fatal(err)
	}
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("status = %q, want dead", w.Status)
	}
	if w.Token != "" {
		t.Errorf("token = %q, want empty", w.Token)
	}
	if owner, ok := pm.ValidateWorkerToken(token); ok || owner != "" {
		t.Fatalf("ValidateWorkerToken(old token) = (%q, %v), want revoked", owner, ok)
	}
}

func TestKillWorkerAlreadyDead(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.KillWorker("w-1")
	if err := pm.KillWorker("w-1"); err != nil {
		t.Errorf("killing already-dead worker should be no-op, got: %v", err)
	}
}

func TestMarkDead(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	w, _ := pm.Worker("w-1")
	token := w.Token
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatal(err)
	}
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("status = %q, want dead", w.Status)
	}
	if w.Token != "" {
		t.Errorf("token = %q, want empty", w.Token)
	}
	if owner, ok := pm.ValidateWorkerToken(token); ok || owner != "" {
		t.Fatalf("ValidateWorkerToken(old token) = (%q, %v), want revoked", owner, ok)
	}
}

func TestMarkDeadClearsRecycleFlag(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.RequestRecycle("w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatal(err)
	}
	if pm.RecycleRequested("w-1") {
		t.Fatal("recycle flag should be cleared when worker dies")
	}
}

func TestFailCompletedTask(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatal(err)
	}
	taskID, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do work", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatal(err)
	}

	workerDir := WorkerStateDir(pm.cfg.StateDir, "w-1")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, WorkerResultFile), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatal(err)
	}
	if err := pm.FailCompletedTask(taskID, "merge conflicts: shared.txt"); err != nil {
		t.Fatal(err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != TaskFailed {
		t.Fatalf("status = %q, want %q", task.Status, TaskFailed)
	}
	if task.Result == nil || task.Result.Error != "merge conflicts: shared.txt" {
		t.Fatalf("result = %+v, want merge conflict error", task.Result)
	}
	if task.Result.Summary != "done" {
		t.Fatalf("summary = %q, want done", task.Result.Summary)
	}
}

func TestReviveFailedTask(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatal(err)
	}
	taskID, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do work", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.FailTask("w-1", taskID, "merge conflicts: shared.txt"); err != nil {
		t.Fatal(err)
	}
	if err := pm.ReviveFailedTask(taskID, true); err != nil {
		t.Fatal(err)
	}

	task, ok := pm.Task(taskID)
	if !ok {
		t.Fatalf("task %q not found", taskID)
	}
	if task.Status != TaskQueued {
		t.Fatalf("status = %q, want %q", task.Status, TaskQueued)
	}
	if task.RetryCount != 1 {
		t.Fatalf("retryCount = %d, want 1", task.RetryCount)
	}
	if !task.RequireFreshWorker {
		t.Fatal("expected revived task to require a fresh worker")
	}
	if task.WorkerID != "" {
		t.Fatalf("workerID = %q, want empty", task.WorkerID)
	}
	if task.Result != nil {
		t.Fatalf("result = %+v, want nil", task.Result)
	}
}

func TestHeartbeat(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	if err := pm.Heartbeat("w-1", "idle", nil, ""); err != nil {
		t.Fatal(err)
	}
	w, _ := pm.Worker("w-1")
	if w.LastHeartbeat.IsZero() {
		t.Error("heartbeat timestamp should be set")
	}
}

func TestHeartbeatRejectsDeadWorker(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatal(err)
	}
	if err := pm.Heartbeat("w-1", "idle", nil, ""); err == nil {
		t.Fatal("expected heartbeat for dead worker to fail")
	}
}

func TestHeartbeatCurrentActivity(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	activity := &WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "README.md",
	}
	if err := pm.Heartbeat("w-1", "idle", activity, ""); err != nil {
		t.Fatal(err)
	}
	w, _ := pm.Worker("w-1")
	if w.CurrentActivity == nil {
		t.Fatal("CurrentActivity should be set")
	}
	if *w.CurrentActivity != *activity {
		t.Fatalf("CurrentActivity = %+v, want %+v", *w.CurrentActivity, *activity)
	}
	if w.CurrentTool != "Read" {
		t.Errorf("CurrentTool = %q, want Read", w.CurrentTool)
	}
}

func TestHeartbeatLegacyCurrentTool(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	if err := pm.Heartbeat("w-1", "idle", nil, "Read"); err != nil {
		t.Fatal(err)
	}
	w, _ := pm.Worker("w-1")
	if w.CurrentActivity == nil {
		t.Fatal("CurrentActivity should be synthesized from currentTool")
	}
	if w.CurrentActivity.Kind != "tool" || w.CurrentActivity.Phase != "started" || w.CurrentActivity.Name != "Read" {
		t.Fatalf("CurrentActivity = %+v, want synthesized tool activity", *w.CurrentActivity)
	}
	if w.CurrentTool != "Read" {
		t.Errorf("CurrentTool = %q, want Read", w.CurrentTool)
	}
}

func TestHeartbeatStatusActivityClearsLegacyCurrentTool(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	if err := pm.Heartbeat("w-1", "idle", nil, "Read"); err != nil {
		t.Fatal(err)
	}
	statusActivity := &WorkerActivity{
		Kind:    "status",
		Phase:   "started",
		Name:    "planning",
		Summary: "Reviewing task context",
	}
	if err := pm.Heartbeat("w-1", "idle", statusActivity, ""); err != nil {
		t.Fatal(err)
	}
	w, _ := pm.Worker("w-1")
	if w.CurrentActivity == nil {
		t.Fatal("CurrentActivity should be set")
	}
	if *w.CurrentActivity != *statusActivity {
		t.Fatalf("CurrentActivity = %+v, want %+v", *w.CurrentActivity, *statusActivity)
	}
	if w.CurrentTool != "" {
		t.Errorf("CurrentTool = %q, want empty", w.CurrentTool)
	}
}

func TestHeartbeatCompletedToolActivityClearsCurrentTool(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	if err := pm.Heartbeat("w-1", "idle", nil, "Read"); err != nil {
		t.Fatal(err)
	}
	completedActivity := &WorkerActivity{
		Kind:    "tool",
		Phase:   "completed",
		Name:    "Read",
		Summary: "finished README.md",
	}
	if err := pm.Heartbeat("w-1", "idle", completedActivity, ""); err != nil {
		t.Fatal(err)
	}
	w, _ := pm.Worker("w-1")
	if w.CurrentActivity == nil {
		t.Fatal("CurrentActivity should be set")
	}
	if *w.CurrentActivity != *completedActivity {
		t.Fatalf("CurrentActivity = %+v, want %+v", *w.CurrentActivity, *completedActivity)
	}
	if w.CurrentTool != "" {
		t.Errorf("CurrentTool = %q, want empty", w.CurrentTool)
	}
}

func TestHeartbeatNotFound(t *testing.T) {
	pm := newTestPoolManager(t)
	err := pm.Heartbeat("nope", "idle", nil, "")
	if err == nil {
		t.Error("expected error for non-existent worker")
	}
}

// --- Task lifecycle tests ---

func TestEnqueueAndDispatchTask(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	tid, err := pm.EnqueueTask(TaskSpec{Prompt: "build it", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	if tid == "" {
		t.Error("task ID should not be empty")
	}

	task, ok := pm.Task(tid)
	if !ok {
		t.Fatal("task not found")
	}
	if task.Status != TaskQueued {
		t.Errorf("status = %q, want queued", task.Status)
	}

	if err := pm.DispatchTask(tid, "w-1"); err != nil {
		t.Fatal(err)
	}
	task, _ = pm.Task(tid)
	if task.Status != TaskDispatched {
		t.Errorf("status = %q, want dispatched", task.Status)
	}
}

func TestEnqueueTaskDuplicate(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	_, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "y"})
	if err == nil {
		t.Error("expected error for duplicate task ID")
	}
}

func TestEnqueueTaskCircularDep(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.EnqueueTask(TaskSpec{ID: "t-a", Prompt: "a", DependsOn: []string{"t-b"}})
	_, err := pm.EnqueueTask(TaskSpec{ID: "t-b", Prompt: "b", DependsOn: []string{"t-a"}})
	if err == nil {
		t.Error("expected circular dependency error")
	}
}

func TestDispatchTaskWrongState(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	// Try to dispatch again — already dispatched.
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")
	err := pm.DispatchTask("t-1", "w-2")
	if err == nil {
		t.Error("expected error dispatching non-queued task")
	}
}

func TestDispatchTaskWorkerNotIdle(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1") // now working

	pm.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "y"})
	err := pm.DispatchTask("t-2", "w-1")
	if err == nil {
		t.Error("expected error dispatching to busy worker")
	}
}

func TestDispatchNext(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-high", Prompt: "high", Priority: 1})
	pm.EnqueueTask(TaskSpec{ID: "t-low", Prompt: "low", Priority: 100})

	task, err := pm.DispatchNext("w-1")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("expected task")
	}
	if task.ID != "t-high" {
		t.Errorf("dispatched %q, want t-high", task.ID)
	}
}

func TestDispatchNextReturnsNilWhenEmpty(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	task, err := pm.DispatchNext("w-1")
	if err != nil {
		t.Fatal(err)
	}
	if task != nil {
		t.Error("expected nil task when queue is empty")
	}
}

func TestDispatchNextWithDeps(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")

	pm.EnqueueTask(TaskSpec{ID: "t-dep", Prompt: "dep", Priority: 100})
	pm.EnqueueTask(TaskSpec{ID: "t-main", Prompt: "main", Priority: 1, DependsOn: []string{"t-dep"}})

	// t-main has higher priority but unsatisfied dep; should get t-dep.
	task, _ := pm.DispatchNext("w-1")
	if task.ID != "t-dep" {
		t.Errorf("dispatched %q, want t-dep (t-main has unsatisfied dep)", task.ID)
	}

	// Complete the dep.
	completeTestTask(pm, "w-1", "t-dep", TaskResult{Summary: "done"}, nil)

	// Now t-main should be available.
	task, _ = pm.DispatchNext("w-2")
	if task == nil || task.ID != "t-main" {
		t.Errorf("expected t-main after dep satisfied, got %v", task)
	}
}

func TestPollTask(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	// No task assigned.
	if pm.PollTask("w-1") != nil {
		t.Error("expected nil before dispatch")
	}

	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	task := pm.PollTask("w-1")
	if task == nil || task.ID != "t-1" {
		t.Errorf("poll = %v, want t-1", task)
	}
}

func TestCompleteTask(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	handover := &TaskHandover{Summary: "implemented it", KeyDecisions: []string{"used stdlib"}}
	err := completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done", FilesChanged: []string{"a.go"}}, handover)
	if err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task("t-1")
	if task.Status != TaskCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.Handover == nil || task.Handover.Summary != "implemented it" {
		t.Error("handover not stored")
	}

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle", w.Status)
	}
}

func TestCompleteTaskWrongWorker(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	err := completeTestTask(pm, "w-2", "t-1", TaskResult{}, nil)
	if err == nil {
		t.Error("expected error when wrong worker completes task")
	}
}

func TestFailTask(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	if err := pm.FailTask("w-1", "t-1", "build failed"); err != nil {
		t.Fatal(err)
	}
	task, _ := pm.Task("t-1")
	if task.Status != TaskFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	w, _ := pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle", w.Status)
	}
}

func TestDispatchTaskWorkerNotFound(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	err := pm.DispatchTask("t-1", "ghost")
	if err == nil {
		t.Error("expected error dispatching to non-existent worker")
	}
}

func TestDispatchTaskTaskNotFound(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	err := pm.DispatchTask("ghost-task", "w-1")
	if err == nil {
		t.Error("expected error dispatching non-existent task")
	}
}

func TestCancelTaskQueuedRemovesFromDispatchQueue(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := pm.CancelTask("t-1"); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task("t-1")
	if task.Status != TaskCanceled {
		t.Fatalf("task status = %q, want canceled", task.Status)
	}

	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatal(err)
	}
	next, err := pm.DispatchNext("w-1")
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("DispatchNext returned %+v, want nil for canceled queued task", next)
	}
}

func TestCancelTaskReviewingReleasesReviewer(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("impl-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("rev-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x", Priority: 1, MaxReviews: 3}); err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchTask("t-1", "impl-1"); err != nil {
		t.Fatal(err)
	}
	if err := completeTestTask(pm, "impl-1", "t-1", TaskResult{Summary: "done"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchReview("t-1", "rev-1"); err != nil {
		t.Fatal(err)
	}

	if err := pm.CancelTask("t-1"); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task("t-1")
	if task.Status != TaskCanceled {
		t.Fatalf("task status = %q, want canceled", task.Status)
	}
	reviewer, _ := pm.Worker("rev-1")
	if reviewer.Status != WorkerIdle {
		t.Fatalf("reviewer status = %q, want idle", reviewer.Status)
	}
	if reviewer.CurrentTaskID != "" {
		t.Fatalf("reviewer currentTaskID = %q, want empty", reviewer.CurrentTaskID)
	}
}

func TestCancelTaskKillsActiveWorkerWhenHostAPIPresent(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })

	hostAPI := &capturingHostAPI{}
	pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, hostAPI)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("w-1", "container-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchTask("t-1", "w-1"); err != nil {
		t.Fatal(err)
	}

	if err := pm.CancelTask("t-1"); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task("t-1")
	if task.Status != TaskCanceled {
		t.Fatalf("task status = %q, want canceled", task.Status)
	}
	worker, _ := pm.Worker("w-1")
	if worker.Status != WorkerDead {
		t.Fatalf("worker status = %q, want dead", worker.Status)
	}
	if len(hostAPI.killed) != 1 || hostAPI.killed[0] != "w-1" {
		t.Fatalf("killed workers = %+v, want [w-1]", hostAPI.killed)
	}
}

func TestKillWorkerFromIdleState(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	w, _ := pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Fatalf("precondition: status = %q, want idle", w.Status)
	}
	if err := pm.KillWorker("w-1"); err != nil {
		t.Fatal(err)
	}
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("status = %q, want dead", w.Status)
	}
}

func TestKillWorkerFromWorkingState(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")
	w, _ := pm.Worker("w-1")
	if w.Status != WorkerWorking {
		t.Fatalf("precondition: status = %q, want working", w.Status)
	}
	if err := pm.KillWorker("w-1"); err != nil {
		t.Fatal(err)
	}
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("status = %q, want dead", w.Status)
	}
	task, _ := pm.Task("t-1")
	if task.Status != TaskQueued {
		t.Fatalf("task status = %q, want queued", task.Status)
	}
	if task.WorkerID != "" {
		t.Fatalf("task workerId = %q, want empty", task.WorkerID)
	}
}

func TestKillWorkerNotFound(t *testing.T) {
	pm := newTestPoolManager(t)
	err := pm.KillWorker("ghost")
	if err == nil {
		t.Error("expected error killing non-existent worker")
	}
}

func TestMultipleWorkersIndependent(t *testing.T) {
	pm := newTestPoolManager(t)

	// Spawn and register 3 workers.
	for _, id := range []string{"w-1", "w-2", "w-3"} {
		pm.SpawnWorker(WorkerSpec{ID: id, Role: "impl"})
		pm.RegisterWorker(id, "")
	}

	// Enqueue 3 tasks.
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "task one"})
	pm.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "task two"})
	pm.EnqueueTask(TaskSpec{ID: "t-3", Prompt: "task three"})

	// Dispatch each task to a different worker.
	pm.DispatchTask("t-1", "w-1")
	pm.DispatchTask("t-2", "w-2")
	pm.DispatchTask("t-3", "w-3")

	// All workers should be working.
	for _, id := range []string{"w-1", "w-2", "w-3"} {
		w, _ := pm.Worker(id)
		if w.Status != WorkerWorking {
			t.Errorf("worker %s: status = %q, want working", id, w.Status)
		}
	}

	// Complete w-2's task first — only w-2 goes idle.
	completeTestTask(pm, "w-2", "t-2", TaskResult{Summary: "two done"}, nil)
	w1, _ := pm.Worker("w-1")
	w2, _ := pm.Worker("w-2")
	w3, _ := pm.Worker("w-3")
	if w1.Status != WorkerWorking {
		t.Errorf("w-1 should still be working, got %q", w1.Status)
	}
	if w2.Status != WorkerIdle {
		t.Errorf("w-2 should be idle after completion, got %q", w2.Status)
	}
	if w3.Status != WorkerWorking {
		t.Errorf("w-3 should still be working, got %q", w3.Status)
	}

	// Complete the rest.
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "one done"}, nil)
	completeTestTask(pm, "w-3", "t-3", TaskResult{Summary: "three done"}, nil)

	for _, id := range []string{"w-1", "w-2", "w-3"} {
		w, _ := pm.Worker(id)
		if w.Status != WorkerIdle {
			t.Errorf("worker %s: status = %q, want idle after all complete", id, w.Status)
		}
	}

	// Verify all tasks completed.
	for _, id := range []string{"t-1", "t-2", "t-3"} {
		task, _ := pm.Task(id)
		if task.Status != TaskCompleted {
			t.Errorf("task %s: status = %q, want completed", id, task.Status)
		}
	}
}

// --- Question lifecycle tests ---

func TestAskAndAnswerQuestion(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	qid, err := pm.AskQuestion("w-1", Question{
		TaskID:   "t-1",
		Question: "which database?",
		Blocking: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if qid == "" {
		t.Error("question ID should not be empty")
	}

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerBlocked {
		t.Errorf("worker status = %q, want blocked", w.Status)
	}

	pending := pm.PendingQuestions()
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}

	if err := pm.AnswerQuestion(qid, "postgres", "leader"); err != nil {
		t.Fatal(err)
	}

	w, _ = pm.Worker("w-1")
	if w.Status != WorkerWorking {
		t.Errorf("worker status = %q, want working after answer", w.Status)
	}

	pending = pm.PendingQuestions()
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 after answer", len(pending))
	}
}

func TestAskQuestionRejectsDeadWorker(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.AskQuestion("w-1", Question{Question: "still there?", Blocking: true}); err == nil {
		t.Fatal("expected dead worker question to fail")
	}
}

func TestAnswerQuestionAlreadyAnswered(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	qid, _ := pm.AskQuestion("w-1", Question{Question: "x", Blocking: false})
	pm.AnswerQuestion(qid, "y", "leader")

	err := pm.AnswerQuestion(qid, "z", "leader")
	if err == nil {
		t.Error("expected error for already-answered question")
	}
}

func TestDeleteTaskRejectsNonTerminalTask(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.DeleteTask("t-1"); err == nil {
		t.Fatal("expected delete of queued task to fail")
	}
}

func TestDeleteTaskRemovesTerminalTaskQuestionsAndOutput(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatal(err)
	}
	taskID, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchTask(taskID, "w-1"); err != nil {
		t.Fatal(err)
	}
	qid, err := pm.AskQuestion("w-1", Question{
		TaskID:   taskID,
		Question: "which database?",
		Blocking: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	workerDir := WorkerStateDir(pm.cfg.StateDir, "w-1")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, WorkerResultFile), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pm.CompleteTask("w-1", taskID); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(pm.cfg.StateDir, "outputs", "t-1.txt")
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected output side-file: %v", err)
	}

	sub, cancel := pm.SubscribeNotifications(1)
	defer cancel()
	if err := pm.DeleteTask(taskID); err != nil {
		t.Fatal(err)
	}

	if _, ok := pm.Task(taskID); ok {
		t.Fatal("task should be deleted")
	}
	if got := pm.GetQuestion(qid); got != nil {
		t.Fatalf("question = %+v, want deleted", got)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output side-file err = %v, want not exists", err)
	}
	select {
	case n := <-sub:
		if n.Type != "task_deleted" || n.ID != taskID {
			t.Fatalf("notification = %+v, want task_deleted for %s", n, taskID)
		}
	default:
		t.Fatal("expected delete notification")
	}
}

// --- Query method tests ---

func TestWorkersAndTasks(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})

	workers := pm.Workers()
	if len(workers) != 2 {
		t.Errorf("workers = %d, want 2", len(workers))
	}
	tasks := pm.Tasks()
	if len(tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(tasks))
	}
}

func TestAliveWorkers(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.KillWorker("w-2")

	if pm.AliveWorkers() != 1 {
		t.Errorf("alive = %d, want 1", pm.AliveWorkers())
	}
}

func TestQueuedTasks(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "a"})
	pm.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "b"})
	pm.DispatchTask("t-1", "w-1")

	queued := pm.QueuedTasks()
	if len(queued) != 1 {
		t.Errorf("queued = %d, want 1", len(queued))
	}
	if queued[0].ID != "t-2" {
		t.Errorf("queued[0] = %q, want t-2", queued[0].ID)
	}
}

// --- Full worker lifecycle test ---

func TestFullWorkerLifecycle(t *testing.T) {
	pm := newTestPoolManager(t)

	// Spawn.
	w, _ := pm.SpawnWorker(WorkerSpec{ID: "w-1", Role: "impl"})
	if w.Status != WorkerSpawning {
		t.Fatalf("step 1: status = %q", w.Status)
	}

	// Register.
	pm.RegisterWorker("w-1", "cid-123")
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Fatalf("step 2: status = %q", w.Status)
	}

	// Dispatch task.
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do it"})
	pm.DispatchTask("t-1", "w-1")
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerWorking {
		t.Fatalf("step 3: status = %q", w.Status)
	}

	// Complete task -> idle.
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done"}, nil)
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Fatalf("step 4: status = %q", w.Status)
	}

	// Dispatch another task.
	pm.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "more"})
	pm.DispatchTask("t-2", "w-1")
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerWorking {
		t.Fatalf("step 5: status = %q", w.Status)
	}

	// Complete and verify cycle.
	completeTestTask(pm, "w-1", "t-2", TaskResult{Summary: "done again"}, nil)
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Fatalf("step 6: status = %q", w.Status)
	}

	// Kill.
	pm.KillWorker("w-1")
	w, _ = pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Fatalf("step 7: status = %q", w.Status)
	}
}

// --- WAL recovery test ---

func TestRecoverPoolManager(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "test.wal")

	// Phase 1: create state.
	wal1, _ := OpenWAL(walPath)
	pm1 := NewPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal1, nil)
	pm1.SpawnWorker(WorkerSpec{ID: "w-1", Role: "impl"})
	pm1.RegisterWorker("w-1", "cid")
	pm1.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "build it", Priority: 5})
	pm1.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "test it", Priority: 10})
	pm1.DispatchTask("t-1", "w-1")
	completeTestTask(pm1, "w-1", "t-1", TaskResult{Summary: "built"}, nil)
	wal1.Close()

	// Phase 2: recover from WAL.
	wal2, _ := OpenWAL(walPath)
	defer wal2.Close()
	pm2, err := RecoverPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal2, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Verify recovered state.
	w, ok := pm2.Worker("w-1")
	if !ok {
		t.Fatal("worker not recovered")
	}
	if w.Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle", w.Status)
	}

	t1, _ := pm2.Task("t-1")
	if t1.Status != TaskCompleted {
		t.Errorf("t-1 status = %q, want completed", t1.Status)
	}

	t2, _ := pm2.Task("t-2")
	if t2.Status != TaskQueued {
		t.Errorf("t-2 status = %q, want queued", t2.Status)
	}

	// t-2 should still be in the queue.
	queued := pm2.QueuedTasks()
	if len(queued) != 1 || queued[0].ID != "t-2" {
		t.Errorf("queued = %v, want [t-2]", queued)
	}

	// Dispatch recovered queued task.
	task, err := pm2.DispatchNext("w-1")
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.ID != "t-2" {
		t.Errorf("dispatched = %v, want t-2", task)
	}
}

func TestRecoverPoolManager_PreservesWorkerTokensForValidation(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "test.wal")

	wal1, err := OpenWAL(walPath)
	if err != nil {
		t.Fatalf("open wal1: %v", err)
	}
	pm1 := NewPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal1, nil)
	w, err := pm1.SpawnWorker(WorkerSpec{ID: "w-1"})
	if err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	token := w.Token
	if token == "" {
		t.Fatal("expected worker token")
	}
	if err := pm1.RegisterWorker("w-1", "cid"); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("close wal1: %v", err)
	}

	wal2, err := OpenWAL(walPath)
	if err != nil {
		t.Fatalf("open wal2: %v", err)
	}
	defer wal2.Close()
	pm2, err := RecoverPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal2, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	workerID, ok := pm2.ValidateWorkerToken(token)
	if !ok {
		t.Fatal("expected recovered worker token to validate")
	}
	if workerID != "w-1" {
		t.Fatalf("workerID = %q, want %q", workerID, "w-1")
	}
}

// --- Notification channel test ---

func TestNotificationsOnTaskComplete(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	<-pm.Notify()
	<-pm.Notify()
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", TaskResult{}, nil)

	select {
	case n := <-pm.Notify():
		if n.Type != "task_completed" || n.ID != "t-1" {
			t.Errorf("notification = %+v, want task_completed/t-1", n)
		}
	default:
		t.Error("expected notification on task completion")
	}
}

func TestNotificationsOnTaskEnqueue(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	select {
	case n := <-pm.Notify():
		if n.Type != "task_created" || n.ID != "t-1" {
			t.Fatalf("notification = %+v, want task_created/t-1", n)
		}
	default:
		t.Fatal("expected notification on task enqueue")
	}
}

func TestNotificationsOnWorkerRegister(t *testing.T) {
	pm := newTestPoolManager(t)
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", "cid-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	select {
	case n := <-pm.Notify():
		if n.Type != "worker_ready" || n.ID != "w-1" {
			t.Fatalf("notification = %+v, want worker_ready/w-1", n)
		}
	default:
		t.Fatal("expected notification on worker registration")
	}
}

func TestNotificationSubscribersReceiveFanoutWithoutStealingLeaderChannel(t *testing.T) {
	pm := newTestPoolManager(t)
	sub, cancel := pm.SubscribeNotifications(1)
	defer cancel()

	want := Notification{Type: "task_completed", ID: "t-1"}
	pm.sendNotify(want)

	select {
	case got := <-pm.Notify():
		if got != want {
			t.Fatalf("leader notification = %+v, want %+v", got, want)
		}
	default:
		t.Fatal("expected leader notification")
	}

	select {
	case got := <-sub:
		if got != want {
			t.Fatalf("subscriber notification = %+v, want %+v", got, want)
		}
	default:
		t.Fatal("expected subscriber notification")
	}
}

// capturingHostAPI records the WorkerSpec passed to SpawnWorker.
type capturingHostAPI struct {
	lastSpec WorkerSpec
	killed   []string
}

func (c *capturingHostAPI) SpawnWorker(_ context.Context, spec WorkerSpec) (string, string, error) {
	c.lastSpec = spec
	return "test-container", "abc123", nil
}
func (c *capturingHostAPI) KillWorker(_ context.Context, workerID string) error {
	c.killed = append(c.killed, workerID)
	return nil
}
func (c *capturingHostAPI) ListContainers(_ context.Context, _ string) ([]ContainerInfo, error) {
	return nil, nil
}

func TestSpawnWorkerPropagatesID(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })

	hostAPI := &capturingHostAPI{}
	pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, hostAPI)

	// Spawn with empty ID — manager should auto-generate and propagate.
	w, err := pm.SpawnWorker(WorkerSpec{Role: "implementer"})
	if err != nil {
		t.Fatal(err)
	}
	if w.ID == "" {
		t.Fatal("expected auto-generated worker ID")
	}

	// The hostAPI should have received the generated ID, not empty string.
	if hostAPI.lastSpec.ID == "" {
		t.Error("hostAPI received empty spec.ID — propagation failed")
	}
	if hostAPI.lastSpec.ID != w.ID {
		t.Errorf("hostAPI spec.ID = %q, want %q (manager's assigned ID)", hostAPI.lastSpec.ID, w.ID)
	}
}

func TestSpawnWorkerPropagatesExplicitID(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })

	hostAPI := &capturingHostAPI{}
	pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, hostAPI)

	// Spawn with explicit ID — should be passed through unchanged.
	_, err = pm.SpawnWorker(WorkerSpec{ID: "w-custom", Role: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if hostAPI.lastSpec.ID != "w-custom" {
		t.Errorf("hostAPI spec.ID = %q, want %q", hostAPI.lastSpec.ID, "w-custom")
	}
}

func TestSpawnWorkerPersistsProviderOnWorker(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })

	hostAPI := &capturingHostAPI{}
	pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, hostAPI)

	worker, err := pm.SpawnWorker(WorkerSpec{ID: "w-1", Role: "implementer", Provider: "openai"})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if worker.Provider != "openai" {
		t.Fatalf("worker.Provider = %q, want openai", worker.Provider)
	}
}

func TestSpawnWorkerPropagatesSessionID(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })

	hostAPI := &capturingHostAPI{}
	pm := NewPoolManager(PoolConfig{
		SessionID:  "kitchen-project-123",
		MaxWorkers: 5,
		StateDir:   dir,
	}, wal, hostAPI)

	_, err = pm.SpawnWorker(WorkerSpec{ID: "w-1", Role: "implementer"})
	if err != nil {
		t.Fatal(err)
	}
	if got := hostAPI.lastSpec.Environment["MITTENS_SESSION_ID"]; got != "kitchen-project-123" {
		t.Fatalf("hostAPI spec environment session = %q, want %q", got, "kitchen-project-123")
	}
}

// --- isTerminalStatus tests ---

func TestIsTerminalStatus(t *testing.T) {
	terminal := []string{TaskCompleted, TaskFailed, TaskCanceled, TaskAccepted, TaskRejected, TaskEscalated}
	for _, s := range terminal {
		if !isTerminalStatus(s) {
			t.Errorf("isTerminalStatus(%q) = false, want true", s)
		}
	}
	active := []string{TaskQueued, TaskDispatched, TaskReviewing}
	for _, s := range active {
		if isTerminalStatus(s) {
			t.Errorf("isTerminalStatus(%q) = true, want false", s)
		}
	}
}

// --- WaitForTask tests ---

func TestWaitForTaskAlreadyCompleted(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done"}, nil)

	// Drain the notification channel so it doesn't interfere.
	<-pm.Notify()

	// Task already completed — should return immediately.
	ctx := context.Background()
	task, err := pm.WaitForTask(ctx, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.Result == nil || task.Result.Summary != "done" {
		t.Error("expected result with summary")
	}
}

func TestWaitForTaskNotFound(t *testing.T) {
	pm := newTestPoolManager(t)
	ctx := context.Background()
	_, err := pm.WaitForTask(ctx, "nope")
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

func TestWaitForTaskBlocksUntilComplete(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var waitTask *Task
	var waitErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		waitTask, waitErr = pm.WaitForTask(ctx, "t-1")
	}()

	// Give the goroutine time to register the waiter.
	time.Sleep(50 * time.Millisecond)

	// Complete the task — should unblock WaitForTask.
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "built"}, nil)

	wg.Wait()
	if waitErr != nil {
		t.Fatalf("WaitForTask error: %v", waitErr)
	}
	if waitTask.Status != TaskCompleted {
		t.Errorf("status = %q, want completed", waitTask.Status)
	}
	if waitTask.Result == nil || waitTask.Result.Summary != "built" {
		t.Error("expected result with summary")
	}
}

func TestWaitForTaskBlocksUntilFailed(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var waitTask *Task
	var waitErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		waitTask, waitErr = pm.WaitForTask(ctx, "t-1")
	}()

	time.Sleep(50 * time.Millisecond)
	pm.FailTask("w-1", "t-1", "crashed")

	wg.Wait()
	if waitErr != nil {
		t.Fatalf("WaitForTask error: %v", waitErr)
	}
	if waitTask.Status != TaskFailed {
		t.Errorf("status = %q, want failed", waitTask.Status)
	}
}

func TestWaitForTaskContextTimeout(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := pm.WaitForTask(ctx, "t-1")
	if err == nil {
		t.Error("expected context deadline error")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}

	// Verify waiter was cleaned up.
	pm.mu.RLock()
	waiters := pm.taskWaiters["t-1"]
	pm.mu.RUnlock()
	if len(waiters) != 0 {
		t.Errorf("waiter leak: %d waiters remaining after timeout", len(waiters))
	}
}

func TestWaitForTaskMultipleWaiters(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const numWaiters = 3
	var wg sync.WaitGroup
	results := make([]*Task, numWaiters)
	errors := make([]error, numWaiters)

	for i := 0; i < numWaiters; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errors[i] = pm.WaitForTask(ctx, "t-1")
		}()
	}

	time.Sleep(50 * time.Millisecond)
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done"}, nil)

	wg.Wait()
	for i := 0; i < numWaiters; i++ {
		if errors[i] != nil {
			t.Errorf("waiter %d error: %v", i, errors[i])
		}
		if results[i] == nil || results[i].Status != TaskCompleted {
			t.Errorf("waiter %d: status = %v, want completed", i, results[i])
		}
	}
}

func TestWaitForTask_PollSafetyNet(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var waitTask *Task
	var waitErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		waitTask, waitErr = pm.WaitForTask(ctx, "t-1")
	}()

	// Wait for the goroutine to register its waiter.
	time.Sleep(50 * time.Millisecond)

	// Manually transition task to completed WITHOUT sendNotify, so only the
	// 5-second poll safety net can detect it.
	pm.mu.Lock()
	task := pm.tasks["t-1"]
	task.Status = TaskCompleted
	task.Result = &TaskResult{Summary: "sneaky"}
	pm.mu.Unlock()

	wg.Wait()
	if waitErr != nil {
		t.Fatalf("WaitForTask error: %v", waitErr)
	}
	if waitTask.Status != TaskCompleted {
		t.Errorf("status = %q, want completed", waitTask.Status)
	}
}

func TestWaitForTask_ContextCancellation(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "x"})
	pm.DispatchTask("t-1", "w-1")

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	var waitErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, waitErr = pm.WaitForTask(ctx, "t-1")
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	wg.Wait()
	if waitErr != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", waitErr)
	}

	// Verify waiter was cleaned up.
	pm.mu.RLock()
	waiters := pm.taskWaiters["t-1"]
	pm.mu.RUnlock()
	if len(waiters) != 0 {
		t.Errorf("waiter leak: %d waiters remaining after cancel", len(waiters))
	}
}

func TestReviewerDeath_TaskReturnsToCompleted(t *testing.T) {
	pm := newTestPoolManager(t)

	// Spawn implementer + reviewer.
	pm.SpawnWorker(WorkerSpec{ID: "w-impl", Role: "implementer"})
	pm.RegisterWorker("w-impl", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-rev", Role: "reviewer"})
	pm.RegisterWorker("w-rev", "")

	// Create, dispatch, and complete task.
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "build feature", Priority: 1, MaxReviews: 3})
	pm.DispatchTask("t-1", "w-impl")
	completeTestTask(pm, "w-impl", "t-1", TaskResult{Summary: "done"}, nil)

	// Drain completion notification.
	<-pm.Notify()

	// Dispatch review.
	if err := pm.DispatchReview("t-1", "w-rev"); err != nil {
		t.Fatalf("DispatchReview: %v", err)
	}
	task, _ := pm.Task("t-1")
	if task.Status != TaskReviewing {
		t.Fatalf("status = %q, want reviewing", task.Status)
	}

	// Reviewer dies mid-review.
	pm.MarkDead("w-rev")

	// Task should revert to completed (not queued).
	task, _ = pm.Task("t-1")
	if task.Status != TaskCompleted {
		t.Errorf("status = %q, want completed (review aborted)", task.Status)
	}
	if task.ReviewerID != "" {
		t.Errorf("reviewerID = %q, want empty", task.ReviewerID)
	}

	// Spawn a new reviewer and verify re-review works.
	pm.SpawnWorker(WorkerSpec{ID: "w-rev2", Role: "reviewer"})
	pm.RegisterWorker("w-rev2", "")

	if err := pm.DispatchReview("t-1", "w-rev2"); err != nil {
		t.Fatalf("second DispatchReview: %v", err)
	}
	task, _ = pm.Task("t-1")
	if task.Status != TaskReviewing {
		t.Errorf("status = %q, want reviewing after re-dispatch", task.Status)
	}
	if task.ReviewerID != "w-rev2" {
		t.Errorf("reviewerID = %q, want w-rev2", task.ReviewerID)
	}
}

func TestWaitForTaskReviewPass(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1", Role: "implementer"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-2", Role: "reviewer"})
	pm.RegisterWorker("w-2", "")

	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "implement it"})
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done"}, nil)

	// Drain completion notification.
	<-pm.Notify()

	// Now dispatch review — task goes to "reviewing" state.
	pm.DispatchReview("t-1", "w-2")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var waitTask *Task
	var waitErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		waitTask, waitErr = pm.WaitForTask(ctx, "t-1")
	}()

	time.Sleep(50 * time.Millisecond)
	pm.ReportReview("t-1", ReviewPass, "looks good", "")

	wg.Wait()
	if waitErr != nil {
		t.Fatalf("WaitForTask error: %v", waitErr)
	}
	if waitTask.Status != TaskAccepted {
		t.Errorf("status = %q, want accepted", waitTask.Status)
	}
}
