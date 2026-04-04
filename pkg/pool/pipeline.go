package pool

import (
	"fmt"
	"strings"
	"time"
)

// PipelineExecutor watches task state changes and auto-dispatches successor stages.
// It is NOT a goroutine — its methods are called from the notification consumer.
type PipelineExecutor struct {
	pm *PoolManager
}

// NewPipelineExecutor creates a PipelineExecutor bound to a PoolManager.
func NewPipelineExecutor(pm *PoolManager) *PipelineExecutor {
	return &PipelineExecutor{pm: pm}
}

// OnTaskEvent is called when a task changes state. It drives pipeline
// stage advancement, failure handling, and escalation.
func (pe *PipelineExecutor) OnTaskEvent(taskID, newState string) {
	task, ok := pe.pm.Task(taskID)
	if !ok || task.PipelineID == "" {
		return
	}

	pipe, ok := pe.pm.GetPipeline(task.PipelineID)
	if !ok {
		return
	}
	if pipe.Status != PipelineRunning && pipe.Status != PipelineBlocked {
		return
	}

	switch newState {
	case TaskCompleted:
		pe.advanceStage(pipe, task)
	case TaskAccepted:
		pe.advanceStage(pipe, task)
	case TaskFailed:
		pe.handleFailure(pipe, task)
	case TaskEscalated:
		pe.pm.mu.Lock()
		e := Event{
			Timestamp: time.Now(),
			Type:      EventPipelineBlocked,
			TaskID:    pipe.ID,
			Data:      marshalData(PipelineBlockedData{PipelineID: pipe.ID, StageIndex: task.StageIndex}),
		}
		if _, err := pe.pm.wal.Append(e); err == nil {
			Apply(pe.pm, e)
		}
		pe.pm.mu.Unlock()
		pe.pm.sendNotify(Notification{
			Type:    "pipeline_blocked",
			ID:      pipe.ID,
			Message: fmt.Sprintf("task %s escalated", taskID),
		})
	}
}

// advanceStage checks if the current stage is done and dispatches the next.
func (pe *PipelineExecutor) advanceStage(pipe *Pipeline, completedTask *Task) {
	if completedTask.StageIndex < 0 || completedTask.StageIndex >= len(pipe.Stages) {
		return
	}

	stage := pipe.Stages[completedTask.StageIndex]
	nextIdx := completedTask.StageIndex + 1

	switch stage.Fan {
	case Streaming:
		// Don't wait for siblings — advance this task's successor immediately.
		// But if the next stage has only 1 task (fan-in), wait for all.
		if nextIdx < len(pipe.Stages) && len(pipe.Stages[nextIdx].Tasks) == 1 {
			if !pe.allStageDone(pipe, completedTask.StageIndex) {
				return
			}
			handovers := pe.collectHandovers(pipe, completedTask.StageIndex)
			pe.enqueueStage(pipe, nextIdx, handovers)
			return
		}
		// Per-task streaming advancement.
		handover := ""
		if completedTask.Handover != nil && completedTask.Handover.ContextForNext != "" {
			handover = completedTask.Handover.ContextForNext
		}
		pe.enqueueStreamingSuccessor(pipe, completedTask, nextIdx, handover)

	case FanOut, FanIn:
		if !pe.allStageDone(pipe, completedTask.StageIndex) {
			return
		}
		if nextIdx >= len(pipe.Stages) {
			pe.completePipeline(pipe)
			return
		}
		nextStage := pipe.Stages[nextIdx]
		if !nextStage.AutoAdvance {
			pe.pm.mu.Lock()
			e := Event{
				Timestamp: time.Now(),
				Type:      EventPipelineBlocked,
				TaskID:    pipe.ID,
				Data:      marshalData(PipelineBlockedData{PipelineID: pipe.ID, StageIndex: nextIdx}),
			}
			if _, err := pe.pm.wal.Append(e); err == nil {
				Apply(pe.pm, e)
			}
			pe.pm.mu.Unlock()
			pe.pm.sendNotify(Notification{
				Type:    "pipeline_blocked",
				ID:      pipe.ID,
				Message: fmt.Sprintf("awaiting approval for stage: %s", nextStage.Name),
			})
			return
		}
		handovers := pe.collectHandovers(pipe, completedTask.StageIndex)
		pe.enqueueStage(pipe, nextIdx, handovers)

	default:
		// Unknown fan mode — treat as fan-out.
		if !pe.allStageDone(pipe, completedTask.StageIndex) {
			return
		}
		if nextIdx >= len(pipe.Stages) {
			pe.completePipeline(pipe)
			return
		}
		handovers := pe.collectHandovers(pipe, completedTask.StageIndex)
		pe.enqueueStage(pipe, nextIdx, handovers)
	}
}

