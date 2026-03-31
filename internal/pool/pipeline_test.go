package pool

import (
	"path/filepath"
	"strings"
	"testing"
)

// --- helpers ---

func twoStagePipeline(fan0, fan1 FanMode, auto1 bool) Pipeline {
	return Pipeline{
		Goal: "test goal",
		Stages: []Stage{
			{
				Name: "stage-0", Role: "planner", Fan: fan0, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "s0-t0", PromptTmpl: "do {{.Goal}}"},
				},
			},
			{
				Name: "stage-1", Role: "reviewer", Fan: fan1, AutoAdvance: auto1,
				Tasks: []StageTask{
					{ID: "s1-t0", PromptTmpl: "review {{.PriorContext}}"},
				},
			},
		},
	}
}

func spawnIdleWorker(pm *PoolManager, id string) {
	pm.SpawnWorker(WorkerSpec{ID: id})
	pm.RegisterWorker(id, "")
}

func completeTaskDirect(pm *PoolManager, workerID, taskID string, handover *TaskHandover) {
	completeTestTask(pm, workerID, taskID, TaskResult{Summary: "done"}, handover)
}

// drainNotify reads all pending notifications without blocking.
func drainNotify(pm *PoolManager) []Notification {
	var out []Notification
	for {
		select {
		case n := <-pm.Notify():
			out = append(out, n)
		default:
			return out
		}
	}
}

// --- SubmitPipeline tests ---

func TestSubmitPipeline_Basic(t *testing.T) {
	pm := newTestPoolManager(t)

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, err := pm.SubmitPipeline(pipe)
	if err != nil {
		t.Fatal(err)
	}
	if pipeID == "" {
		t.Error("pipeline ID should not be empty")
	}

	p, ok := pm.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found")
	}
	if p.Status != PipelineRunning {
		t.Errorf("status = %q, want running", p.Status)
	}

	// Stage 0 tasks should be enqueued.
	tasks := pm.PipelineStageTasks(pipeID, 0)
	if len(tasks) != 1 {
		t.Fatalf("stage 0 tasks = %d, want 1", len(tasks))
	}
	if tasks[0].Status != TaskQueued {
		t.Errorf("task status = %q, want queued", tasks[0].Status)
	}
	if tasks[0].PipelineID != pipeID {
		t.Errorf("task pipelineId = %q, want %q", tasks[0].PipelineID, pipeID)
	}
}

func TestSubmitPipeline_Empty(t *testing.T) {
	pm := newTestPoolManager(t)
	_, err := pm.SubmitPipeline(Pipeline{Goal: "x"})
	if err == nil {
		t.Error("expected error for pipeline with no stages")
	}
}

func TestSubmitPipeline_EmptyStage(t *testing.T) {
	pm := newTestPoolManager(t)
	_, err := pm.SubmitPipeline(Pipeline{
		Goal: "x",
		Stages: []Stage{
			{Name: "empty", Tasks: nil},
		},
	})
	if err == nil {
		t.Error("expected error for stage with no tasks")
	}
}

func TestGetPipeline(t *testing.T) {
	pm := newTestPoolManager(t)
	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)

	p, ok := pm.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found")
	}
	if p.Goal != "test goal" {
		t.Errorf("goal = %q, want 'test goal'", p.Goal)
	}

	_, ok = pm.GetPipeline("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent pipeline")
	}
}

func TestCancelPipeline(t *testing.T) {
	pm := newTestPoolManager(t)
	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)

	if err := pm.CancelPipeline(pipeID); err != nil {
		t.Fatal(err)
	}

	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineFailed {
		t.Errorf("status = %q, want failed", p.Status)
	}

	tasks := pm.PipelineStageTasks(pipeID, 0)
	for _, task := range tasks {
		if task.Status != TaskCanceled {
			t.Errorf("task %q status = %q, want canceled", task.ID, task.Status)
		}
	}
}

// --- PipelineExecutor tests ---

