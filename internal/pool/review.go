package pool

import (
	"fmt"
	"time"
)

// DispatchReview assigns a completed or rejected task to a reviewer.
func (pm *PoolManager) DispatchReview(taskID, reviewerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("dispatch review: task %q not found", taskID)
	}
	if t.Status != TaskCompleted && t.Status != TaskRejected {
		return fmt.Errorf("dispatch review: task %q is %q, expected completed or rejected", taskID, t.Status)
	}

	w := pm.workers[reviewerID]
	if w == nil {
		return fmt.Errorf("dispatch review: reviewer %q not found", reviewerID)
	}
	if w.Status != WorkerIdle {
		return fmt.Errorf("dispatch review: reviewer %q is %q, expected idle", reviewerID, w.Status)
	}
	// No self-review: the implementation worker cannot review their own task.
	if t.WorkerID == reviewerID {
		return fmt.Errorf("dispatch review: reviewer %q cannot self-review task %q", reviewerID, taskID)
	}

	// WAL: review dispatched.
	e1 := Event{
		Timestamp: time.Now(),
		Type:      EventReviewDispatched,
		TaskID:    taskID,
		Data:      marshalData(ReviewDispatchedData{ReviewerID: reviewerID}),
	}
	if _, err := pm.wal.Append(e1); err != nil {
		return fmt.Errorf("dispatch review wal: %w", err)
	}
	Apply(pm, e1)

	// WAL: reviewer busy (TaskID carried so Apply sets CurrentTaskID).
	e2 := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerBusy,
		WorkerID:  reviewerID,
		TaskID:    taskID,
	}
	if _, err := pm.wal.Append(e2); err != nil {
		return fmt.Errorf("dispatch review wal (busy): %w", err)
	}
	Apply(pm, e2)

	return nil
}

// ReportReview records a review verdict and transitions the task accordingly:
// pass -> accepted; fail with cycles < max -> rejected; fail with cycles >= max -> escalated.
func (pm *PoolManager) ReportReview(taskID, verdict, feedback, severity string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("report review: task %q not found", taskID)
	}
	if t.Status != TaskReviewing {
		return fmt.Errorf("report review: task %q is %q, expected reviewing", taskID, t.Status)
	}

	// WAL: review completed.
	e1 := Event{
		Timestamp: time.Now(),
		Type:      EventReviewCompleted,
		TaskID:    taskID,
		Data: marshalData(ReviewCompletedData{
			ReviewerID: t.ReviewerID,
			Verdict:    verdict,
			Feedback:   feedback,
			Severity:   severity,
		}),
	}
	if _, err := pm.wal.Append(e1); err != nil {
		return fmt.Errorf("report review wal: %w", err)
	}
	Apply(pm, e1)

	// Determine next state based on verdict.
	var nextEvent Event
	switch verdict {
	case ReviewPass:
		nextEvent = Event{
			Timestamp: time.Now(),
			Type:      EventTaskAccepted,
			TaskID:    taskID,
		}
	case ReviewFail:
		if t.ReviewCycles >= t.MaxReviews {
			nextEvent = Event{
				Timestamp: time.Now(),
				Type:      EventTaskEscalated,
				TaskID:    taskID,
			}
		} else {
			nextEvent = Event{
				Timestamp: time.Now(),
				Type:      EventTaskRejected,
				TaskID:    taskID,
			}
		}
	default:
		return fmt.Errorf("report review: unknown verdict %q", verdict)
	}

	if _, err := pm.wal.Append(nextEvent); err != nil {
		return fmt.Errorf("report review wal (outcome): %w", err)
	}
	Apply(pm, nextEvent)

	pm.sendNotify(Notification{Type: "review_" + verdict, ID: taskID})
	return nil
}

// ResolveEscalation handles an escalated task:
// accept -> accepted; retry -> increase MaxReviews + rejected; abort -> canceled.
func (pm *PoolManager) ResolveEscalation(taskID, action string, extraCycles int) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("resolve escalation: task %q not found", taskID)
	}
	if t.Status != TaskEscalated {
		return fmt.Errorf("resolve escalation: task %q is %q, expected escalated", taskID, t.Status)
	}

	var e Event
	switch action {
	case EscalationAccept:
		e = Event{
			Timestamp: time.Now(),
			Type:      EventTaskAccepted,
			TaskID:    taskID,
		}
	case EscalationRetry:
		e = Event{
			Timestamp: time.Now(),
			Type:      EventTaskRejected,
			TaskID:    taskID,
		}
	case EscalationAbort:
		e = Event{
			Timestamp: time.Now(),
			Type:      EventTaskCanceled,
			TaskID:    taskID,
		}
	default:
		return fmt.Errorf("resolve escalation: unknown action %q", action)
	}

	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("resolve escalation wal: %w", err)
	}
	// Update MaxReviews after successful WAL write to avoid state-WAL desync.
	if action == EscalationRetry {
		t.MaxReviews += extraCycles
	}
	Apply(pm, e)

	pm.sendNotify(Notification{Type: "escalation_" + action, ID: taskID})
	return nil
}

// AbortReview cleans up a failed review: reverts the task to completed and
// releases the reviewer worker back to idle.
func (pm *PoolManager) AbortReview(workerID, taskID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("abort review: task %q not found", taskID)
	}
	if t.Status != TaskReviewing {
		return fmt.Errorf("abort review: task %q is %q, expected reviewing", taskID, t.Status)
	}
	if t.ReviewerID != workerID {
		return fmt.Errorf("abort review: worker %q is not the reviewer for task %q", workerID, taskID)
	}

	// WAL: review aborted — clears ReviewerID and reverts task to completed.
	e1 := Event{
		Timestamp: time.Now(),
		Type:      EventReviewAborted,
		TaskID:    taskID,
	}
	if _, err := pm.wal.Append(e1); err != nil {
		return fmt.Errorf("abort review wal: %w", err)
	}
	Apply(pm, e1)

	// WAL: reviewer idle — releases the worker back to idle status.
	e2 := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerIdle,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e2); err != nil {
		return fmt.Errorf("abort review wal (idle): %w", err)
	}
	Apply(pm, e2)

	return nil
}

// PickReviewer selects an idle reviewer worker for a task.
// Prefers workers with the "reviewer" role, avoids self-review, prefers fresh eyes.
func (pm *PoolManager) PickReviewer(taskID string) (string, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	t := pm.tasks[taskID]
	if t == nil {
		return "", fmt.Errorf("pick reviewer: task %q not found", taskID)
	}

	// Collect prior reviewer IDs for this task.
	priorReviewers := make(map[string]bool)
	for _, r := range t.Reviews {
		priorReviewers[r.ReviewerID] = true
	}

	var bestID string
	bestScore := -1

	for _, w := range pm.workers {
		if w.Status != WorkerIdle {
			continue
		}
		if w.ID == t.WorkerID {
			continue // no self-review
		}

		score := 0
		if w.Role == "reviewer" {
			score += 10
		}
		if !priorReviewers[w.ID] {
			score += 5 // fresh eyes bonus
		}

		if score > bestScore {
			bestScore = score
			bestID = w.ID
		}
	}

	if bestID == "" {
		return "", fmt.Errorf("pick reviewer: no idle workers available for task %q", taskID)
	}
	return bestID, nil
}