// enqueueStreamingSuccessor dispatches a single successor task for a streaming stage.
func (pe *PipelineExecutor) enqueueStreamingSuccessor(pipe *Pipeline, completedTask *Task, nextIdx int, handover string) {
	if nextIdx >= len(pipe.Stages) {
		// Check if all siblings are also done before completing pipeline.
		if pe.allStageDone(pipe, completedTask.StageIndex) {
			pe.completePipeline(pipe)
		}
		return
	}

	nextStage := pipe.Stages[nextIdx]
	if !nextStage.AutoAdvance {
		// For streaming with AutoAdvance=false, wait for all siblings then block.
		if pe.allStageDone(pipe, completedTask.StageIndex) {
			pe.pm.mu.Lock()
			e := Event{
				Timestamp: time.Now(),
				Type:      EventPipelineBlocked,
				TaskID:    pipe.ID,
				Data:      marshalData(PipelineBlockedData{PipelineID: pipe.ID, StageIndex: nextIdx}),
			}
			if _, err := pe.pm.wal.Append(e); err == nil {
				Apply(pe.pm, e)
			}
			pe.pm.mu.Unlock()
			pe.pm.sendNotify(Notification{
				Type:    "pipeline_blocked",
				ID:      pipe.ID,
				Message: fmt.Sprintf("awaiting approval for stage: %s", nextStage.Name),
			})
		}
		return
	}

	// Find the positional index of this task within its stage.
	stageTasks := pe.pm.PipelineStageTasks(pipe.ID, completedTask.StageIndex)
	posIdx := 0
	for i, st := range stageTasks {
		if st.ID == completedTask.ID {
			posIdx = i
			break
		}
	}

	// Map to successor: if next stage has fewer tasks, map excess to last task.
	succIdx := posIdx
	if succIdx >= len(nextStage.Tasks) {
		succIdx = len(nextStage.Tasks) - 1
	}

	// Check if this successor was already enqueued (by a sibling that mapped to the same slot).
	succTID := fmt.Sprintf("%s-s%d-t%d", pipe.ID, nextIdx, succIdx)
	if _, exists := pe.pm.Task(succTID); exists {
		return
	}

	st := nextStage.Tasks[succIdx]
	prompt := st.PromptTmpl
	prompt = strings.ReplaceAll(prompt, "{{.Goal}}", pipe.Goal)
	prompt = strings.ReplaceAll(prompt, "{{.PriorContext}}", handover)

	if _, err := pe.pm.EnqueueTask(TaskSpec{
		ID:         succTID,
		PipelineID: pipe.ID,
		StageIndex: nextIdx,
		Prompt:     prompt,
		Role:       nextStage.Role,
		Priority:   1,
		DependsOn:  st.DependsOn,
	}); err != nil {
		return
	}

	// WAL audit trail.
	pe.pm.mu.Lock()
	e := Event{
		Timestamp: time.Now(),
		Type:      EventStageAdvanced,
		TaskID:    pipe.ID,
		Data: marshalData(StageAdvancedData{
			PipelineID:    pipe.ID,
			StageIndex:    nextIdx,
			TriggerTaskID: completedTask.ID,
		}),
	}
	if _, err := pe.pm.wal.Append(e); err == nil {
		Apply(pe.pm, e)
	}
	pe.pm.mu.Unlock()
}

