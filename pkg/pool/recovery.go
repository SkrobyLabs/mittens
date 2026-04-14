package pool

import (
	"context"
	"log"
	"time"
)

// RequeueOrphanedTasks finds dispatched tasks on dead workers and re-enqueues
// them. Returns the count of re-queued tasks.
func RequeueOrphanedTasks(pm *PoolManager) int {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	requeued := 0
	for _, t := range pm.tasks {
		switch t.Status {
		case TaskDispatched:
			w := pm.workers[t.WorkerID]
			if w == nil || w.Status == WorkerDead {
				e := Event{
					Timestamp: time.Now(),
					Type:      EventTaskRequeued,
					TaskID:    t.ID,
				}
				if _, err := pm.wal.Append(e); err != nil {
					log.Printf("recovery: WAL append failed for task %s requeue: %v", t.ID, err)
					continue
				}
				Apply(pm, e)
				pm.queue.Push(t.ID, t.Priority, t.DependsOn)
				requeued++
			}
		}
	}
	return requeued
}

// Reconcile matches WAL-recovered pool state against discovered session containers.
// A worker counts as live only when at least one discovered container is running.
// Workers without a running container are marked dead. Stale worker containers and
// orphaned worker containers are removed best-effort during the same pass.
// Returns counts of reconciled (marked dead) workers and removed worker containers.
func Reconcile(pm *PoolManager, containers []ContainerInfo) (reconciled int, killed int) {
	runningWorkers := make(map[string]bool, len(containers))
	staleWorkers := make(map[string]bool, len(containers))
	for _, c := range containers {
		if c.WorkerID == "" {
			continue
		}
		if c.IsRunning() {
			runningWorkers[c.WorkerID] = true
			continue
		}
		staleWorkers[c.WorkerID] = true
	}

	workers := pm.Workers()
	knownWorkers := make(map[string]string, len(workers))

	// Mark workers without a running container as dead.
	for _, w := range workers {
		knownWorkers[w.ID] = w.Status
		if w.Status == WorkerDead {
			continue
		}
		if !runningWorkers[w.ID] {
			if err := pm.MarkDead(w.ID); err == nil {
				reconciled++
			}
		}
	}

	cleanupWorkers := make(map[string]struct{})
	for wid := range staleWorkers {
		status, known := knownWorkers[wid]
		if !known || status == WorkerDead || !runningWorkers[wid] {
			cleanupWorkers[wid] = struct{}{}
		}
	}
	for wid := range runningWorkers {
		status, known := knownWorkers[wid]
		if !known || status == WorkerDead {
			cleanupWorkers[wid] = struct{}{}
		}
	}

	pm.mu.RLock()
	hostAPI := pm.hostAPI
	pm.mu.RUnlock()

	if hostAPI != nil {
		for wid := range cleanupWorkers {
			if err := hostAPI.KillWorker(context.Background(), wid); err == nil {
				killed++
			}
		}
	}

	return reconciled, killed
}
