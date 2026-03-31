package pool

import (
	"path/filepath"
	"testing"
)

func newReviewTestPM(t *testing.T) *PoolManager {
	t.Helper()
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "test.wal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })
	return NewPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal, nil)
}

// setupCompletedTask spawns an implementer + reviewer, enqueues a task,
// dispatches to implementer, completes it. Returns (taskID, implID, revID).
func setupCompletedTask(t *testing.T, pm *PoolManager, maxReviews int) (string, string, string) {
	t.Helper()

	if _, err := pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("impl-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	if err := pm.RegisterWorker("rev-1", ""); err != nil {
		t.Fatal(err)
	}

	tid, err := pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "implement feature", Priority: 1, MaxReviews: maxReviews})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.DispatchTask(tid, "impl-1"); err != nil {
		t.Fatal(err)
	}
	if err := completeTestTask(pm, "impl-1", tid, TaskResult{Summary: "done"}, nil); err != nil {
		t.Fatal(err)
	}
	return tid, "impl-1", "rev-1"
}

// setupEscalatedTask creates a task that has been escalated (MaxReviews=1, one fail).
func setupEscalatedTask(t *testing.T, pm *PoolManager) string {
	t.Helper()
	tid, _, revID := setupCompletedTask(t, pm, 1)

	if err := pm.DispatchReview(tid, revID); err != nil {
		t.Fatal(err)
	}
	// ReviewCycles becomes 1 == MaxReviews → escalated.
	if err := pm.ReportReview(tid, ReviewFail, "bad", SeverityCritical); err != nil {
		t.Fatal(err)
	}
	task, _ := pm.Task(tid)
	if task.Status != TaskEscalated {
		t.Fatalf("setup: expected escalated, got %q", task.Status)
	}
	return tid
}

// --- DispatchReview ---

func TestDispatchReview_AssignsReviewerAndTransitions(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	if err := pm.DispatchReview(tid, revID); err != nil {
		t.Fatal(err)
	}

	task, ok := pm.Task(tid)
	if !ok {
		t.Fatal("task not found")
	}
	if task.Status != TaskReviewing {
		t.Errorf("task status = %q, want %q", task.Status, TaskReviewing)
	}
	if task.ReviewerID != revID {
		t.Errorf("reviewerID = %q, want %q", task.ReviewerID, revID)
	}

	w, _ := pm.Worker(revID)
	if w.Status != WorkerWorking {
		t.Errorf("reviewer status = %q, want %q", w.Status, WorkerWorking)
	}
	if w.CurrentTaskID != tid {
		t.Errorf("reviewer currentTaskID = %q, want %q", w.CurrentTaskID, tid)
	}
}

func TestDispatchReview_PollTaskReturnsReviewTask(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	if err := pm.DispatchReview(tid, revID); err != nil {
		t.Fatal(err)
	}

	// The reviewer should be able to poll and receive the review task.
	task := pm.PollTask(revID)
	if task == nil {
		t.Fatal("PollTask returned nil for reviewer with dispatched review")
	}
	if task.ID != tid {
		t.Errorf("PollTask returned task %q, want %q", task.ID, tid)
	}
	if task.Status != TaskReviewing {
		t.Errorf("polled task status = %q, want %q", task.Status, TaskReviewing)
	}
}

func TestDispatchReview_AutoPick(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, _ := setupCompletedTask(t, pm, 3)

	revID, err := pm.PickReviewer(tid)
	if err != nil {
		t.Fatal(err)
	}
	if revID == "" {
		t.Fatal("PickReviewer returned empty ID")
	}
	if err := pm.DispatchReview(tid, revID); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskReviewing {
		t.Errorf("task status = %q, want %q", task.Status, TaskReviewing)
	}
}

func TestDispatchReview_SelfReviewPrevention(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, implID, _ := setupCompletedTask(t, pm, 3)

	err := pm.DispatchReview(tid, implID)
	if err == nil {
		t.Fatal("expected error for self-review")
	}
}

func TestDispatchReview_TaskNotFound(t *testing.T) {
	pm := newReviewTestPM(t)
	err := pm.DispatchReview("nonexistent", "w-1")
	if err == nil {
		t.Error("expected error for missing task")
	}
}