// enqueueStage enqueues all tasks for a pipeline stage.
func (pe *PipelineExecutor) enqueueStage(pipe *Pipeline, stageIdx int, priorHandovers []*TaskHandover) {
	stage := pipe.Stages[stageIdx]
	ctx := aggregateHandovers(priorHandovers)

	for i, st := range stage.Tasks {
		prompt := st.PromptTmpl
		prompt = strings.ReplaceAll(prompt, "{{.Goal}}", pipe.Goal)
		prompt = strings.ReplaceAll(prompt, "{{.PriorContext}}", ctx)

		tid := fmt.Sprintf("%s-s%d-t%d", pipe.ID, stageIdx, i)
		if _, err := pe.pm.EnqueueTask(TaskSpec{
			ID:         tid,
			PipelineID: pipe.ID,
			StageIndex: stageIdx,
			Prompt:     prompt,
			Role:       stage.Role,
			Priority:   1,
			DependsOn:  st.DependsOn,
		}); err != nil {
			continue
		}
	}

	// WAL audit trail.
	pe.pm.mu.Lock()
	e := Event{
		Timestamp: time.Now(),
		Type:      EventStageAdvanced,
		TaskID:    pipe.ID,
		Data: marshalData(StageAdvancedData{
			PipelineID: pipe.ID,
			StageIndex: stageIdx,
		}),
	}
	if _, err := pe.pm.wal.Append(e); err == nil {
		Apply(pe.pm, e)
	}
	pe.pm.mu.Unlock()
}

// allStageDone returns true if all tasks in the given stage are in a terminal state.
func (pe *PipelineExecutor) allStageDone(pipe *Pipeline, stageIdx int) bool {
	tasks := pe.pm.PipelineStageTasks(pipe.ID, stageIdx)
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		switch t.Status {
		case TaskCompleted, TaskAccepted, TaskCanceled:
			continue
		default:
			return false
		}
	}
	return true
}

// collectHandovers gathers handover data from all tasks in a stage.
func (pe *PipelineExecutor) collectHandovers(pipe *Pipeline, stageIdx int) []*TaskHandover {
	tasks := pe.pm.PipelineStageTasks(pipe.ID, stageIdx)
	var handovers []*TaskHandover
	for _, t := range tasks {
		if t.Handover != nil {
			h := *t.Handover
			handovers = append(handovers, &h)
		}
	}
	return handovers
}

// completePipeline marks a pipeline as completed.
func (pe *PipelineExecutor) completePipeline(pipe *Pipeline) {
	pe.pm.mu.Lock()
	e := Event{
		Timestamp: time.Now(),
		Type:      EventPipelineCompleted,
		TaskID:    pipe.ID,
	}
	if _, err := pe.pm.wal.Append(e); err == nil {
		Apply(pe.pm, e)
	}
	pe.pm.mu.Unlock()

	pe.pm.sendNotify(Notification{Type: "pipeline_completed", ID: pipe.ID})
}

// handleFailure marks a pipeline as failed when a task fails.
func (pe *PipelineExecutor) handleFailure(pipe *Pipeline, failedTask *Task) {
	// Cancel remaining queued tasks.
	pe.pm.mu.Lock()
	for _, t := range pe.pm.tasks {
		if t.PipelineID != pipe.ID {
			continue
		}
		if t.Status == TaskQueued {
			ce := Event{
				Timestamp: time.Now(),
				Type:      EventTaskCanceled,
				TaskID:    t.ID,
			}
			if _, err := pe.pm.wal.Append(ce); err == nil {
				Apply(pe.pm, ce)
			}
		}
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventPipelineFailed,
		TaskID:    pipe.ID,
		Data:      marshalData(PipelineFailedData{Reason: fmt.Sprintf("task %s failed", failedTask.ID)}),
	}
	if _, err := pe.pm.wal.Append(e); err == nil {
		Apply(pe.pm, e)
	}
	pe.pm.mu.Unlock()

	pe.pm.sendNotify(Notification{Type: "pipeline_failed", ID: pipe.ID})
}