func TestFanOut_WaitsForAll(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	spawnIdleWorker(pm, "w-2")
	spawnIdleWorker(pm, "w-3")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "fan-out test",
		Stages: []Stage{
			{
				Name: "impl", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "a", PromptTmpl: "task a: {{.Goal}}"},
					{ID: "b", PromptTmpl: "task b: {{.Goal}}"},
					{ID: "c", PromptTmpl: "task c: {{.Goal}}"},
				},
			},
			{
				Name: "review", Role: "reviewer", Fan: FanIn, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "review", PromptTmpl: "review: {{.PriorContext}}"},
				},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	if len(s0Tasks) != 3 {
		t.Fatalf("stage 0 tasks = %d, want 3", len(s0Tasks))
	}

	// Dispatch and complete first two tasks.
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{TaskID: s0Tasks[0].ID, ContextForNext: "a done"})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	pm.DispatchTask(s0Tasks[1].ID, "w-2")
	completeTaskDirect(pm, "w-2", s0Tasks[1].ID, &TaskHandover{TaskID: s0Tasks[1].ID, ContextForNext: "b done"})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[1].ID, TaskCompleted)

	// Stage 1 should NOT be enqueued yet (third task still pending).
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 0 {
		t.Errorf("stage 1 should not be enqueued yet, got %d tasks", len(s1Tasks))
	}

	// Complete the third task.
	pm.DispatchTask(s0Tasks[2].ID, "w-3")
	completeTaskDirect(pm, "w-3", s0Tasks[2].ID, &TaskHandover{TaskID: s0Tasks[2].ID, ContextForNext: "c done"})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[2].ID, TaskCompleted)

	// Now stage 1 should be enqueued.
	s1Tasks = pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1", len(s1Tasks))
	}
}

func TestFanIn_AggregatesHandovers(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	spawnIdleWorker(pm, "w-2")
	spawnIdleWorker(pm, "w-3")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "fan-in test",
		Stages: []Stage{
			{
				Name: "impl", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "a", PromptTmpl: "{{.Goal}}"},
					{ID: "b", PromptTmpl: "{{.Goal}}"},
					{ID: "c", PromptTmpl: "{{.Goal}}"},
				},
			},
			{
				Name: "aggregate", Role: "planner", Fan: FanIn, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "agg", PromptTmpl: "aggregate: {{.PriorContext}}"},
				},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)

	// Dispatch and complete all three.
	for i, tid := range []string{s0Tasks[0].ID, s0Tasks[1].ID, s0Tasks[2].ID} {
		wid := []string{"w-1", "w-2", "w-3"}[i]
		pm.DispatchTask(tid, wid)
		completeTaskDirect(pm, wid, tid, &TaskHandover{
			TaskID:         tid,
			ContextForNext: tid + " output",
		})
		drainNotify(pm)
		executor.OnTaskEvent(tid, TaskCompleted)
	}

	// Stage 1 should be enqueued with aggregated context.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1", len(s1Tasks))
	}
	// The prompt should contain handover context from all 3 tasks.
	prompt := s1Tasks[0].Prompt
	for _, tid := range []string{s0Tasks[0].ID, s0Tasks[1].ID, s0Tasks[2].ID} {
		if !strings.Contains(prompt, tid+" output") {
			t.Errorf("stage 1 prompt missing handover from %q", tid)
		}
	}
}

func TestStreaming_AdvancesImmediately(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	spawnIdleWorker(pm, "w-2")
	spawnIdleWorker(pm, "w-3")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "streaming test",
		Stages: []Stage{
			{
				Name: "impl", Role: "impl", Fan: Streaming, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "a", PromptTmpl: "{{.Goal}}"},
					{ID: "b", PromptTmpl: "{{.Goal}}"},
					{ID: "c", PromptTmpl: "{{.Goal}}"},
				},
			},
			{
				Name: "review", Role: "reviewer", Fan: Streaming, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "r-a", PromptTmpl: "review: {{.PriorContext}}"},
					{ID: "r-b", PromptTmpl: "review: {{.PriorContext}}"},
					{ID: "r-c", PromptTmpl: "review: {{.PriorContext}}"},
				},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)

	// Complete only the first task.
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{
		TaskID:         s0Tasks[0].ID,
		ContextForNext: "a output",
	})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Stage 1 should have 1 task enqueued (streaming — don't wait for siblings).
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1 (streaming should advance immediately)", len(s1Tasks))
	}

	// Other tasks still running.
	if s0t1, _ := pm.Task(s0Tasks[1].ID); s0t1.Status != TaskQueued {
		t.Errorf("s0 task 1 status = %q, want queued", s0t1.Status)
	}
}

