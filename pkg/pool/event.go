package pool

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event type constants.
const (
	// Pool lifecycle.
	EventPoolCreated = "pool_created"

	// Worker lifecycle.
	EventWorkerSpawned = "worker_spawned"
	EventWorkerReady   = "worker_ready"
	EventWorkerBusy    = "worker_busy"
	EventWorkerIdle    = "worker_idle"
	EventWorkerBlocked = "worker_blocked"
	EventWorkerDead    = "worker_dead"

	// Task lifecycle.
	EventTaskCreated    = "task_created"
	EventTaskDispatched = "task_dispatched"
	EventTaskCompleted  = "task_completed"
	EventTaskFailed     = "task_failed"
	EventTaskCanceled   = "task_canceled"
	EventTaskRequeued   = "task_requeued"
	EventTaskDeleted    = "task_deleted"

	// Review lifecycle.
	EventReviewDispatched = "review_dispatched"
	EventReviewCompleted  = "review_completed"
	EventReviewAborted    = "review_aborted"
	EventTaskAccepted     = "task_accepted"
	EventTaskRejected     = "task_rejected"
	EventTaskEscalated    = "task_escalated"

	// Question lifecycle.
	EventWorkerQuestion   = "worker_question"
	EventQuestionAnswered = "question_answered"

	// Pipeline lifecycle.
	EventPipelineCreated   = "pipeline_created"
	EventPipelineCompleted = "pipeline_completed"
	EventPipelineFailed    = "pipeline_failed"
	EventPipelineBlocked   = "pipeline_blocked"
	EventPipelineUnblocked = "pipeline_unblocked"
	EventStageAdvanced     = "stage_advanced"
)

// Event is a single entry in the WAL.
type Event struct {
	Timestamp time.Time       `json:"ts"`
	Type      string          `json:"type"`
	Sequence  uint64          `json:"seq"`
	TaskID    string          `json:"taskId,omitempty"`
	WorkerID  string          `json:"workerId,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Typed payload structs for events that carry data.

type WorkerSpawnedData struct {
	ContainerID string `json:"containerId,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Role        string `json:"role,omitempty"`
	Token       string `json:"token,omitempty"`
}

type TaskCreatedData struct {
	Prompt         string   `json:"prompt"`
	Complexity     string   `json:"complexity,omitempty"`
	Priority       int      `json:"priority"`
	DependsOn      []string `json:"dependsOn,omitempty"`
	TimeoutMinutes int      `json:"timeoutMinutes,omitempty"`
	Role           string   `json:"role,omitempty"`
	MaxReviews     int      `json:"maxReviews,omitempty"`
	PipelineID     string   `json:"pipelineId,omitempty"`
	PlanID         string   `json:"planId,omitempty"`
	StageIndex     int      `json:"stageIndex,omitempty"`
}

type TaskRequeuedData struct {
	RequireFreshWorker bool `json:"requireFreshWorker,omitempty"`
}

type TaskDeletedData struct {
	QuestionIDs []string `json:"questionIds,omitempty"`
}

type TaskDispatchedData struct {
	WorkerID string `json:"workerId"`
}

type TaskCompletedData struct {
	Summary        string   `json:"summary,omitempty"`
	ContextSummary string   `json:"contextSummary,omitempty"`
	FilesChanged   []string `json:"filesChanged,omitempty"`
}

type TaskFailedData struct {
	Error        string          `json:"error"`
	FailureClass string          `json:"failureClass,omitempty"`
	Detail       json.RawMessage `json:"detail,omitempty"`
}

type ReviewDispatchedData struct {
	ReviewerID string `json:"reviewerId"`
}

type ReviewCompletedData struct {
	ReviewerID string `json:"reviewerId"`
	Verdict    string `json:"verdict"`
	Feedback   string `json:"feedback,omitempty"`
	Severity   string `json:"severity,omitempty"`
}

type WorkerQuestionData struct {
	QuestionID string   `json:"questionId"`
	Question   string   `json:"question"`
	Category   string   `json:"category,omitempty"`
	Options    []string `json:"options,omitempty"`
	Context    string   `json:"context,omitempty"`
	Blocking   bool     `json:"blocking"`
}

type QuestionAnsweredData struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answeredBy,omitempty"`
}

type EscalationData struct {
	Action string `json:"action"`
}

type PipelineCreatedData struct {
	Pipeline Pipeline `json:"pipeline"`
}

type PipelineFailedData struct {
	Reason string `json:"reason,omitempty"`
}

type PipelineBlockedData struct {
	PipelineID string `json:"pipelineId"`
	StageIndex int    `json:"stageIndex"`
}

type StageAdvancedData struct {
	PipelineID    string `json:"pipelineId"`
	StageIndex    int    `json:"stageIndex"`
	TriggerTaskID string `json:"triggerTaskId,omitempty"`
}