// AdvanceStage manually advances a blocked pipeline to the next stage.
func (pe *PipelineExecutor) AdvanceStage(pipeID string) error {
	pipe, ok := pe.pm.GetPipeline(pipeID)
	if !ok {
		return fmt.Errorf("advance stage: pipeline %q not found", pipeID)
	}
	if pipe.Status != PipelineBlocked {
		return fmt.Errorf("advance stage: pipeline %q is %q, expected blocked", pipeID, pipe.Status)
	}

	// Find the last completed stage.
	lastCompleted := -1
	for i := range pipe.Stages {
		tasks := pe.pm.PipelineStageTasks(pipeID, i)
		if len(tasks) == 0 {
			break
		}
		allDone := true
		for _, t := range tasks {
			if t.Status != TaskCompleted && t.Status != TaskAccepted && t.Status != TaskCanceled {
				allDone = false
				break
			}
		}
		if allDone {
			lastCompleted = i
		} else {
			break
		}
	}

	nextIdx := lastCompleted + 1
	if nextIdx >= len(pipe.Stages) {
		pe.completePipeline(pipe)
		return nil
	}

	// Unblock the pipeline.
	pe.pm.mu.Lock()
	e := Event{
		Timestamp: time.Now(),
		Type:      EventPipelineUnblocked,
		TaskID:    pipeID,
		Data:      marshalData(PipelineBlockedData{PipelineID: pipeID, StageIndex: nextIdx}),
	}
	if _, err := pe.pm.wal.Append(e); err == nil {
		Apply(pe.pm, e)
	}
	pe.pm.mu.Unlock()

	handovers := pe.collectHandovers(pipe, lastCompleted)
	pe.enqueueStage(pipe, nextIdx, handovers)
	return nil
}

// ScanStuckPipelines finds running pipelines where the current stage is fully
// completed but the next stage was never triggered (e.g. due to a crash losing
// the completion notification). It triggers advanceStage for each stuck pipeline.
func (pe *PipelineExecutor) ScanStuckPipelines() {
	pe.pm.mu.RLock()
	pipes := make([]*Pipeline, 0, len(pe.pm.pipes))
	for _, p := range pe.pm.pipes {
		if p.Status == PipelineRunning || p.Status == PipelineBlocked {
			cp := *p
			cp.Stages = make([]Stage, len(p.Stages))
			copy(cp.Stages, p.Stages)
			pipes = append(pipes, &cp)
		}
	}
	pe.pm.mu.RUnlock()

	for _, pipe := range pipes {
		for i := range pipe.Stages {
			if !pe.allStageDone(pipe, i) {
				break
			}
			nextIdx := i + 1
			if nextIdx >= len(pipe.Stages) {
				// All stages complete but pipeline not marked done.
				pe.completePipeline(pipe)
				break
			}
			// Check if next stage has any tasks enqueued.
			nextTasks := pe.pm.PipelineStageTasks(pipe.ID, nextIdx)
			if len(nextTasks) == 0 {
				nextStage := pipe.Stages[nextIdx]
				if !nextStage.AutoAdvance {
					// Approval gate — block the pipeline instead of advancing.
					if pipe.Status != PipelineBlocked {
						pe.pm.mu.Lock()
						e := Event{
							Timestamp: time.Now(),
							Type:      EventPipelineBlocked,
							TaskID:    pipe.ID,
							Data:      marshalData(PipelineBlockedData{PipelineID: pipe.ID, StageIndex: nextIdx}),
						}
						if _, err := pe.pm.wal.Append(e); err == nil {
							Apply(pe.pm, e)
						}
						pe.pm.mu.Unlock()
					}
					break
				}
				// Next stage was never triggered — advance now.
				handovers := pe.collectHandovers(pipe, i)
				pe.enqueueStage(pipe, nextIdx, handovers)
				break
			}
		}
	}
}

// aggregateHandovers combines handover context from multiple tasks.
func aggregateHandovers(handovers []*TaskHandover) string {
	var parts []string
	for _, h := range handovers {
		if h.ContextForNext != "" {
			parts = append(parts, fmt.Sprintf("## Task %s handover\n%s", h.TaskID, h.ContextForNext))
		}
	}
	result := strings.Join(parts, "\n\n")
	if len(result) > 4000 {
		result = result[:4000] + "\n\n[context truncated]"
	}
	return result
}