func TestStreaming_FanInNextStage_WaitsForAll(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	spawnIdleWorker(pm, "w-2")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "streaming fan-in test",
		Stages: []Stage{
			{
				Name: "impl", Role: "impl", Fan: Streaming, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "a", PromptTmpl: "{{.Goal}}"},
					{ID: "b", PromptTmpl: "{{.Goal}}"},
				},
			},
			{
				Name: "aggregate", Role: "planner", Fan: FanIn, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "agg", PromptTmpl: "aggregate: {{.PriorContext}}"},
				},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)

	// Complete first task.
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Next stage has 1 task (fan-in), so streaming should wait.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 0 {
		t.Errorf("stage 1 should not be enqueued yet (fan-in waits for all), got %d", len(s1Tasks))
	}

	// Complete second task.
	pm.DispatchTask(s0Tasks[1].ID, "w-2")
	completeTaskDirect(pm, "w-2", s0Tasks[1].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[1].ID, TaskCompleted)

	s1Tasks = pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1 after all siblings done", len(s1Tasks))
	}
}

func TestAutoAdvanceFalse_Blocks(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := twoStagePipeline(FanOut, FanIn, false) // AutoAdvance=false on stage 1
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Pipeline should be blocked.
	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineBlocked {
		t.Errorf("status = %q, want blocked", p.Status)
	}

	// Stage 1 should NOT be enqueued.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 0 {
		t.Errorf("stage 1 should not be enqueued when AutoAdvance=false, got %d", len(s1Tasks))
	}
}

func TestAdvanceStage_Manual(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := twoStagePipeline(FanOut, FanIn, false) // AutoAdvance=false on stage 1
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{TaskID: s0Tasks[0].ID, ContextForNext: "stage 0 output"})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Pipeline is blocked.
	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineBlocked {
		t.Fatalf("status = %q, want blocked", p.Status)
	}

	// Manually advance.
	if err := executor.AdvanceStage(pipeID); err != nil {
		t.Fatal(err)
	}

	// Pipeline should be running again.
	p, _ = pm.GetPipeline(pipeID)
	if p.Status != PipelineRunning {
		t.Errorf("status = %q, want running after manual advance", p.Status)
	}

	// Stage 1 should be enqueued.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1", len(s1Tasks))
	}
}

func TestPipelineFailure(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	pm.FailTask("w-1", s0Tasks[0].ID, "build error")
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskFailed)

	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineFailed {
		t.Errorf("status = %q, want failed", p.Status)
	}
}

func TestPipelineEscalation(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")

	// Simulate escalation by directly setting status (normally via review cycle).
	pm.mu.Lock()
	pm.tasks[s0Tasks[0].ID].Status = TaskEscalated
	pm.mu.Unlock()

	executor.OnTaskEvent(s0Tasks[0].ID, TaskEscalated)

	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineBlocked {
		t.Errorf("status = %q, want blocked on escalation", p.Status)
	}
}

func TestFullPipeline_Lifecycle(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "full lifecycle",
		Stages: []Stage{
			{
				Name: "s0", Role: "planner", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "s0-t", PromptTmpl: "{{.Goal}}"},
				},
			},
			{
				Name: "s1", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "s1-t", PromptTmpl: "{{.PriorContext}}"},
				},
			},
			{
				Name: "s2", Role: "reviewer", Fan: FanIn, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "s2-t", PromptTmpl: "{{.PriorContext}}"},
				},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	// Stage 0.
	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{TaskID: s0Tasks[0].ID, ContextForNext: "designed"})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Stage 1.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1", len(s1Tasks))
	}
	pm.DispatchTask(s1Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s1Tasks[0].ID, &TaskHandover{TaskID: s1Tasks[0].ID, ContextForNext: "implemented"})
	drainNotify(pm)
	executor.OnTaskEvent(s1Tasks[0].ID, TaskCompleted)

	// Stage 2.
	s2Tasks := pm.PipelineStageTasks(pipeID, 2)
	if len(s2Tasks) != 1 {
		t.Fatalf("stage 2 tasks = %d, want 1", len(s2Tasks))
	}
	pm.DispatchTask(s2Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s2Tasks[0].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s2Tasks[0].ID, TaskCompleted)

	// Pipeline should be completed.
	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineCompleted {
		t.Errorf("status = %q, want completed", p.Status)
	}
}