func TestDispatchReview_TaskWrongStatus(t *testing.T) {
	pm := newReviewTestPM(t)
	pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"})
	pm.RegisterWorker("rev-1", "")
	pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "x"})

	err := pm.DispatchReview("task-1", "rev-1")
	if err == nil {
		t.Error("expected error for non-completed task")
	}
}

func TestDispatchReview_ReviewerNotIdle(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	tid2, _ := pm.EnqueueTask(TaskSpec{ID: "task-2", Prompt: "other"})
	pm.DispatchTask(tid2, revID)

	err := pm.DispatchReview(tid, revID)
	if err == nil {
		t.Error("expected error for non-idle reviewer")
	}
}

func TestDispatchReview_ReviewerNotFound(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, _ := setupCompletedTask(t, pm, 3)

	err := pm.DispatchReview(tid, "ghost")
	if err == nil {
		t.Error("expected error for missing reviewer")
	}
}

// --- ReportReview ---

func TestReportReview_Pass(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	pm.DispatchReview(tid, revID)
	if err := pm.ReportReview(tid, ReviewPass, "looks good", SeverityMinor); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskAccepted {
		t.Errorf("task status = %q, want %q", task.Status, TaskAccepted)
	}

	w, _ := pm.Worker(revID)
	if w.Status != WorkerIdle {
		t.Errorf("reviewer status = %q, want %q", w.Status, WorkerIdle)
	}
}

func TestReportReview_FailUnderMaxRetries(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	pm.DispatchReview(tid, revID)
	// MaxReviews=3, after first fail ReviewCycles=1 → under max → rejected.
	if err := pm.ReportReview(tid, ReviewFail, "needs changes", SeverityMajor); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskRejected {
		t.Errorf("task status = %q, want %q", task.Status, TaskRejected)
	}
	if task.ReviewCycles != 1 {
		t.Errorf("reviewCycles = %d, want 1", task.ReviewCycles)
	}

	w, _ := pm.Worker(revID)
	if w.Status != WorkerIdle {
		t.Errorf("reviewer status = %q, want %q", w.Status, WorkerIdle)
	}
}

func TestReportReview_FailAtMaxRetries(t *testing.T) {
	pm := newReviewTestPM(t)
	// MaxReviews=1: first fail → cycles=1 >= max=1 → escalated.
	tid, _, revID := setupCompletedTask(t, pm, 1)

	pm.DispatchReview(tid, revID)
	if err := pm.ReportReview(tid, ReviewFail, "critical issue", SeverityCritical); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskEscalated {
		t.Errorf("task status = %q, want %q", task.Status, TaskEscalated)
	}

	w, _ := pm.Worker(revID)
	if w.Status != WorkerIdle {
		t.Errorf("reviewer status = %q, want %q", w.Status, WorkerIdle)
	}
}

func TestReportReview_UnknownVerdict(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)
	pm.DispatchReview(tid, revID)

	err := pm.ReportReview(tid, "maybe", "", "")
	if err == nil {
		t.Error("expected error for unknown verdict")
	}
}

func TestReportReview_TaskNotReviewing(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, _ := setupCompletedTask(t, pm, 3)

	err := pm.ReportReview(tid, ReviewPass, "", "")
	if err == nil {
		t.Error("expected error for non-reviewing task")
	}
}

// --- ResolveEscalation ---

func TestResolveEscalation_Accept(t *testing.T) {
	pm := newReviewTestPM(t)
	tid := setupEscalatedTask(t, pm)

	if err := pm.ResolveEscalation(tid, EscalationAccept, 0); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskAccepted {
		t.Errorf("task status = %q, want %q", task.Status, TaskAccepted)
	}
}

func TestResolveEscalation_Retry(t *testing.T) {
	pm := newReviewTestPM(t)
	tid := setupEscalatedTask(t, pm)

	if err := pm.ResolveEscalation(tid, EscalationRetry, 2); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskRejected {
		t.Errorf("task status = %q, want %q", task.Status, TaskRejected)
	}
	// MaxReviews: original 1 + extra 2 = 3.
	if task.MaxReviews != 3 {
		t.Errorf("maxReviews = %d, want 3", task.MaxReviews)
	}
}

