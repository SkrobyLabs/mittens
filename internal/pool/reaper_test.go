package pool

import (
	"testing"
	"time"
)

// setWorkerHeartbeat directly sets a worker's LastHeartbeat for testing.
func setWorkerHeartbeat(pm *PoolManager, workerID string, ts time.Time) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if w := pm.workers[workerID]; w != nil {
		w.LastHeartbeat = ts
	}
}

// setWorkerSpawnedAt directly sets a worker's SpawnedAt for testing.
func setWorkerSpawnedAt(pm *PoolManager, workerID string, ts time.Time) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if w := pm.workers[workerID]; w != nil {
		w.SpawnedAt = ts
	}
}

func TestReapStaleWorkers_ExpiredHeartbeatMarkedDead(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	setWorkerHeartbeat(pm, "w-1", time.Now().Add(-2*time.Minute))

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("worker status = %q, want dead", w.Status)
	}
}

func TestReapStaleWorkers_SpawnGracePeriod(t *testing.T) {
	pm := newTestPoolManager(t)

	// Worker just spawned, still spawning, no heartbeat — grace period protects.
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerSpawning {
		t.Errorf("worker status = %q, want spawning (grace period)", w.Status)
	}
}

func TestReapStaleWorkers_SpawnGracePeriodExpired(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	setWorkerSpawnedAt(pm, "w-1", time.Now().Add(-5*time.Minute))

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("worker status = %q, want dead (old spawning)", w.Status)
	}
}

func TestReapStaleWorkers_ZeroHeartbeatRecentSpawn(t *testing.T) {
	pm := newTestPoolManager(t)

	// Registered (idle) but never sent a heartbeat; recent spawn protects.
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle (zero heartbeat, recent spawn)", w.Status)
	}
}

func TestReapStaleWorkers_ZeroHeartbeatOldSpawn(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	setWorkerSpawnedAt(pm, "w-1", time.Now().Add(-5*time.Minute))

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("worker status = %q, want dead (zero heartbeat, old spawn)", w.Status)
	}
}

func TestReapStaleWorkers_ActiveTaskRequeued(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "do work", Priority: 5})
	pm.DispatchTask("t-1", "w-1")

	task, _ := pm.Task("t-1")
	if task.Status != TaskDispatched {
		t.Fatalf("task status = %q, want dispatched (precondition)", task.Status)
	}

	setWorkerHeartbeat(pm, "w-1", time.Now().Add(-2*time.Minute))

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("worker status = %q, want dead", w.Status)
	}

	task, _ = pm.Task("t-1")
	if task.Status != TaskQueued {
		t.Errorf("task status = %q, want queued (should be requeued)", task.Status)
	}
	if task.WorkerID != "" {
		t.Errorf("task workerID = %q, want empty after requeue", task.WorkerID)
	}
}

func TestReapStaleWorkers_IdleWorkerMarkedDead(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	setWorkerHeartbeat(pm, "w-1", time.Now().Add(-2*time.Minute))

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("worker status = %q, want dead", w.Status)
	}

	// No tasks exist, so nothing to requeue.
	for _, task := range pm.Tasks() {
		if task.Status == TaskQueued {
			t.Errorf("unexpected queued task %s after reaping idle worker", task.ID)
		}
	}
}

func TestReapStaleWorkers_FreshHeartbeat(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.Heartbeat("w-1", "idle", "")

	reapStaleWorkers(pm, 90*time.Second)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle (fresh heartbeat)", w.Status)
	}
}

func TestReapStaleWorkers_MixedWorkers(t *testing.T) {
	pm := newTestPoolManager(t)
	timeout := 90 * time.Second

	// w-1: stale idle — should be reaped.
	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	setWorkerHeartbeat(pm, "w-1", time.Now().Add(-2*time.Minute))

	// w-2: fresh idle — should survive.
	pm.SpawnWorker(WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-2", "")
	pm.Heartbeat("w-2", "idle", "")

	// w-3: stale busy with task — should be reaped, task requeued.
	pm.SpawnWorker(WorkerSpec{ID: "w-3"})
	pm.RegisterWorker("w-3", "")
	pm.EnqueueTask(TaskSpec{ID: "t-1", Prompt: "work", Priority: 1})
	pm.DispatchTask("t-1", "w-3")
	setWorkerHeartbeat(pm, "w-3", time.Now().Add(-2*time.Minute))

	// w-4: recently spawned, no heartbeat — grace period protects.
	pm.SpawnWorker(WorkerSpec{ID: "w-4"})

	// w-5: already dead — should stay dead.
	pm.SpawnWorker(WorkerSpec{ID: "w-5"})
	pm.RegisterWorker("w-5", "")
	pm.MarkDead("w-5")

	reapStaleWorkers(pm, timeout)

	expectations := []struct {
		id   string
		want string
	}{
		{"w-1", WorkerDead},
		{"w-2", WorkerIdle},
		{"w-3", WorkerDead},
		{"w-4", WorkerSpawning},
		{"w-5", WorkerDead},
	}
	for _, tt := range expectations {
		w, ok := pm.Worker(tt.id)
		if !ok {
			t.Errorf("worker %s not found", tt.id)
			continue
		}
		if w.Status != tt.want {
			t.Errorf("worker %s: status = %q, want %q", tt.id, w.Status, tt.want)
		}
	}

	// Task from w-3 should be requeued.
	task, _ := pm.Task("t-1")
	if task.Status != TaskQueued {
		t.Errorf("task t-1: status = %q, want queued", task.Status)
	}
}

func TestStartReaper_StopsCleanly(t *testing.T) {
	pm := newTestPoolManager(t)
	stop := StartReaper(pm, 10*time.Millisecond, 90*time.Second)

	time.Sleep(30 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not return within 2s")
	}
}

func TestStartReaper_ReapsOnTick(t *testing.T) {
	pm := newTestPoolManager(t)

	pm.SpawnWorker(WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	setWorkerHeartbeat(pm, "w-1", time.Now().Add(-5*time.Minute))

	stop := StartReaper(pm, 50*time.Millisecond, 90*time.Second)
	defer stop()

	// Wait for at least one tick.
	time.Sleep(150 * time.Millisecond)

	w, _ := pm.Worker("w-1")
	if w.Status != WorkerDead {
		t.Errorf("worker status = %q, want dead (reaper should have reaped on tick)", w.Status)
	}
}
