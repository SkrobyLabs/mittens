package pool

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRequeueOrphanedTasks_DispatchedOnDeadWorker(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do it", Priority: 5})
	pm.DispatchTask("t-1", "w-1")

	// Kill the worker — MarkDead now requeues inline.
	pm.MarkDead("w-1")

	// RequeueOrphanedTasks finds nothing because MarkDead already handled it.
	requeued := RequeueOrphanedTasks(pm)
	if requeued != 0 {
		t.Errorf("requeued = %d, want 0 (already requeued by MarkDead)", requeued)
	}

	task, _ := pm.Task("t-1")
	if task.Status != TaskQueued {
		t.Errorf("task status = %q, want queued", task.Status)
	}
	if task.WorkerID != "" {
		t.Errorf("task worker = %q, want empty", task.WorkerID)
	}
}

func TestRequeueOrphanedTasks_NoOrphans(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do it"})
	pm.DispatchTask("t-1", "w-1")

	// Worker is alive — no orphans.
	requeued := RequeueOrphanedTasks(pm)
	if requeued != 0 {
		t.Errorf("requeued = %d, want 0", requeued)
	}
}

func TestRequeueOrphanedTasks_ReviewingOnDeadReviewer(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")

	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do it"})
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done"}, nil)

	// Simulate review dispatch via direct WAL event.
	pm.mu.Lock()
	e := Event{Type: EventReviewDispatched, TaskID: "t-1", Data: marshalData(ReviewDispatchedData{ReviewerID: "w-2"})}
	pm.wal.Append(e)
	Apply(pm, e)
	pm.mu.Unlock()

	// Kill the reviewer — MarkDead aborts review inline.
	pm.MarkDead("w-2")

	// RequeueOrphanedTasks finds nothing because MarkDead already handled it.
	requeued := RequeueOrphanedTasks(pm)
	if requeued != 0 {
		t.Errorf("requeued = %d, want 0 (already handled by MarkDead)", requeued)
	}

	task, _ := pm.Task("t-1")
	if task.Status != TaskCompleted {
		t.Errorf("task status = %q, want completed (review aborted, not re-queued for implementation)", task.Status)
	}
}

func TestRequeueOrphanedTasks_MissingWorker(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do it"})
	pm.DispatchTask("t-1", "w-1")

	// Remove worker from map to simulate WAL inconsistency.
	pm.mu.Lock()
	delete(pm.workers, "w-1")
	pm.mu.Unlock()

	requeued := RequeueOrphanedTasks(pm)
	if requeued != 1 {
		t.Errorf("requeued = %d, want 1", requeued)
	}
}

// mockHostAPI implements HostAPI for testing.
type mockHostAPI struct {
	killed []string
}

func (m *mockHostAPI) SpawnWorker(_ context.Context, _ WorkerSpec) (string, string, error) {
	return "", "", nil
}
func (m *mockHostAPI) KillWorker(_ context.Context, workerID string) error {
	m.killed = append(m.killed, workerID)
	return nil
}
func (m *mockHostAPI) ListContainers(_ context.Context, _ string) ([]ContainerInfo, error) {
	return nil, nil
}
func (m *mockHostAPI) CheckSession(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func TestReconcile_MarkMissingWorkersDead(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")

	// Only w-1 is running.
	running := []ContainerInfo{
		{ContainerID: "abc", WorkerID: "w-1", Status: "Up"},
	}

	reconciled, killed := Reconcile(pm, running)
	if reconciled != 1 {
		t.Errorf("reconciled = %d, want 1", reconciled)
	}
	if killed != 0 {
		t.Errorf("killed = %d, want 0", killed)
	}

	w2, _ := pm.Worker("w-2")
	if w2.Status != WorkerDead {
		t.Errorf("w-2 status = %q, want dead", w2.Status)
	}
}

func newTestPoolManagerWithHostAPI(t *testing.T, hostAPI HostAPI) *PoolManager {
	t.Helper()
	dir := t.TempDir()
	wal, err := OpenWAL(dir + "/test.wal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })
	pm := NewPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal, hostAPI)
	return pm
}

func TestReconcile_KillOrphans(t *testing.T) {
	mock := &mockHostAPI{}
	pm := newTestPoolManagerWithHostAPI(t, mock)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	// Container w-orphan is not in pool state.
	running := []ContainerInfo{
		{ContainerID: "abc", WorkerID: "w-1", Status: "Up"},
		{ContainerID: "def", WorkerID: "w-orphan", Status: "Up"},
	}

	reconciled, killed := Reconcile(pm, running)
	if reconciled != 0 {
		t.Errorf("reconciled = %d, want 0", reconciled)
	}
	if killed != 1 {
		t.Errorf("killed = %d, want 1", killed)
	}
	if len(mock.killed) != 1 || mock.killed[0] != "w-orphan" {
		t.Errorf("killed = %v, want [w-orphan]", mock.killed)
	}
}

func TestReconcile_ZeroContainers(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")

	// All workers crashed — no containers running.
	reconciled, _ := Reconcile(pm, nil)
	if reconciled != 2 {
		t.Errorf("reconciled = %d, want 2", reconciled)
	}

	w1, _ := pm.Worker("w-1")
	w2, _ := pm.Worker("w-2")
	if w1.Status != WorkerDead || w2.Status != WorkerDead {
		t.Errorf("expected both workers dead, got w-1=%q w-2=%q", w1.Status, w2.Status)
	}
}

func TestReconcile_AlreadyDeadNotRecounted(t *testing.T) {
	pm := newTestPoolManager(t)
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.MarkDead("w-1")

	// No running containers — but w-1 is already dead.
	reconciled, _ := Reconcile(pm, nil)
	if reconciled != 0 {
		t.Errorf("reconciled = %d, want 0 (already dead)", reconciled)
	}
}

// newRecoveryPM creates a PoolManager whose WAL path is returned for reopen.
func newRecoveryPM(t *testing.T, hostAPI HostAPI) (*PoolManager, string) {
	t.Helper()
	dir := t.TempDir()
	walPath := filepath.Join(dir, "events.wal")
	wal, err := OpenWAL(walPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wal.Close() })
	return NewPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal, hostAPI), walPath
}