func TestResolveEscalation_Abort(t *testing.T) {
	pm := newReviewTestPM(t)
	tid := setupEscalatedTask(t, pm)

	if err := pm.ResolveEscalation(tid, EscalationAbort, 0); err != nil {
		t.Fatal(err)
	}

	task, _ := pm.Task(tid)
	if task.Status != TaskCanceled {
		t.Errorf("task status = %q, want %q", task.Status, TaskCanceled)
	}
}

func TestResolveEscalation_TaskNotEscalated(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, _ := setupCompletedTask(t, pm, 3)

	err := pm.ResolveEscalation(tid, EscalationAccept, 0)
	if err == nil {
		t.Error("expected error for non-escalated task")
	}
}

func TestResolveEscalation_UnknownAction(t *testing.T) {
	pm := newReviewTestPM(t)
	tid := setupEscalatedTask(t, pm)

	err := pm.ResolveEscalation(tid, "explode", 0)
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestResolveEscalation_TaskNotFound(t *testing.T) {
	pm := newReviewTestPM(t)

	err := pm.ResolveEscalation("ghost", EscalationAccept, 0)
	if err == nil {
		t.Error("expected error for missing task")
	}
}

// --- ReviewRecord preservation ---

func TestReviewRecord_FeedbackAndSeverityPreserved(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	pm.DispatchReview(tid, revID)
	pm.ReportReview(tid, ReviewPass, "excellent work, ship it", SeverityMinor)

	task, _ := pm.Task(tid)
	if len(task.Reviews) != 1 {
		t.Fatalf("reviews count = %d, want 1", len(task.Reviews))
	}

	r := task.Reviews[0]
	if r.ReviewerID != revID {
		t.Errorf("reviewerID = %q, want %q", r.ReviewerID, revID)
	}
	if r.Verdict != ReviewPass {
		t.Errorf("verdict = %q, want %q", r.Verdict, ReviewPass)
	}
	if r.Feedback != "excellent work, ship it" {
		t.Errorf("feedback = %q, want %q", r.Feedback, "excellent work, ship it")
	}
	if r.Severity != SeverityMinor {
		t.Errorf("severity = %q, want %q", r.Severity, SeverityMinor)
	}
	if r.ReviewedAt.IsZero() {
		t.Error("reviewedAt should not be zero")
	}
}

func TestReviewRecord_MultipleReviewsAccumulate(t *testing.T) {
	pm := newReviewTestPM(t)

	pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"})
	pm.RegisterWorker("impl-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"})
	pm.RegisterWorker("rev-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "rev-2", Role: "reviewer"})
	pm.RegisterWorker("rev-2", "")

	tid, _ := pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "x", MaxReviews: 3})
	pm.DispatchTask(tid, "impl-1")
	completeTestTask(pm, "impl-1", tid, TaskResult{Summary: "done"}, nil)

	// First review: fail.
	pm.DispatchReview(tid, "rev-1")
	pm.ReportReview(tid, ReviewFail, "first feedback", SeverityMajor)

	// Second review on the rejected task: pass.
	pm.DispatchReview(tid, "rev-2")
	pm.ReportReview(tid, ReviewPass, "second feedback", SeverityMinor)

	task, _ := pm.Task(tid)
	if len(task.Reviews) != 2 {
		t.Fatalf("reviews count = %d, want 2", len(task.Reviews))
	}
	if task.Reviews[0].Feedback != "first feedback" {
		t.Errorf("reviews[0].feedback = %q, want %q", task.Reviews[0].Feedback, "first feedback")
	}
	if task.Reviews[1].Feedback != "second feedback" {
		t.Errorf("reviews[1].feedback = %q, want %q", task.Reviews[1].Feedback, "second feedback")
	}
	if task.ReviewCycles != 2 {
		t.Errorf("reviewCycles = %d, want 2", task.ReviewCycles)
	}
}

// --- AbortReview ---

func TestAbortReview_Success(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	if err := pm.DispatchReview(tid, revID); err != nil {
		t.Fatal(err)
	}

	if err := pm.AbortReview(revID, tid); err != nil {
		t.Fatal(err)
	}

	task, ok := pm.Task(tid)
	if !ok {
		t.Fatal("task not found")
	}
	if task.Status != TaskCompleted {
		t.Errorf("task status = %q, want %q", task.Status, TaskCompleted)
	}
	if task.ReviewerID != "" {
		t.Errorf("reviewerID = %q, want empty", task.ReviewerID)
	}

	w, _ := pm.Worker(revID)
	if w.Status != WorkerIdle {
		t.Errorf("reviewer status = %q, want %q", w.Status, WorkerIdle)
	}
	if w.CurrentTaskID != "" {
		t.Errorf("reviewer currentTaskID = %q, want empty", w.CurrentTaskID)
	}
}

