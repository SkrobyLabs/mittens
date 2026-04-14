package pool

import (
	"testing"
	"time"
)

func newTestPM() *PoolManager {
	return &PoolManager{
		workers:   make(map[string]*Worker),
		tasks:     make(map[string]*Task),
		queue:     NewPriorityQueue(),
		pipes:     make(map[string]*Pipeline),
		questions: make(map[string]*Question),
		notify:    make(chan Notification, 100),
	}
}

func TestApplyWorkerSpawned(t *testing.T) {
	pm := newTestPM()
	e := Event{
		Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1",
		Data: marshalData(WorkerSpawnedData{ContainerID: "abc", Provider: "openai", Model: "gpt-5.4", Adapter: "openai-codex", Role: "impl"}),
	}
	if err := Apply(pm, e); err != nil {
		t.Fatal(err)
	}
	w := pm.workers["w-1"]
	if w == nil {
		t.Fatal("worker not created")
	}
	if w.Status != WorkerSpawning {
		t.Errorf("status = %q, want spawning", w.Status)
	}
	if w.ContainerID != "abc" {
		t.Errorf("containerId = %q, want abc", w.ContainerID)
	}
	if w.Role != "impl" {
		t.Errorf("role = %q, want impl", w.Role)
	}
	if w.Provider != "openai" || w.Model != "gpt-5.4" || w.Adapter != "openai-codex" {
		t.Errorf("worker route = %q/%q/%q, want openai/gpt-5.4/openai-codex", w.Provider, w.Model, w.Adapter)
	}
}

func TestApplyWorkerReady(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	if pm.workers["w-1"].Status != WorkerIdle {
		t.Errorf("status = %q, want idle", pm.workers["w-1"].Status)
	}
}

func TestApplyWorkerDead(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1", Data: marshalData(WorkerSpawnedData{Token: "tok-1"})})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerDead, WorkerID: "w-1"})
	if pm.workers["w-1"].Status != WorkerDead {
		t.Errorf("status = %q, want dead", pm.workers["w-1"].Status)
	}
	if pm.workers["w-1"].Token != "" {
		t.Errorf("token = %q, want empty", pm.workers["w-1"].Token)
	}
}

func TestApplyWorkerQuestionDoesNotReviveDeadWorker(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerDead, WorkerID: "w-1"})
	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventWorkerQuestion, WorkerID: "w-1",
		Data: marshalData(WorkerQuestionData{QuestionID: "q-1", Question: "which way?", Blocking: true}),
	})
	if pm.workers["w-1"].Status != WorkerDead {
		t.Errorf("status = %q, want dead", pm.workers["w-1"].Status)
	}
}

func TestApplyTaskLifecycle(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})

	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1",
		Data: marshalData(TaskCreatedData{Prompt: "do it", Priority: 5}),
	})
	if pm.tasks["t-1"].Status != TaskQueued {
		t.Fatalf("task status = %q, want queued", pm.tasks["t-1"].Status)
	}

	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1"})
	if pm.tasks["t-1"].Status != TaskDispatched {
		t.Errorf("task status = %q, want dispatched", pm.tasks["t-1"].Status)
	}
	if pm.workers["w-1"].Status != WorkerWorking {
		t.Errorf("worker status = %q, want working", pm.workers["w-1"].Status)
	}
	if pm.workers["w-1"].CurrentTaskID != "t-1" {
		t.Errorf("currentTaskId = %q, want t-1", pm.workers["w-1"].CurrentTaskID)
	}

	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventTaskCompleted, TaskID: "t-1", WorkerID: "w-1",
		Data: marshalData(TaskCompletedData{Summary: "done"}),
	})
	if pm.tasks["t-1"].Status != TaskCompleted {
		t.Errorf("task status = %q, want completed", pm.tasks["t-1"].Status)
	}
	if pm.workers["w-1"].Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle", pm.workers["w-1"].Status)
	}
}

func TestApplyTaskFailed(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1", Data: marshalData(TaskCreatedData{Prompt: "x"})})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1"})
	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventTaskFailed, TaskID: "t-1", WorkerID: "w-1",
		Data: marshalData(TaskFailedData{Error: "oops"}),
	})
	if pm.tasks["t-1"].Status != TaskFailed {
		t.Errorf("status = %q, want failed", pm.tasks["t-1"].Status)
	}
	if pm.tasks["t-1"].Result.Error != "oops" {
		t.Errorf("error = %q, want oops", pm.tasks["t-1"].Result.Error)
	}
	if pm.workers["w-1"].Status != WorkerIdle {
		t.Errorf("worker status = %q, want idle", pm.workers["w-1"].Status)
	}
}