// spawnReady spawns a worker and registers it so it transitions to idle.
func spawnReady(t *testing.T, pm *PoolManager, id, role string) {
	t.Helper()
	if _, err := pm.SpawnWorker(WorkerSpec{ID: id, Role: role}); err != nil {
		t.Fatalf("spawn %s: %v", id, err)
	}
	if err := pm.RegisterWorker(id, "cid-"+id); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func TestWALReplayRebuildsState(t *testing.T) {
	pm, walPath := newRecoveryPM(t, nil)

	// Build up state: 2 workers, 3 tasks, dispatch and complete one.
	spawnReady(t, pm, "w-1", "implementer")
	spawnReady(t, pm, "w-2", "reviewer")

	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "task one", Priority: 1})
	pm.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "task two", Priority: 2})
	pm.EnqueueTask(TaskSpec{ID: "t-3", Prompt: "task three", Priority: 3})

	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "done"}, nil)
	pm.DispatchTask("t-2", "w-1")

	// Snapshot original state.
	origWorkers := pm.Workers()
	origTasks := pm.Tasks()

	// Recover from WAL into a new PoolManager.
	wal2, err := OpenWAL(walPath)
	if err != nil {
		t.Fatal(err)
	}
	defer wal2.Close()

	pm2, err := RecoverPoolManager(PoolConfig{MaxWorkers: 10}, wal2, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Verify worker count and statuses match.
	recWorkers := pm2.Workers()
	if len(recWorkers) != len(origWorkers) {
		t.Fatalf("recovered %d workers, want %d", len(recWorkers), len(origWorkers))
	}
	for _, ow := range origWorkers {
		rw, ok := pm2.Worker(ow.ID)
		if !ok {
			t.Errorf("worker %s not found after recovery", ow.ID)
			continue
		}
		if rw.Status != ow.Status {
			t.Errorf("worker %s status = %q, want %q", ow.ID, rw.Status, ow.Status)
		}
		if rw.Role != ow.Role {
			t.Errorf("worker %s role = %q, want %q", ow.ID, rw.Role, ow.Role)
		}
	}

	// Verify task count and statuses match.
	recTasks := pm2.Tasks()
	if len(recTasks) != len(origTasks) {
		t.Fatalf("recovered %d tasks, want %d", len(recTasks), len(origTasks))
	}

	t1, _ := pm2.Task("t-1")
	if t1.Status != TaskCompleted {
		t.Errorf("t-1 status = %q, want completed", t1.Status)
	}

	t2, _ := pm2.Task("t-2")
	if t2.Status != TaskDispatched {
		t.Errorf("t-2 status = %q, want dispatched", t2.Status)
	}
	if t2.WorkerID != "w-1" {
		t.Errorf("t-2 workerID = %q, want w-1", t2.WorkerID)
	}

	// t-3 should be queued and re-enqueued to the priority queue by RecoverPoolManager.
	t3, _ := pm2.Task("t-3")
	if t3.Status != TaskQueued {
		t.Errorf("t-3 status = %q, want queued", t3.Status)
	}
	queued := pm2.QueuedTasks()
	found := false
	for _, qt := range queued {
		if qt.ID == "t-3" {
			found = true
			break
		}
	}
	if !found {
		t.Error("t-3 not found in recovered priority queue")
	}
}