func TestNonPipelineTask_Ignored(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pm.EnqueueTask(TaskSpec{ID: "standalone", Prompt: "do something"})
	pm.DispatchTask("standalone", "w-1")
	completeTestTask(pm, "w-1", "standalone", TaskResult{Summary: "done"}, nil)
	drainNotify(pm)

	// Should not panic or error.
	executor.OnTaskEvent("standalone", TaskCompleted)
	executor.OnTaskEvent("nonexistent", TaskCompleted)
}

func TestPipelineCompletion_SetsCompletedAt(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "completedAt test",
		Stages: []Stage{{
			Name: "only", Role: "impl", Fan: FanOut, AutoAdvance: true,
			Tasks: []StageTask{{ID: "t", PromptTmpl: "work"}},
		}},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	p, _ := pm.GetPipeline(pipeID)
	if p.CompletedAt == nil {
		t.Error("CompletedAt should be set on completed pipeline")
	}
}

func TestPipelineCompletion_Notification(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "notify test",
		Stages: []Stage{{
			Name: "only", Role: "impl", Fan: FanOut, AutoAdvance: true,
			Tasks: []StageTask{{ID: "t", PromptTmpl: "work"}},
		}},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm) // clear creation notification

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm) // clear task_completed

	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	notifications := drainNotify(pm)
	found := false
	for _, n := range notifications {
		if n.Type == "pipeline_completed" && n.ID == pipeID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected pipeline_completed notification, got: %v", notifications)
	}
}

func TestPipelineFailure_CancelsRemainingTasks(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	spawnIdleWorker(pm, "w-2")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "fail cancel test",
		Stages: []Stage{
			{
				Name: "s0", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{
					{ID: "a", PromptTmpl: "task a"},
					{ID: "b", PromptTmpl: "task b"},
					{ID: "c", PromptTmpl: "task c"},
				},
			},
			{
				Name: "s1", Role: "reviewer", Fan: FanIn, AutoAdvance: true,
				Tasks: []StageTask{{ID: "d", PromptTmpl: "review"}},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)

	// Fail one task — remaining queued tasks should be canceled.
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	pm.FailTask("w-1", s0Tasks[0].ID, "build error")
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskFailed)

	// The other stage 0 tasks should be canceled.
	for _, tid := range []string{s0Tasks[1].ID, s0Tasks[2].ID} {
		task, ok := pm.Task(tid)
		if !ok {
			t.Fatalf("task %s not found", tid)
		}
		if task.Status != TaskCanceled {
			t.Errorf("task %s status = %q, want canceled", tid, task.Status)
		}
	}

	// Stage 1 should never be enqueued.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 0 {
		t.Errorf("stage 1 has %d tasks, want 0 (pipeline failed)", len(s1Tasks))
	}
}

func TestPipelineFailure_Notification(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	pm.FailTask("w-1", s0Tasks[0].ID, "boom")
	drainNotify(pm) // clear task_failed

	executor.OnTaskEvent(s0Tasks[0].ID, TaskFailed)

	notifications := drainNotify(pm)
	found := false
	for _, n := range notifications {
		if n.Type == "pipeline_failed" && n.ID == pipeID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected pipeline_failed notification, got: %v", notifications)
	}
}

func TestHandoverContext_BasicFlow(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "handover test",
		Stages: []Stage{
			{
				Name: "research", Role: "planner", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "r", PromptTmpl: "research {{.Goal}}"}},
			},
			{
				Name: "implement", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "i", PromptTmpl: "implement with context: {{.PriorContext}}"}},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{
		TaskID:         s0Tasks[0].ID,
		Summary:        "found API structure",
		ContextForNext: "API has 3 endpoints: GET /users, POST /users, DELETE /users",
	})
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1", len(s1Tasks))
	}

	prompt := s1Tasks[0].Prompt
	if !strings.Contains(prompt, "API has 3 endpoints") {
		t.Errorf("prompt missing handover context: %q", prompt)
	}
	if strings.Contains(prompt, "{{.PriorContext}}") {
		t.Errorf("prompt still has template placeholder: %q", prompt)
	}
}