// marshalData marshals a payload to json.RawMessage.
func marshalData(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func clearWorkerLiveState(w *Worker) {
	if w == nil {
		return
	}
	w.CurrentTaskID = ""
	w.CurrentActivity = nil
	w.CurrentTool = ""
}

// Apply mutates the PoolManager's in-memory state based on an event.
// INVARIANT: the caller holds pm.mu (write lock). Apply must never lock pm.mu.
func Apply(pm *PoolManager, e Event) error {
	switch e.Type {
	case EventPoolCreated:
		// No-op for in-memory state; pool already exists.

	case EventWorkerSpawned:
		var d WorkerSpawnedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		pm.workers[e.WorkerID] = &Worker{
			ID:          e.WorkerID,
			ContainerID: d.ContainerID,
			Provider:    d.Provider,
			Role:        d.Role,
			Token:       d.Token,
			Status:      WorkerSpawning,
			SpawnedAt:   e.Timestamp,
		}

	case EventWorkerReady:
		w := pm.workers[e.WorkerID]
		if w != nil {
			if w.Status != WorkerDead {
				w.Status = WorkerIdle
			}
		}

	case EventWorkerBusy:
		w := pm.workers[e.WorkerID]
		if w != nil {
			w.Status = WorkerWorking
			if e.TaskID != "" {
				w.CurrentTaskID = e.TaskID
			}
		}

	case EventWorkerIdle:
		w := pm.workers[e.WorkerID]
		if w != nil {
			if w.Status != WorkerDead {
				w.Status = WorkerIdle
			}
			clearWorkerLiveState(w)
		}

	case EventWorkerBlocked:
		w := pm.workers[e.WorkerID]
		if w != nil {
			w.Status = WorkerBlocked
		}

	case EventWorkerDead:
		w := pm.workers[e.WorkerID]
		if w != nil {
			w.Status = WorkerDead
			w.Token = ""
			clearWorkerLiveState(w)
		}
		delete(pm.recycleReqs, e.WorkerID)

	case EventTaskCreated:
		var d TaskCreatedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		pm.tasks[e.TaskID] = &Task{
			ID:             e.TaskID,
			PipelineID:     d.PipelineID,
			PlanID:         d.PlanID,
			StageIndex:     d.StageIndex,
			Prompt:         d.Prompt,
			Complexity:     d.Complexity,
			Role:           d.Role,
			Priority:       d.Priority,
			DependsOn:      d.DependsOn,
			TimeoutMinutes: d.TimeoutMinutes,
			MaxReviews:     d.MaxReviews,
			Status:         TaskQueued,
			CreatedAt:      e.Timestamp,
		}

	case EventTaskDispatched:
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskDispatched
			t.WorkerID = e.WorkerID
			t.RequireFreshWorker = false
			now := e.Timestamp
			t.DispatchedAt = &now
		}
		w := pm.workers[e.WorkerID]
		if w != nil {
			w.Status = WorkerWorking
			w.CurrentTaskID = e.TaskID
		}

	case EventTaskCompleted:
		var d TaskCompletedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskCompleted
			now := e.Timestamp
			t.CompletedAt = &now
			if t.Result == nil {
				t.Result = &TaskResult{}
			}
			t.Result.Summary = d.Summary
			t.Result.FilesChanged = d.FilesChanged
		}
		w := pm.workers[e.WorkerID]
		if w != nil {
			if w.Status != WorkerDead {
				w.Status = WorkerIdle
			}
			clearWorkerLiveState(w)
		}

	case EventTaskFailed:
		var d TaskFailedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskFailed
			now := e.Timestamp
			t.CompletedAt = &now
			if t.Result == nil {
				t.Result = &TaskResult{}
			}
			t.Result.Error = d.Error
		}
		w := pm.workers[e.WorkerID]
		if w != nil {
			if w.Status != WorkerDead {
				w.Status = WorkerIdle
			}
			clearWorkerLiveState(w)
		}

	case EventTaskCanceled:
		t := pm.tasks[e.TaskID]
		assignedWorkerID := ""
		if t != nil {
			assignedWorkerID = t.WorkerID
		}
		if t != nil {
			t.Status = TaskCanceled
			now := e.Timestamp
			t.CompletedAt = &now
		}
		if assignedWorkerID != "" {
			w := pm.workers[assignedWorkerID]
			if w != nil && w.CurrentTaskID == e.TaskID {
				if w.Status != WorkerDead {
					w.Status = WorkerIdle
				}
				clearWorkerLiveState(w)
			}
		}

	case EventTaskRequeued:
		var d TaskRequeuedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		t := pm.tasks[e.TaskID]
		if t != nil {
			if t.Status == TaskFailed {
				t.RetryCount++
			}
			t.Status = TaskQueued
			t.WorkerID = ""
			t.ReviewerID = ""
			t.RequireFreshWorker = d.RequireFreshWorker
			t.DispatchedAt = nil
			t.CompletedAt = nil
			t.Result = nil
			t.Handover = nil
		}

	case EventTaskDeleted:
		var d TaskDeletedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		pm.queue.Remove(e.TaskID)
		delete(pm.tasks, e.TaskID)
		delete(pm.taskWaiters, e.TaskID)
		for _, qid := range d.QuestionIDs {
			if q := pm.questions[qid]; q != nil && q.Blocking {
				w := pm.workers[q.WorkerID]
				if w != nil && w.Status == WorkerBlocked {
					w.Status = WorkerIdle
					clearWorkerLiveState(w)
				}
			}
			delete(pm.questions, qid)
		}

	case EventReviewAborted:
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskCompleted
			t.ReviewerID = ""
		}

	case EventReviewDispatched:
		var d ReviewDispatchedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskReviewing
			t.ReviewerID = d.ReviewerID
		}

	case EventReviewCompleted:
		var d ReviewCompletedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Reviews = append(t.Reviews, ReviewRecord{
				ReviewerID: d.ReviewerID,
				Verdict:    d.Verdict,
				Feedback:   d.Feedback,
				Severity:   d.Severity,
				ReviewedAt: e.Timestamp,
			})
			t.ReviewCycles++
		}

	case EventTaskAccepted:
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskAccepted
		}
		// Mark reviewer idle.
		if t != nil && t.ReviewerID != "" {
			w := pm.workers[t.ReviewerID]
			if w != nil {
				if w.Status != WorkerDead {
					w.Status = WorkerIdle
				}
				clearWorkerLiveState(w)
			}
		}

	case EventTaskRejected:
		t := pm.tasks[e.TaskID]
		// Mark reviewer idle before clearing ReviewerID.
		if t != nil && t.ReviewerID != "" {
			w := pm.workers[t.ReviewerID]
			if w != nil {
				if w.Status != WorkerDead {
					w.Status = WorkerIdle
				}
				clearWorkerLiveState(w)
			}
		}
		if t != nil {
			t.Status = TaskRejected
			t.ReviewerID = ""
		}

	case EventTaskEscalated:
		t := pm.tasks[e.TaskID]
		if t != nil {
			t.Status = TaskEscalated
		}
		if t != nil && t.ReviewerID != "" {
			w := pm.workers[t.ReviewerID]
			if w != nil {
				if w.Status != WorkerDead {
					w.Status = WorkerIdle
				}
				clearWorkerLiveState(w)
			}
		}

	case EventWorkerQuestion:
		var d WorkerQuestionData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		pm.questions[d.QuestionID] = &Question{
			ID:       d.QuestionID,
			WorkerID: e.WorkerID,
			TaskID:   e.TaskID,
			Question: d.Question,
			Category: d.Category,
			Options:  d.Options,
			Context:  d.Context,
			Blocking: d.Blocking,
			AskedAt:  e.Timestamp,
		}
		if d.Blocking {
			w := pm.workers[e.WorkerID]
			if w != nil && w.Status != WorkerDead {
				w.Status = WorkerBlocked
			}
		}

	case EventQuestionAnswered:
		var d QuestionAnsweredData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		q := pm.questions[d.QuestionID]
		if q != nil {
			q.Answer = d.Answer
			q.Answered = true
			q.AnsweredBy = d.AnsweredBy
			q.AnsweredAt = e.Timestamp
			if q.Blocking {
				w := pm.workers[q.WorkerID]
				if w != nil && w.Status == WorkerBlocked {
					w.Status = WorkerWorking
				}
			}
		}

	case EventPipelineCreated:
		var d PipelineCreatedData
		if e.Data != nil {
			json.Unmarshal(e.Data, &d)
		}
		p := d.Pipeline
		p.Status = PipelineRunning
		pm.pipes[p.ID] = &p

	case EventPipelineCompleted:
		p := pm.pipes[e.TaskID]
		if p != nil {
			p.Status = PipelineCompleted
			now := e.Timestamp
			p.CompletedAt = &now
		}

	case EventPipelineFailed:
		p := pm.pipes[e.TaskID]
		if p != nil {
			p.Status = PipelineFailed
		}

	case EventPipelineBlocked:
		p := pm.pipes[e.TaskID]
		if p != nil {
			p.Status = PipelineBlocked
		}

	case EventPipelineUnblocked:
		p := pm.pipes[e.TaskID]
		if p != nil {
			p.Status = PipelineRunning
		}

	case EventStageAdvanced:
		// Audit trail only; no in-memory state change needed.

	default:
		return fmt.Errorf("apply event: unknown type %q", e.Type)
	}
	return nil
}
