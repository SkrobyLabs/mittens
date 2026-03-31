package pool

import (
	"context"
	"log"
	"time"
)

// RequeueOrphanedTasks finds dispatched tasks on dead workers and re-enqueues them.
// Tasks in TaskReviewing on dead reviewers are reset to TaskQueued for re-dispatch.
// Returns the count of re-queued tasks.
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

		case TaskReviewing:
			w := pm.workers[t.ReviewerID]
			if w == nil || w.Status == WorkerDead {
				e := Event{
					Timestamp: time.Now(),
					Type:      EventReviewAborted,
					TaskID:    t.ID,
				}
				if _, err := pm.wal.Append(e); err != nil {
					log.Printf("recovery: WAL append failed for task %s requeue: %v", t.ID, err)
					continue
				}
				Apply(pm, e)
				// Don't push to queue — task is already completed,
				// it just needs to be re-dispatched for review.
				requeued++
			}
		}
	}
	return requeued
}

// Reconcile matches WAL-recovered pool state against running Docker containers.
// Workers not found in the running set are marked dead.
// Running containers not matching any pool worker are killed as orphans.
// Returns counts of reconciled (marked dead) workers and killed orphans.
func Reconcile(pm *PoolManager, running []ContainerInfo) (reconciled int, killed int) {
	runningSet := make(map[string]ContainerInfo, len(running))
	for _, c := range running {
		if c.WorkerID != "" {
			runningSet[c.WorkerID] = c
		}
	}

	// Mark missing workers as dead.
	for _, w := range pm.Workers() {
		if w.Status == WorkerDead {
			continue
		}
		if _, found := runningSet[w.ID]; !found {
			if err := pm.MarkDead(w.ID); err == nil {
				reconciled++
			}
		}
	}

	// Kill orphan containers not in pool state.
	pm.mu.RLock()
	knownWorkers := make(map[string]bool, len(pm.workers))
	for wid := range pm.workers {
		knownWorkers[wid] = true
	}
	hostAPI := pm.hostAPI
	pm.mu.RUnlock()

	if hostAPI != nil {
		for _, c := range running {
			if c.WorkerID != "" && !knownWorkers[c.WorkerID] {
				if err := hostAPI.KillWorker(context.Background(), c.WorkerID); err == nil {
					killed++
				}
			}
		}
	}

	return reconciled, killed
}