func TestGoalTemplate_ExpandedInAllStages(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "deploy service X",
		Stages: []Stage{
			{
				Name: "s0", Role: "planner", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "a", PromptTmpl: "plan {{.Goal}}"}},
			},
			{
				Name: "s1", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "b", PromptTmpl: "execute {{.Goal}}"}},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	// Check stage 0 prompt.
	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	if !strings.Contains(s0Tasks[0].Prompt, "deploy service X") {
		t.Errorf("s0 prompt missing goal: %q", s0Tasks[0].Prompt)
	}

	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Check stage 1 prompt.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1", len(s1Tasks))
	}
	if !strings.Contains(s1Tasks[0].Prompt, "deploy service X") {
		t.Errorf("s1 prompt missing goal: %q", s1Tasks[0].Prompt)
	}
}

func TestTaskAccepted_AdvancesStage(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)

	// Simulate review acceptance by directly setting status.
	pm.mu.Lock()
	pm.tasks[s0Tasks[0].ID].Status = TaskAccepted
	pm.mu.Unlock()

	executor.OnTaskEvent(s0Tasks[0].ID, TaskAccepted)

	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d, want 1 (accepted should advance)", len(s1Tasks))
	}
}

func TestOnTaskEvent_IgnoresCompletedPipeline(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")
	executor := NewPipelineExecutor(pm)

	pipe := Pipeline{
		Goal: "already done",
		Stages: []Stage{{
			Name: "s0", Role: "impl", Fan: FanOut, AutoAdvance: true,
			Tasks: []StageTask{{ID: "t", PromptTmpl: "work"}},
		}},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	// Pipeline is now completed. Calling again should be a no-op.
	executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)

	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineCompleted {
		t.Errorf("status = %q, want completed (should remain completed)", p.Status)
	}
}

func TestScanStuckPipelines_HandoversPassedForStuckStage(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")

	pipe := Pipeline{
		Goal: "handover stuck",
		Stages: []Stage{
			{
				Name: "s0", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "a", PromptTmpl: "research"}},
			},
			{
				Name: "s1", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "b", PromptTmpl: "impl: {{.PriorContext}}"}},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{
		TaskID:         s0Tasks[0].ID,
		ContextForNext: "found 3 modules",
	})
	drainNotify(pm)
	// No OnTaskEvent — simulate crash.

	executor := NewPipelineExecutor(pm)
	executor.ScanStuckPipelines()

	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d after scan, want 1", len(s1Tasks))
	}
	if !strings.Contains(s1Tasks[0].Prompt, "found 3 modules") {
		t.Errorf("handover not passed through stuck scan: %q", s1Tasks[0].Prompt)
	}
}

func TestAggregateHandovers(t *testing.T) {
	handovers := []*TaskHandover{
		{TaskID: "t-1", ContextForNext: "output 1"},
		{TaskID: "t-2", ContextForNext: ""},
		{TaskID: "t-3", ContextForNext: "output 3"},
	}
	result := aggregateHandovers(handovers)
	if !strings.Contains(result, "output 1") {
		t.Error("missing output 1")
	}
	if strings.Contains(result, "t-2") {
		t.Error("should skip empty context")
	}
	if !strings.Contains(result, "output 3") {
		t.Error("missing output 3")
	}
}

func TestAggregateHandovers_Empty(t *testing.T) {
	result := aggregateHandovers(nil)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestAggregateHandovers_Truncation(t *testing.T) {
	// Create handover exceeding 4000 chars.
	longCtx := strings.Repeat("x", 3000)
	handovers := []*TaskHandover{
		{TaskID: "t-1", ContextForNext: longCtx},
		{TaskID: "t-2", ContextForNext: longCtx},
	}
	result := aggregateHandovers(handovers)
	if !strings.HasSuffix(result, "[context truncated]") {
		t.Error("expected truncation marker")
	}
}

// --- ScanStuckPipelines tests ---

func TestScanStuckPipelines_AdvancesStuckStage(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	// Complete stage 0 task directly (without executor — simulating crash).
	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, &TaskHandover{
		TaskID:         s0Tasks[0].ID,
		ContextForNext: "from stage 0",
	})
	drainNotify(pm)

	// Stage 1 was never enqueued (crash lost the notification).
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 0 {
		t.Fatalf("stage 1 should be empty before scan, got %d", len(s1Tasks))
	}

	// Scan should detect and advance.
	executor := NewPipelineExecutor(pm)
	executor.ScanStuckPipelines()

	s1Tasks = pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 1 {
		t.Fatalf("stage 1 tasks = %d after scan, want 1", len(s1Tasks))
	}
}