func TestApplyTaskCompletedDoesNotReviveDeadWorker(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1", Data: marshalData(TaskCreatedData{Prompt: "x"})})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerDead, WorkerID: "w-1"})
	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventTaskCompleted, TaskID: "t-1", WorkerID: "w-1",
		Data: marshalData(TaskCompletedData{Summary: "done"}),
	})
	if pm.workers["w-1"].Status != WorkerDead {
		t.Errorf("worker status = %q, want dead", pm.workers["w-1"].Status)
	}
}

func TestApplyTaskCanceledReleasesAssignedWorker(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1",
		Data: marshalData(TaskCreatedData{Prompt: "x"}),
	})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1"})

	w := pm.workers["w-1"]
	w.Status = WorkerBlocked
	w.CurrentActivity = &WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "Reading cancellation context",
	}
	w.CurrentTool = "Read"

	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskCanceled, TaskID: "t-1"})

	if pm.tasks["t-1"].Status != TaskCanceled {
		t.Fatalf("task status = %q, want canceled", pm.tasks["t-1"].Status)
	}
	if w.Status != WorkerIdle {
		t.Fatalf("worker status = %q, want idle", w.Status)
	}
	if w.CurrentTaskID != "" {
		t.Fatalf("worker currentTaskID = %q, want empty", w.CurrentTaskID)
	}
	if w.CurrentActivity != nil {
		t.Fatalf("worker currentActivity = %+v, want nil", w.CurrentActivity)
	}
	if w.CurrentTool != "" {
		t.Fatalf("worker currentTool = %q, want empty", w.CurrentTool)
	}
}

func TestApplyQuestionLifecycle(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1", Data: marshalData(TaskCreatedData{Prompt: "x"})})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskDispatched, TaskID: "t-1", WorkerID: "w-1"})

	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventWorkerQuestion, WorkerID: "w-1", TaskID: "t-1",
		Data: marshalData(WorkerQuestionData{QuestionID: "q-1", Question: "which DB?", Blocking: true}),
	})
	if pm.workers["w-1"].Status != WorkerBlocked {
		t.Errorf("worker status = %q, want blocked", pm.workers["w-1"].Status)
	}
	q := pm.questions["q-1"]
	if q == nil {
		t.Fatal("question not created")
	}
	if q.Answered {
		t.Error("question should not be answered yet")
	}

	Apply(pm, Event{
		Timestamp: time.Now(), Type: EventQuestionAnswered,
		Data: marshalData(QuestionAnsweredData{QuestionID: "q-1", Answer: "postgres", AnsweredBy: "leader"}),
	})
	if !pm.questions["q-1"].Answered {
		t.Error("question should be answered")
	}
	if pm.workers["w-1"].Status != WorkerWorking {
		t.Errorf("worker status = %q, want working", pm.workers["w-1"].Status)
	}
}

func TestApplyTaskDeletedRemovesTaskAndQuestions(t *testing.T) {
	pm := newTestPM()
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerSpawned, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventWorkerReady, WorkerID: "w-1"})
	Apply(pm, Event{Timestamp: time.Now(), Type: EventTaskCreated, TaskID: "t-1", Data: marshalData(TaskCreatedData{Prompt: "x", Priority: 1})})
	pm.queue.Push("t-1", 1, nil)
	Apply(pm, Event{
		Timestamp: time.Now(),
		Type:      EventWorkerQuestion,
		WorkerID:  "w-1",
		TaskID:    "t-1",
		Data:      marshalData(WorkerQuestionData{QuestionID: "q-1", Question: "which DB?", Blocking: true}),
	})

	if err := Apply(pm, Event{
		Timestamp: time.Now(),
		Type:      EventTaskDeleted,
		TaskID:    "t-1",
		Data:      marshalData(TaskDeletedData{QuestionIDs: []string{"q-1"}}),
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok := pm.tasks["t-1"]; ok {
		t.Fatal("task should be removed")
	}
	if _, ok := pm.questions["q-1"]; ok {
		t.Fatal("question should be removed")
	}
	if _, ok := pm.queue.Pop(func(string) bool { return true }); ok {
		t.Fatal("queue entry should be removed")
	}
	if pm.workers["w-1"].Status != WorkerIdle {
		t.Fatalf("worker status = %q, want idle", pm.workers["w-1"].Status)
	}
}

func TestApplyUnknownEvent(t *testing.T) {
	pm := newTestPM()
	err := Apply(pm, Event{Type: "bogus"})
	if err == nil {
		t.Error("expected error for unknown event type")
	}
}
