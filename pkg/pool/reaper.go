package pool

import (
	"time"
)

// StartReaper runs a goroutine that marks workers dead after missed heartbeats
// and re-queues their orphaned tasks. Returns a stop function.
func StartReaper(pm *PoolManager, interval, timeout time.Duration) (stop func()) {
	return StartReaperWithReservations(pm, interval, timeout, nil)
}

func StartReaperWithReservations(pm *PoolManager, interval, timeout time.Duration, reserved func() map[string]string) (stop func()) {
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				reapStaleWorkersWithReservations(pm, timeout, reserved)
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

// reapStaleWorkers marks workers dead if their heartbeat exceeds timeout.
// Newly spawned workers (< 2 min) in spawning status get a grace period.
// Orphaned task requeue is handled atomically inside MarkDeadIfStale.
func reapStaleWorkers(pm *PoolManager, timeout time.Duration) {
	reapStaleWorkersWithReservations(pm, timeout, nil)
}

func reapStaleWorkersWithReservations(pm *PoolManager, timeout time.Duration, reserved func() map[string]string) {
	// Collect candidate worker IDs from a snapshot, then use MarkDeadIfStale
	// which re-checks under lock to avoid TOCTOU races. MarkDeadIfStale also
	// requeues orphaned tasks inline so there's no window for CompleteTask to
	// race between mark-dead and requeue.
	protected := map[string]string(nil)
	if reserved != nil {
		protected = reserved()
	}
	for _, w := range pm.Workers() {
		if w.Status == WorkerDead {
			continue
		}
		if _, ok := protected[w.ID]; ok {
			continue
		}
		pm.MarkDeadIfStale(w.ID, timeout)
	}
}