func TestScanStuckPipelines_CompletesFullyDone(t *testing.T) {
	pm := newTestPoolManager(t)
	spawnIdleWorker(pm, "w-1")

	pipe := Pipeline{
		Goal: "one stage",
		Stages: []Stage{
			{
				Name: "only", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "t0", PromptTmpl: "{{.Goal}}"}},
			},
		},
	}
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	s0Tasks := pm.PipelineStageTasks(pipeID, 0)
	pm.DispatchTask(s0Tasks[0].ID, "w-1")
	completeTaskDirect(pm, "w-1", s0Tasks[0].ID, nil)
	drainNotify(pm)

	// Pipeline not marked complete (crash lost the event).
	p, _ := pm.GetPipeline(pipeID)
	if p.Status != PipelineRunning {
		t.Fatalf("expected running (simulating lost completion), got %q", p.Status)
	}

	executor := NewPipelineExecutor(pm)
	executor.ScanStuckPipelines()

	p, _ = pm.GetPipeline(pipeID)
	if p.Status != PipelineCompleted {
		t.Errorf("status = %q after scan, want completed", p.Status)
	}
}

func TestScanStuckPipelines_NoStuck(t *testing.T) {
	pm := newTestPoolManager(t)

	pipe := twoStagePipeline(FanOut, FanIn, true)
	pipeID, _ := pm.SubmitPipeline(pipe)
	drainNotify(pm)

	// Stage 0 task is still queued — not stuck.
	executor := NewPipelineExecutor(pm)
	executor.ScanStuckPipelines()

	// Stage 1 should not be enqueued.
	s1Tasks := pm.PipelineStageTasks(pipeID, 1)
	if len(s1Tasks) != 0 {
		t.Errorf("stage 1 should not be enqueued, got %d", len(s1Tasks))
	}
}

// --- Pipeline WAL replay tests ---

// newReplayPM creates a fresh PoolManager that replays the given WAL file.
func newReplayPM(t *testing.T, walPath string) *PoolManager {
	t.Helper()
	wal, err := OpenWAL(walPath)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	t.Cleanup(func() { wal.Close() })

	dir := filepath.Dir(walPath)
	pm := NewPoolManager(PoolConfig{MaxWorkers: 10, StateDir: dir}, wal, nil)

	// Replay WAL into PM.
	err = wal.Replay(func(e Event) error {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		return Apply(pm, e)
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return pm
}

func TestPipelineReplay_RunningState(t *testing.T) {
	// Phase 1: create pipeline and record WAL events.
	dir := t.TempDir()
	walPath := filepath.Join(dir, "replay.wal")

	var pipeID string
	func() {
		wal, err := OpenWAL(walPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer wal.Close()

		pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
		spawnIdleWorker(pm, "w-1")
		pipeID, _ = pm.SubmitPipeline(Pipeline{
			Goal: "replay test",
			Stages: []Stage{
				{
					Name: "s0", Role: "impl", Fan: FanOut, AutoAdvance: true,
					Tasks: []StageTask{{ID: "t0", PromptTmpl: "{{.Goal}}"}},
				},
				{
					Name: "s1", Role: "reviewer", Fan: FanIn, AutoAdvance: true,
					Tasks: []StageTask{{ID: "t1", PromptTmpl: "{{.PriorContext}}"}},
				},
			},
		})
		drainNotify(pm)
	}()

	// Phase 2: replay WAL into a fresh PM.
	pm2 := newReplayPM(t, walPath)

	// Verify pipeline is reconstructed as running.
	p, ok := pm2.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found after replay")
	}
	if p.Status != PipelineRunning {
		t.Errorf("status = %q, want running", p.Status)
	}

	// Verify worker and stage 0 tasks are reconstructed.
	w, ok := pm2.Worker("w-1")
	if !ok {
		t.Fatal("worker w-1 not found after replay")
	}
	if w.Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle", w.Status)
	}

	s0Tasks := pm2.PipelineStageTasks(pipeID, 0)
	if len(s0Tasks) != 1 {
		t.Fatalf("stage 0 tasks = %d, want 1", len(s0Tasks))
	}
	if s0Tasks[0].Status != TaskQueued {
		t.Errorf("task status = %q, want queued", s0Tasks[0].Status)
	}
}

func TestPipelineReplay_BlockedState(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "blocked.wal")

	var pipeID string
	func() {
		wal, err := OpenWAL(walPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer wal.Close()

		pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
		spawnIdleWorker(pm, "w-1")

		pipe := twoStagePipeline(FanOut, FanIn, false)
		pipeID, _ = pm.SubmitPipeline(pipe)
		drainNotify(pm)

		s0Tasks := pm.PipelineStageTasks(pipeID, 0)
		pm.DispatchTask(s0Tasks[0].ID, "w-1")
		completeTestTask(pm, "w-1", s0Tasks[0].ID, TaskResult{Summary: "done"}, nil)
		drainNotify(pm)

		executor := NewPipelineExecutor(pm)
		executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)
		drainNotify(pm)
	}()

	pm2 := newReplayPM(t, walPath)

	p, ok := pm2.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found after replay")
	}
	if p.Status != PipelineBlocked {
		t.Errorf("status = %q, want blocked", p.Status)
	}
}