func TestRecoveryAfterWorkerDeathMidTask(t *testing.T) {
	pm, walPath := newRecoveryPM(t, nil)

	spawnReady(t, pm, "w-1", "implementer")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "big refactor", Priority: 1})
	pm.DispatchTask("t-1", "w-1")

	// Worker dies mid-task (container crash). MarkDead requeues t-1 inline.
	pm.MarkDead("w-1")

	// Recover from WAL — both the dead event and requeue event are replayed.
	wal2, err := OpenWAL(walPath)
	if err != nil {
		t.Fatal(err)
	}
	defer wal2.Close()

	pm2, err := RecoverPoolManager(PoolConfig{MaxWorkers: 10}, wal2, nil)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	// After replay, w-1 is dead and t-1 is already queued (requeued inline by MarkDead).
	w, _ := pm2.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("w-1 status = %q, want dead", w.Status)
	}
	task, _ := pm2.Task("t-1")
	if task.Status != TaskQueued {
		t.Errorf("t-1 status = %q, want queued (requeued inline by MarkDead)", task.Status)
	}

	// RequeueOrphanedTasks finds nothing — already handled.
	requeued := RequeueOrphanedTasks(pm2)
	if requeued != 0 {
		t.Fatalf("requeued = %d, want 0 (already requeued by MarkDead)", requeued)
	}
}

func TestFullRecoverySequence(t *testing.T) {
	// Phase 1: Build state with original PoolManager.
	pm, walPath := newRecoveryPM(t, nil)

	spawnReady(t, pm, "w-1", "implementer")
	spawnReady(t, pm, "w-2", "implementer")
	spawnReady(t, pm, "w-3", "reviewer")

	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "task one", Priority: 1})
	pm.EnqueueTask(TaskSpec{ID: "t-2", Prompt: "task two", Priority: 2})
	pm.EnqueueTask(TaskSpec{ID: "t-3", Prompt: "task three", Priority: 3})

	// t-1: dispatched and completed.
	pm.DispatchTask("t-1", "w-1")
	completeTestTask(pm, "w-1", "t-1", TaskResult{Summary: "t1 done"}, nil)

	// t-2: dispatched to w-2, still in-flight.
	pm.DispatchTask("t-2", "w-2")

	// t-3: still queued.

	// Phase 2: WAL.Replay via RecoverPoolManager.
	wal2, err := OpenWAL(walPath)
	if err != nil {
		t.Fatal(err)
	}
	defer wal2.Close()

	mock := &mockHostAPI{}
	pm2, err := RecoverPoolManager(PoolConfig{MaxWorkers: 10}, wal2, mock)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Phase 3: Reconcile — only w-1 is still running; w-2 and w-3 crashed.
	running := []ContainerInfo{
		{ContainerID: "cid-w-1", WorkerID: "w-1", Status: "running"},
		{ContainerID: "cid-ghost", WorkerID: "w-ghost", Status: "running"}, // orphan
	}
	reconciled, killed := Reconcile(pm2, running)

	if reconciled != 2 {
		t.Errorf("reconciled = %d, want 2", reconciled)
	}
	if killed != 1 {
		t.Errorf("killed = %d, want 1", killed)
	}

	// Phase 4: RequeueOrphanedTasks — nothing to do because MarkDead
	// (called by Reconcile) already requeued t-2 inline.
	requeued := RequeueOrphanedTasks(pm2)
	if requeued != 0 {
		t.Errorf("requeued = %d, want 0 (already requeued by MarkDead)", requeued)
	}

	// Verify final state.
	w1, _ := pm2.Worker("w-1")
	if w1.Status != WorkerIdle {
		t.Errorf("w-1 status = %q, want idle", w1.Status)
	}
	w2f, _ := pm2.Worker("w-2")
	if w2f.Status != WorkerDead {
		t.Errorf("w-2 status = %q, want dead", w2f.Status)
	}
	w3f, _ := pm2.Worker("w-3")
	if w3f.Status != WorkerDead {
		t.Errorf("w-3 status = %q, want dead", w3f.Status)
	}

	t1, _ := pm2.Task("t-1")
	if t1.Status != TaskCompleted {
		t.Errorf("t-1 status = %q, want completed", t1.Status)
	}
	t2, _ := pm2.Task("t-2")
	if t2.Status != TaskQueued {
		t.Errorf("t-2 status = %q, want queued (requeued)", t2.Status)
	}
	t3, _ := pm2.Task("t-3")
	if t3.Status != TaskQueued {
		t.Errorf("t-3 status = %q, want queued", t3.Status)
	}

	// Both t-2 and t-3 are in the queue. Dispatch to w-1 (only alive idle worker).
	dispatched, err := pm2.DispatchNext("w-1")
	if err != nil {
		t.Fatalf("dispatch next: %v", err)
	}
	if dispatched == nil {
		t.Fatal("expected a task to be dispatched from recovered queue")
	}
	// t-2 (priority 2) beats t-3 (priority 3) — lower number = higher priority.
	if dispatched.ID != "t-2" {
		t.Errorf("dispatched task = %q, want t-2 (higher priority)", dispatched.ID)
	}
}