func TestAbortReview_WrongStatus(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, _ := setupCompletedTask(t, pm, 3)

	err := pm.AbortReview("rev-1", tid)
	if err == nil {
		t.Error("expected error when task is not in reviewing status")
	}
}

func TestAbortReview_WrongWorker(t *testing.T) {
	pm := newReviewTestPM(t)
	tid, _, revID := setupCompletedTask(t, pm, 3)

	if err := pm.DispatchReview(tid, revID); err != nil {
		t.Fatal(err)
	}

	err := pm.AbortReview("wrong-worker", tid)
	if err == nil {
		t.Error("expected error when workerID does not match ReviewerID")
	}
}

// --- PickReviewer ---

func TestPickReviewer_PrefersReviewerRole(t *testing.T) {
	pm := newReviewTestPM(t)

	pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"})
	pm.RegisterWorker("impl-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "gen-1", Role: "general"})
	pm.RegisterWorker("gen-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"})
	pm.RegisterWorker("rev-1", "")

	tid, _ := pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "x"})
	pm.DispatchTask(tid, "impl-1")
	completeTestTask(pm, "impl-1", tid, TaskResult{Summary: "done"}, nil)

	picked, err := pm.PickReviewer(tid)
	if err != nil {
		t.Fatal(err)
	}
	if picked != "rev-1" {
		t.Errorf("picked = %q, want rev-1 (reviewer role preferred)", picked)
	}
}

func TestPickReviewer_ExcludesSelfReview(t *testing.T) {
	pm := newReviewTestPM(t)

	pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"})
	pm.RegisterWorker("impl-1", "")

	tid, _ := pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "x"})
	pm.DispatchTask(tid, "impl-1")
	completeTestTask(pm, "impl-1", tid, TaskResult{Summary: "done"}, nil)

	_, err := pm.PickReviewer(tid)
	if err == nil {
		t.Error("expected error when only available worker is the implementer")
	}
}

func TestPickReviewer_PrefersFreshEyes(t *testing.T) {
	pm := newReviewTestPM(t)

	pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"})
	pm.RegisterWorker("impl-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"})
	pm.RegisterWorker("rev-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "rev-2", Role: "reviewer"})
	pm.RegisterWorker("rev-2", "")

	tid, _ := pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "x", MaxReviews: 3})
	pm.DispatchTask(tid, "impl-1")
	completeTestTask(pm, "impl-1", tid, TaskResult{Summary: "done"}, nil)

	// First review by rev-1 → fail (rejected).
	pm.DispatchReview(tid, "rev-1")
	pm.ReportReview(tid, ReviewFail, "nope", SeverityMinor)

	// PickReviewer should prefer rev-2 (fresh eyes).
	picked, err := pm.PickReviewer(tid)
	if err != nil {
		t.Fatal(err)
	}
	if picked != "rev-2" {
		t.Errorf("picked = %q, want rev-2 (fresh eyes preferred)", picked)
	}
}

func TestPickReviewer_TaskNotFound(t *testing.T) {
	pm := newReviewTestPM(t)

	_, err := pm.PickReviewer("ghost")
	if err == nil {
		t.Error("expected error for missing task")
	}
}

func TestPickReviewer_NoIdleWorkers(t *testing.T) {
	pm := newReviewTestPM(t)

	pm.SpawnWorker(WorkerSpec{ID: "impl-1", Role: "implementer"})
	pm.RegisterWorker("impl-1", "")
	// Reviewer exists but is spawning (not registered).
	pm.SpawnWorker(WorkerSpec{ID: "rev-1", Role: "reviewer"})

	tid, _ := pm.EnqueueTask(TaskSpec{ID: "task-1", Prompt: "x"})
	pm.DispatchTask(tid, "impl-1")
	completeTestTask(pm, "impl-1", tid, TaskResult{Summary: "done"}, nil)

	_, err := pm.PickReviewer(tid)
	if err == nil {
		t.Error("expected error when no idle workers available")
	}
}