func TestPipelineReplay_CompletedState(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "completed.wal")

	var pipeID string
	func() {
		wal, err := OpenWAL(walPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer wal.Close()

		pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
		spawnIdleWorker(pm, "w-1")

		pipeID, _ = pm.SubmitPipeline(Pipeline{
			Goal: "complete test",
			Stages: []Stage{{
				Name: "s0", Role: "impl", Fan: FanOut, AutoAdvance: true,
				Tasks: []StageTask{{ID: "t0", PromptTmpl: "work"}},
			}},
		})
		drainNotify(pm)

		s0Tasks := pm.PipelineStageTasks(pipeID, 0)
		pm.DispatchTask(s0Tasks[0].ID, "w-1")
		completeTestTask(pm, "w-1", s0Tasks[0].ID, TaskResult{Summary: "done"}, nil)
		drainNotify(pm)

		executor := NewPipelineExecutor(pm)
		executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)
		drainNotify(pm)
	}()

	pm2 := newReplayPM(t, walPath)

	p, ok := pm2.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found after replay")
	}
	if p.Status != PipelineCompleted {
		t.Errorf("status = %q, want completed", p.Status)
	}
	if p.CompletedAt == nil {
		t.Error("CompletedAt should be set after replay")
	}
}

func TestPipelineReplay_FailedState(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "failed.wal")

	var pipeID string
	func() {
		wal, err := OpenWAL(walPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer wal.Close()

		pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
		spawnIdleWorker(pm, "w-1")

		pipeID, _ = pm.SubmitPipeline(twoStagePipeline(FanOut, FanIn, true))
		drainNotify(pm)

		s0Tasks := pm.PipelineStageTasks(pipeID, 0)
		pm.DispatchTask(s0Tasks[0].ID, "w-1")
		pm.FailTask("w-1", s0Tasks[0].ID, "build error")
		drainNotify(pm)

		executor := NewPipelineExecutor(pm)
		executor.OnTaskEvent(s0Tasks[0].ID, TaskFailed)
		drainNotify(pm)
	}()

	pm2 := newReplayPM(t, walPath)

	p, ok := pm2.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found after replay")
	}
	if p.Status != PipelineFailed {
		t.Errorf("status = %q, want failed", p.Status)
	}
}

func TestPipelineReplay_UnblockedState(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "unblocked.wal")

	var pipeID string
	func() {
		wal, err := OpenWAL(walPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer wal.Close()

		pm := NewPoolManager(PoolConfig{MaxWorkers: 5, StateDir: dir}, wal, nil)
		spawnIdleWorker(pm, "w-1")

		pipe := twoStagePipeline(FanOut, FanIn, false)
		pipeID, _ = pm.SubmitPipeline(pipe)
		drainNotify(pm)

		s0Tasks := pm.PipelineStageTasks(pipeID, 0)
		pm.DispatchTask(s0Tasks[0].ID, "w-1")
		completeTestTask(pm, "w-1", s0Tasks[0].ID, TaskResult{Summary: "done"}, nil)
		drainNotify(pm)

		executor := NewPipelineExecutor(pm)
		executor.OnTaskEvent(s0Tasks[0].ID, TaskCompleted)
		drainNotify(pm)

		executor.AdvanceStage(pipeID)
		drainNotify(pm)
	}()

	pm2 := newReplayPM(t, walPath)

	p, ok := pm2.GetPipeline(pipeID)
	if !ok {
		t.Fatal("pipeline not found after replay")
	}
	if p.Status != PipelineRunning {
		t.Errorf("status = %q, want running (unblocked)", p.Status)
	}
}
