package pool

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// isTerminalStatus returns true if the task status represents a non-active state
// (i.e. the task is no longer being worked on by a worker).
func isTerminalStatus(status string) bool {
	switch status {
	case TaskCompleted, TaskFailed, TaskCanceled, TaskAccepted, TaskRejected, TaskEscalated:
		return true
	}
	return false
}

// PoolManager is the single owner of all mutable pool state.
// Every state mutation follows: lock -> validate -> WAL append -> update in-memory -> unlock.
type PoolManager struct {
	mu          sync.RWMutex
	workers     map[string]*Worker
	tasks       map[string]*Task
	queue       *PriorityQueue
	pipes       map[string]*Pipeline
	wal         *WAL
	questions   map[string]*Question
	qSeq        int
	taskSeq     int
	hostAPI     HostAPI
	notify      chan Notification
	taskWaiters map[string][]chan Notification // taskID -> per-waiter channels
	cfg         PoolConfig
}

func cloneTimePtr(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	cp := *ts
	return &cp
}

func cloneWorkerActivity(activity *WorkerActivity) *WorkerActivity {
	if activity == nil {
		return nil
	}
	cp := *activity
	return &cp
}

func cloneTaskResult(result *TaskResult) *TaskResult {
	if result == nil {
		return nil
	}
	cp := *result
	if result.FilesChanged != nil {
		cp.FilesChanged = append([]string(nil), result.FilesChanged...)
	}
	return &cp
}

func cloneTaskHandover(handover *TaskHandover) *TaskHandover {
	if handover == nil {
		return nil
	}
	cp := *handover
	if handover.KeyDecisions != nil {
		cp.KeyDecisions = append([]string(nil), handover.KeyDecisions...)
	}
	if handover.FilesChanged != nil {
		cp.FilesChanged = append([]FileChange(nil), handover.FilesChanged...)
	}
	if handover.OpenQuestions != nil {
		cp.OpenQuestions = append([]string(nil), handover.OpenQuestions...)
	}
	return &cp
}

func currentToolFromActivity(activity *WorkerActivity) string {
	if activity == nil || activity.Kind != "tool" || activity.Phase != "started" {
		return ""
	}
	return activity.Name
}

func normalizeWorkerHeartbeat(activity *WorkerActivity, legacyCurrentTool string) (*WorkerActivity, string) {
	if activity != nil {
		normalized := cloneWorkerActivity(activity)
		if normalized.Kind == "" && normalized.Phase == "" && normalized.Name == "" && normalized.Summary == "" {
			return nil, ""
		}
		return normalized, currentToolFromActivity(normalized)
	}
	if legacyCurrentTool == "" {
		return nil, ""
	}
	return &WorkerActivity{
		Kind:  "tool",
		Phase: "started",
		Name:  legacyCurrentTool,
	}, legacyCurrentTool
}

func rejectDeadWorkerActivity(action string, w *Worker) error {
	if w != nil && w.Status == WorkerDead {
		return fmt.Errorf("%s: worker %q is dead", action, w.ID)
	}
	return nil
}

// NewPoolManager creates a fresh PoolManager.
func NewPoolManager(cfg PoolConfig, wal *WAL, hostAPI HostAPI) *PoolManager {
	pm := &PoolManager{
		workers:     make(map[string]*Worker),
		tasks:       make(map[string]*Task),
		queue:       NewPriorityQueue(),
		pipes:       make(map[string]*Pipeline),
		wal:         wal,
		questions:   make(map[string]*Question),
		hostAPI:     hostAPI,
		notify:      make(chan Notification, 100),
		taskWaiters: make(map[string][]chan Notification),
		cfg:         cfg,
	}
	return pm
}

// RecoverPoolManager replays the WAL to rebuild in-memory state.
func RecoverPoolManager(cfg PoolConfig, wal *WAL, hostAPI HostAPI) (*PoolManager, error) {
	pm := NewPoolManager(cfg, wal, hostAPI)

	err := wal.Replay(func(e Event) error {
		return Apply(pm, e)
	})
	if err != nil {
		return nil, fmt.Errorf("recover pool: %w", err)
	}

	// Re-enqueue tasks that are still queued (replay created them but didn't push to queue).
	for _, t := range pm.tasks {
		if t.Status == TaskQueued {
			pm.queue.Push(t.ID, t.Priority, t.DependsOn)
		}
	}

	// Recover taskSeq from replayed tasks to prevent ID collisions.
	for _, t := range pm.tasks {
		var n int
		if _, err := fmt.Sscanf(t.ID, "t-%d", &n); err == nil && n > pm.taskSeq {
			pm.taskSeq = n
		}
	}

	// Recover qSeq from replayed questions to prevent ID collisions.
	for _, q := range pm.questions {
		var n int
		if _, err := fmt.Sscanf(q.ID, "q-%d", &n); err == nil && n > pm.qSeq {
			pm.qSeq = n
		}
	}

	return pm, nil
}

// --- Worker lifecycle ---

// SpawnWorker creates a new worker and optionally calls HostAPI.SpawnWorker.
func (pm *PoolManager) SpawnWorker(spec WorkerSpec) (*Worker, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.cfg.MaxWorkers > 0 && pm.aliveWorkersLocked() >= pm.cfg.MaxWorkers {
		return nil, fmt.Errorf("spawn worker: max workers (%d) reached", pm.cfg.MaxWorkers)
	}

	wid := spec.ID
	if wid == "" {
		wid = fmt.Sprintf("w-%d", len(pm.workers)+1)
	}
	if _, exists := pm.workers[wid]; exists {
		return nil, fmt.Errorf("spawn worker: %q already exists", wid)
	}

	// Propagate the resolved ID so the host broker uses the same ID
	// for container naming and env vars.
	spec.ID = wid

	// Generate a unique per-worker auth token.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("spawn worker: generate token: %w", err)
	}
	workerToken := hex.EncodeToString(tokenBytes)

	// Inject into environment so the container receives it.
	if spec.Environment == nil {
		spec.Environment = make(map[string]string)
	}
	spec.Environment["MITTENS_WORKER_TOKEN"] = workerToken

	var containerName, containerID string
	if pm.hostAPI != nil {
		var err error
		containerName, containerID, err = pm.hostAPI.SpawnWorker(context.Background(), spec)
		if err != nil {
			return nil, fmt.Errorf("spawn worker: %w", err)
		}
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerSpawned,
		WorkerID:  wid,
		Data:      marshalData(WorkerSpawnedData{ContainerID: containerID, Role: spec.Role, Token: workerToken}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		// Clean up orphaned container before returning.
		if pm.hostAPI != nil {
			_ = pm.hostAPI.KillWorker(context.Background(), wid)
		}
		return nil, fmt.Errorf("spawn worker wal: %w", err)
	}
	Apply(pm, e)

	w := pm.workers[wid]
	w.ContainerName = containerName
	return w, nil
}

// RegisterWorker transitions a spawning worker to idle.
func (pm *PoolManager) RegisterWorker(workerID, containerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	w := pm.workers[workerID]
	if w == nil {
		return fmt.Errorf("register worker: %q not found", workerID)
	}
	if w.Status != WorkerSpawning {
		return fmt.Errorf("register worker: %q is %q, expected spawning", workerID, w.Status)
	}

	if containerID != "" {
		w.ContainerID = containerID
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerReady,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("register worker wal: %w", err)
	}
	Apply(pm, e)
	return nil
}

// Heartbeat updates the worker's last heartbeat timestamp and current activity.
// Heartbeats are ephemeral and not WAL'd.
func (pm *PoolManager) Heartbeat(workerID, state string, activity *WorkerActivity, currentTool string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	w := pm.workers[workerID]
	if w == nil {
		return fmt.Errorf("heartbeat: worker %q not found", workerID)
	}
	if err := rejectDeadWorkerActivity("heartbeat", w); err != nil {
		return err
	}
	normalizedActivity, normalizedTool := normalizeWorkerHeartbeat(activity, currentTool)
	w.LastHeartbeat = time.Now()
	w.CurrentActivity = normalizedActivity
	w.CurrentTool = normalizedTool
	return nil
}

// KillWorker transitions a worker to dead and optionally calls HostAPI.KillWorker.
func (pm *PoolManager) KillWorker(workerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	w := pm.workers[workerID]
	if w == nil {
		return fmt.Errorf("kill worker: %q not found", workerID)
	}
	if w.Status == WorkerDead {
		return nil // already dead
	}

	if pm.hostAPI != nil {
		if err := pm.hostAPI.KillWorker(context.Background(), workerID); err != nil {
			return fmt.Errorf("kill worker: %w", err)
		}
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerDead,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("kill worker wal: %w", err)
	}
	Apply(pm, e)
	pm.requeueWorkerTasksLocked(workerID)
	return nil
}

// MarkDead marks a worker as dead without calling HostAPI (used by reaper).
func (pm *PoolManager) MarkDead(workerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	w := pm.workers[workerID]
	if w == nil {
		return fmt.Errorf("mark dead: worker %q not found", workerID)
	}
	if w.Status == WorkerDead {
		return nil
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerDead,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("mark dead wal: %w", err)
	}
	Apply(pm, e)

	// Requeue active tasks inline so mark-dead and requeue are atomic.
	pm.requeueWorkerTasksLocked(workerID)

	return nil
}

// MarkDeadIfStale atomically checks whether a worker's heartbeat has exceeded
// the stale threshold and marks it dead only if still stale. This avoids the
// TOCTOU race where a heartbeat arrives between the snapshot read and the
// MarkDead call.
func (pm *PoolManager) MarkDeadIfStale(workerID string, staleThreshold time.Duration) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	const spawnGracePeriod = 2 * time.Minute
	now := time.Now()

	w := pm.workers[workerID]
	if w == nil || w.Status == WorkerDead {
		return false
	}

	// Grace period for workers still starting up.
	if w.Status == WorkerSpawning && now.Sub(w.SpawnedAt) < spawnGracePeriod {
		return false
	}

	// Check heartbeat staleness under lock.
	if w.LastHeartbeat.IsZero() {
		if now.Sub(w.SpawnedAt) < spawnGracePeriod {
			return false
		}
	} else if now.Sub(w.LastHeartbeat) <= staleThreshold {
		return false
	}

	e := Event{
		Timestamp: now,
		Type:      EventWorkerDead,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e); err != nil {
		log.Printf("mark dead if stale: wal: %v", err)
		return false
	}
	Apply(pm, e)

	// Requeue active tasks inline so mark-dead and requeue are atomic.
	// This prevents the race where CompleteTask runs between mark-dead and
	// a separate RequeueOrphanedTasks call, finding a requeued task.
	pm.requeueWorkerTasksLocked(workerID)

	return true
}

// --- Task lifecycle ---

// EnqueueTask creates a new task and adds it to the priority queue.
func (pm *PoolManager) EnqueueTask(spec TaskSpec) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.enqueueTaskLocked(spec)
}

// enqueueTaskLocked is the lock-held implementation of EnqueueTask.
func (pm *PoolManager) enqueueTaskLocked(spec TaskSpec) (string, error) {
	tid := spec.ID
	if tid == "" {
		pm.taskSeq++
		tid = fmt.Sprintf("t-%d", pm.taskSeq)
	}
	if _, exists := pm.tasks[tid]; exists {
		return "", fmt.Errorf("enqueue task: %q already exists", tid)
	}

	// Check for circular dependencies.
	if len(spec.DependsOn) > 0 {
		getDeps := func(id string) []string {
			if d, ok := pm.queue.deps[id]; ok {
				return d
			}
			if t, ok := pm.tasks[id]; ok {
				return t.DependsOn
			}
			return nil
		}
		if pm.queue.HasCircularDeps(tid, spec.DependsOn, getDeps) {
			return "", fmt.Errorf("enqueue task: circular dependency detected for %q", tid)
		}
	}

	maxReviews := spec.MaxReviews
	if maxReviews == 0 {
		maxReviews = 3
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventTaskCreated,
		TaskID:    tid,
		Data: marshalData(TaskCreatedData{
			Prompt:     spec.Prompt,
			Priority:   spec.Priority,
			DependsOn:  spec.DependsOn,
			Role:       spec.Role,
			MaxReviews: maxReviews,
			PipelineID: spec.PipelineID,
			PlanID:     spec.PlanID,
			StageIndex: spec.StageIndex,
		}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		return "", fmt.Errorf("enqueue task wal: %w", err)
	}
	Apply(pm, e)

	pm.queue.Push(tid, spec.Priority, spec.DependsOn)
	return tid, nil
}

// DispatchTask assigns a specific task to a specific worker.
func (pm *PoolManager) DispatchTask(taskID, workerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("dispatch task: task %q not found", taskID)
	}
	if t.Status != TaskQueued {
		return fmt.Errorf("dispatch task: task %q is %q, expected queued", taskID, t.Status)
	}
	w := pm.workers[workerID]
	if w == nil {
		return fmt.Errorf("dispatch task: worker %q not found", workerID)
	}
	if w.Status != WorkerIdle {
		return fmt.Errorf("dispatch task: worker %q is %q, expected idle", workerID, w.Status)
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventTaskDispatched,
		TaskID:    taskID,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("dispatch task wal: %w", err)
	}
	Apply(pm, e)
	pm.queue.Remove(taskID)
	return nil
}

// DispatchNext pops the highest-priority ready task and dispatches it to the given worker.
func (pm *PoolManager) DispatchNext(workerID string) (*Task, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	w := pm.workers[workerID]
	if w == nil {
		return nil, fmt.Errorf("dispatch next: worker %q not found", workerID)
	}
	if w.Status != WorkerIdle {
		return nil, fmt.Errorf("dispatch next: worker %q is %q, expected idle", workerID, w.Status)
	}

	isDepSatisfied := func(depID string) bool {
		t := pm.tasks[depID]
		return t != nil && (t.Status == TaskCompleted || t.Status == TaskAccepted)
	}

	tid, ok := pm.queue.Pop(isDepSatisfied)
	if !ok {
		return nil, nil // no ready tasks
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventTaskDispatched,
		TaskID:    tid,
		WorkerID:  workerID,
	}
	if _, err := pm.wal.Append(e); err != nil {
		// Re-enqueue on WAL failure.
		t := pm.tasks[tid]
		pm.queue.Push(tid, t.Priority, t.DependsOn)
		return nil, fmt.Errorf("dispatch next wal: %w", err)
	}
	Apply(pm, e)
	return pm.tasks[tid], nil
}

// PollTask returns a copy of the task currently assigned to a worker, or nil if none.
func (pm *PoolManager) PollTask(workerID string) *Task {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	w := pm.workers[workerID]
	if w == nil || w.CurrentTaskID == "" {
		return nil
	}
	t := pm.tasks[w.CurrentTaskID]
	if t == nil || (t.Status != TaskDispatched && t.Status != TaskReviewing) {
		return nil
	}
	cp := *t
	return &cp
}

// CompleteTask marks a task as completed. It reads result.txt and handover.json
// from the worker's team directory on the filesystem, archives the output to
// the outputs/ side-file, and stores the result and handover in memory.
//
// File I/O (readWorkerFiles, readWorkerOutput) is performed before acquiring
// the lock so that slow filesystem reads don't block heartbeat processing.
func (pm *PoolManager) CompleteTask(workerID, taskID string) error {
	// Pre-read files outside the lock to avoid blocking heartbeats on slow I/O.
	result, handover := pm.readWorkerFiles(workerID, taskID)
	output := pm.readWorkerOutput(workerID)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("complete task: task %q not found", taskID)
	}

	// Guard: if the worker is already dead (reaped), log but still process
	// the completion so we don't silently discard work.
	w := pm.workers[workerID]
	if w != nil && w.Status == WorkerDead {
		log.Printf("complete task: worker %q is dead, processing completion for task %q anyway", workerID, taskID)
	}

	if t.Status != TaskDispatched {
		return fmt.Errorf("complete task: task %q is %q, expected dispatched", taskID, t.Status)
	}
	if t.WorkerID != workerID {
		return fmt.Errorf("complete task: task %q assigned to %q, not %q", taskID, t.WorkerID, workerID)
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventTaskCompleted,
		TaskID:    taskID,
		WorkerID:  workerID,
		Data: marshalData(TaskCompletedData{
			Summary:      result.Summary,
			FilesChanged: result.FilesChanged,
		}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("complete task wal: %w", err)
	}
	Apply(pm, e)

	// Store result and handover in memory.
	t = pm.tasks[taskID]
	t.Result = &result
	t.Handover = handover

	// Archive the full output to side-file.
	if output != "" {
		pm.writeOutputSideFile(taskID, output)
	}

	pm.sendNotify(Notification{Type: "task_completed", ID: taskID})
	return nil
}

// readWorkerFiles reads result.txt and handover.json from the worker's team directory.
func (pm *PoolManager) readWorkerFiles(workerID, taskID string) (TaskResult, *TaskHandover) {
	result := TaskResult{
		TaskID:   taskID,
		WorkerID: workerID,
		State:    "completed",
	}

	workerDir := WorkerStateDir(pm.cfg.StateDir, workerID)

	// Read result.txt for the summary.
	if data, err := os.ReadFile(filepath.Join(workerDir, WorkerResultFile)); err == nil {
		result.Summary = string(data)
	}

	// Read handover.json if present.
	var handover *TaskHandover
	if data, err := os.ReadFile(filepath.Join(workerDir, WorkerHandoverFile)); err == nil {
		var h TaskHandover
		if json.Unmarshal(data, &h) == nil {
			handover = &h
			if handover.Summary != "" {
				result.Summary = handover.Summary
			}
			// Bridge file paths from handover into result.
			for _, fc := range handover.FilesChanged {
				result.FilesChanged = append(result.FilesChanged, fc.Path)
			}
		}
	}

	return result, handover
}

// readWorkerOutput reads the raw result.txt content for archival.
func (pm *PoolManager) readWorkerOutput(workerID string) string {
	data, err := os.ReadFile(WorkerStatePath(pm.cfg.StateDir, workerID, WorkerResultFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// FailTask marks a task as failed and sets worker idle.
func (pm *PoolManager) FailTask(workerID, taskID, errMsg string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	t := pm.tasks[taskID]
	if t == nil {
		return fmt.Errorf("fail task: task %q not found", taskID)
	}
	if t.Status != TaskDispatched {
		return fmt.Errorf("fail task: task %q is %q, expected dispatched", taskID, t.Status)
	}
	if t.WorkerID != workerID {
		return fmt.Errorf("fail task: task %q assigned to %q, not %q", taskID, t.WorkerID, workerID)
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventTaskFailed,
		TaskID:    taskID,
		WorkerID:  workerID,
		Data:      marshalData(TaskFailedData{Error: errMsg}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("fail task wal: %w", err)
	}
	Apply(pm, e)

	pm.sendNotify(Notification{Type: "task_failed", ID: taskID})
	return nil
}

// --- Question lifecycle ---

// AskQuestion records a question from a worker and marks the worker blocked if blocking.
func (pm *PoolManager) AskQuestion(workerID string, q Question) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	w := pm.workers[workerID]
	if w == nil {
		return "", fmt.Errorf("ask question: worker %q not found", workerID)
	}
	if err := rejectDeadWorkerActivity("ask question", w); err != nil {
		return "", err
	}

	pm.qSeq++
	qid := fmt.Sprintf("q-%d", pm.qSeq)

	e := Event{
		Timestamp: time.Now(),
		Type:      EventWorkerQuestion,
		WorkerID:  workerID,
		TaskID:    q.TaskID,
		Data: marshalData(WorkerQuestionData{
			QuestionID: qid,
			Question:   q.Question,
			Category:   q.Category,
			Options:    q.Options,
			Blocking:   q.Blocking,
		}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		pm.qSeq--
		return "", fmt.Errorf("ask question wal: %w", err)
	}
	Apply(pm, e)

	pm.sendNotify(Notification{Type: "question", ID: qid, Message: q.Question})
	return qid, nil
}

// AnswerQuestion stores the answer and unblocks the worker.
func (pm *PoolManager) AnswerQuestion(questionID, answer, answeredBy string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	q := pm.questions[questionID]
	if q == nil {
		return fmt.Errorf("answer question: %q not found", questionID)
	}
	if q.Answered {
		return fmt.Errorf("answer question: %q already answered", questionID)
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventQuestionAnswered,
		Data: marshalData(QuestionAnsweredData{
			QuestionID: questionID,
			Answer:     answer,
			AnsweredBy: answeredBy,
		}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("answer question wal: %w", err)
	}
	Apply(pm, e)
	return nil
}

// GetQuestion returns a question by ID, or nil if not found.
func (pm *PoolManager) GetQuestion(qid string) *Question {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	q, ok := pm.questions[qid]
	if !ok {
		return nil
	}
	cp := *q
	return &cp
}

// PendingQuestions returns all unanswered questions.
func (pm *PoolManager) PendingQuestions() []Question {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var result []Question
	for _, q := range pm.questions {
		if !q.Answered {
			result = append(result, *q)
		}
	}
	return result
}

// --- Pipeline lifecycle ---

// SubmitPipeline validates and submits a pipeline, enqueuing stage 0 tasks.
func (pm *PoolManager) SubmitPipeline(pipe Pipeline) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if len(pipe.Stages) == 0 {
		return "", fmt.Errorf("submit pipeline: no stages")
	}
	for i, s := range pipe.Stages {
		if len(s.Tasks) == 0 {
			return "", fmt.Errorf("submit pipeline: stage %d (%q) has no tasks", i, s.Name)
		}
	}

	if pipe.ID == "" {
		pipe.ID = fmt.Sprintf("pipe-%d", len(pm.pipes)+1)
	}
	if _, exists := pm.pipes[pipe.ID]; exists {
		return "", fmt.Errorf("submit pipeline: %q already exists", pipe.ID)
	}

	pipe.Status = PipelineRunning
	pipe.CreatedAt = time.Now()

	e := Event{
		Timestamp: time.Now(),
		Type:      EventPipelineCreated,
		TaskID:    pipe.ID,
		Data:      marshalData(PipelineCreatedData{Pipeline: pipe}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		return "", fmt.Errorf("submit pipeline wal: %w", err)
	}
	Apply(pm, e)

	// Enqueue stage 0 tasks.
	for i, st := range pipe.Stages[0].Tasks {
		prompt := st.PromptTmpl
		prompt = strings.ReplaceAll(prompt, "{{.Goal}}", pipe.Goal)
		prompt = strings.ReplaceAll(prompt, "{{.PriorContext}}", "")

		tid := fmt.Sprintf("%s-s0-t%d", pipe.ID, i)
		if _, err := pm.enqueueTaskLocked(TaskSpec{
			ID:         tid,
			PipelineID: pipe.ID,
			StageIndex: 0,
			Prompt:     prompt,
			Role:       pipe.Stages[0].Role,
			Priority:   1,
			DependsOn:  st.DependsOn,
		}); err != nil {
			return "", fmt.Errorf("submit pipeline enqueue stage 0: %w", err)
		}
	}

	pm.sendNotify(Notification{Type: "pipeline_created", ID: pipe.ID})
	return pipe.ID, nil
}

// GetPipeline returns a copy of the pipeline.
func (pm *PoolManager) GetPipeline(pipeID string) (*Pipeline, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.pipes[pipeID]
	if !ok {
		return nil, false
	}
	cp := *p
	cp.Stages = make([]Stage, len(p.Stages))
	copy(cp.Stages, p.Stages)
	return &cp, true
}

// CancelPipeline cancels all in-flight tasks and marks the pipeline as failed.
func (pm *PoolManager) CancelPipeline(pipeID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	p := pm.pipes[pipeID]
	if p == nil {
		return fmt.Errorf("cancel pipeline: %q not found", pipeID)
	}
	if p.Status != PipelineRunning && p.Status != PipelineBlocked {
		return fmt.Errorf("cancel pipeline: %q is %q", pipeID, p.Status)
	}

	for _, t := range pm.tasks {
		if t.PipelineID != pipeID {
			continue
		}
		if t.Status == TaskQueued || t.Status == TaskDispatched {
			e := Event{
				Timestamp: time.Now(),
				Type:      EventTaskCanceled,
				TaskID:    t.ID,
			}
			if _, err := pm.wal.Append(e); err == nil {
				Apply(pm, e)
			}
		}
	}

	e := Event{
		Timestamp: time.Now(),
		Type:      EventPipelineFailed,
		TaskID:    pipeID,
		Data:      marshalData(PipelineFailedData{Reason: "canceled"}),
	}
	if _, err := pm.wal.Append(e); err != nil {
		return fmt.Errorf("cancel pipeline wal: %w", err)
	}
	Apply(pm, e)

	pm.sendNotify(Notification{Type: "pipeline_failed", ID: pipeID, Message: "canceled"})
	return nil
}

// PipelineStageTasks returns copies of tasks belonging to a pipeline stage.
func (pm *PoolManager) PipelineStageTasks(pipeID string, stageIdx int) []Task {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var result []Task
	for _, t := range pm.tasks {
		if t.PipelineID == pipeID && t.StageIndex == stageIdx {
			result = append(result, *t)
		}
	}
	return result
}

// --- Query methods (read-only) ---

// Workers returns a snapshot of all workers.
func (pm *PoolManager) Workers() []Worker {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]Worker, 0, len(pm.workers))
	for _, w := range pm.workers {
		cp := *w
		cp.CurrentActivity = cloneWorkerActivity(w.CurrentActivity)
		result = append(result, cp)
	}
	return result
}

// Tasks returns a snapshot of all tasks.
func (pm *PoolManager) Tasks() []Task {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]Task, 0, len(pm.tasks))
	for _, t := range pm.tasks {
		result = append(result, *t)
	}
	return result
}

// TaskSummaries returns a lightweight summary of all tasks.
func (pm *PoolManager) TaskSummaries() []TaskSummary {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	summaries := make([]TaskSummary, 0, len(pm.tasks))
	for _, t := range pm.tasks {
		summaries = append(summaries, t.Summary())
	}
	return summaries
}

// Worker returns a single worker by ID.
func (pm *PoolManager) Worker(id string) (*Worker, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	w, ok := pm.workers[id]
	if !ok {
		return nil, false
	}
	cp := *w
	cp.CurrentActivity = cloneWorkerActivity(w.CurrentActivity)
	return &cp, true
}

// Task returns a single task by ID.
func (pm *PoolManager) Task(id string) (*Task, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	t, ok := pm.tasks[id]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// QueuedTasks returns tasks currently in the priority queue.
func (pm *PoolManager) QueuedTasks() []Task {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var result []Task
	for _, t := range pm.tasks {
		if t.Status == TaskQueued {
			result = append(result, *t)
		}
	}
	return result
}

// AliveWorkers returns the count of workers not in dead state.
func (pm *PoolManager) AliveWorkers() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.aliveWorkersLocked()
}

// ValidateWorkerToken returns the owning worker ID when token matches a
// persisted per-worker token. Tokens are stored in durable worker state via
// the worker_spawned event, so callers do not need process-local replay.
func (pm *PoolManager) ValidateWorkerToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, w := range pm.workers {
		if w.Token == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(w.Token)) == 1 {
			return w.ID, true
		}
	}
	return "", false
}

// Notify returns the notification channel for the leader to read.
func (pm *PoolManager) Notify() <-chan Notification {
	return pm.notify
}

// StateDir returns the pool state directory path.
func (pm *PoolManager) StateDir() string {
	return pm.cfg.StateDir
}

// MaxWorkers returns the configured maximum worker count for the pool.
func (pm *PoolManager) MaxWorkers() int {
	return pm.cfg.MaxWorkers
}

// PlanStore returns the configured PlanStore, or nil if not configured.
func (pm *PoolManager) PlanStore() *PlanStore {
	return pm.cfg.PlanStore
}

// CheckSession delegates to the HostAPI to check if a session is alive.
func (pm *PoolManager) CheckSession(ctx context.Context, sessionID string) (bool, error) {
	if pm.hostAPI == nil {
		return false, fmt.Errorf("host API not configured")
	}
	return pm.hostAPI.CheckSession(ctx, sessionID)
}

// ResolveModel returns the ModelConfig for the given role via the router.
// Returns zero-value ModelConfig if no router is configured.
func (pm *PoolManager) ResolveModel(role string) ModelConfig {
	if pm.cfg.Router == nil {
		return ModelConfig{}
	}
	return pm.cfg.Router.Resolve(role)
}

// --- internal helpers ---

// requeueWorkerTasksLocked requeues active tasks assigned to (or being reviewed
// by) the given worker. Tasks in terminal states are skipped. Must be called
// with pm.mu held.
func (pm *PoolManager) requeueWorkerTasksLocked(workerID string) {
	for _, t := range pm.tasks {
		if isTerminalStatus(t.Status) {
			continue
		}

		pushToQueue := false
		switch {
		case t.Status == TaskDispatched && t.WorkerID == workerID:
			pushToQueue = true
		case t.Status == TaskReviewing && t.ReviewerID == workerID:
			// Reviewing tasks don't need queue insertion — they'll be
			// re-dispatched for review by the leader's scheduling loop.
		default:
			continue
		}

		evType := EventTaskRequeued
		if t.Status == TaskReviewing {
			evType = EventReviewAborted
		}
		e := Event{
			Timestamp: time.Now(),
			Type:      evType,
			TaskID:    t.ID,
		}
		// Save queue metadata before Apply clears WorkerID/ReviewerID.
		priority, deps := t.Priority, t.DependsOn
		if _, err := pm.wal.Append(e); err != nil {
			log.Printf("requeue worker task: WAL append failed for task %s: %v", t.ID, err)
			continue
		}
		Apply(pm, e)
		pm.sendNotify(Notification{Type: "task_requeued", ID: t.ID})

		if pushToQueue {
			pm.queue.Push(t.ID, priority, deps)
		}
	}
}

func (pm *PoolManager) aliveWorkersLocked() int {
	count := 0
	for _, w := range pm.workers {
		if w.Status != WorkerDead {
			count++
		}
	}
	return count
}

func (pm *PoolManager) sendNotify(n Notification) {
	select {
	case pm.notify <- n:
	default:
		// Channel full; drop notification to avoid blocking.
	}

	// Wake task-specific waiters registered via WaitForTask.
	if n.ID != "" {
		if waiters, ok := pm.taskWaiters[n.ID]; ok {
			for _, ch := range waiters {
				select {
				case ch <- n:
				default:
				}
			}
			delete(pm.taskWaiters, n.ID)
		}
	}
}

// removeWaiterLocked removes a specific waiter channel from the taskWaiters
// slice. Caller must hold pm.mu (write lock).
func (pm *PoolManager) removeWaiterLocked(taskID string, ch chan Notification) {
	waiters := pm.taskWaiters[taskID]
	for i, w := range waiters {
		if w == ch {
			pm.taskWaiters[taskID] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(pm.taskWaiters[taskID]) == 0 {
		delete(pm.taskWaiters, taskID)
	}
}

// WaitForTask blocks until the given task reaches a terminal state (completed,
// failed, canceled, accepted, rejected, escalated) or the context is canceled.
// Returns a snapshot of the task including its result. Uses a loop with a
// 5-second poll safety net so that missed notifications cannot cause permanent
// blocking. Callers should invoke this from a separate goroutine (e.g. a
// background MCP tool call) so the leader remains free to schedule additional
// work.
func (pm *PoolManager) WaitForTask(ctx context.Context, taskID string) (*Task, error) {
	// Fast path: check if task is already terminal under RLock.
	pm.mu.RLock()
	t := pm.tasks[taskID]
	if t == nil {
		pm.mu.RUnlock()
		return nil, fmt.Errorf("wait: task %q not found", taskID)
	}
	if isTerminalStatus(t.Status) {
		cp := *t
		pm.mu.RUnlock()
		return &cp, nil
	}
	pm.mu.RUnlock()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		// Acquire write lock, re-check terminal status, register waiter.
		pm.mu.Lock()
		t = pm.tasks[taskID]
		if t == nil {
			pm.mu.Unlock()
			return nil, fmt.Errorf("wait: task %q not found", taskID)
		}
		if isTerminalStatus(t.Status) {
			cp := *t
			pm.mu.Unlock()
			return &cp, nil
		}
		ch := make(chan Notification, 1)
		pm.taskWaiters[taskID] = append(pm.taskWaiters[taskID], ch)
		pm.mu.Unlock()

		select {
		case <-ch:
			// Notification received — loop to re-check status under lock.
			continue
		case <-ticker.C:
			// Poll safety net — remove stale waiter and loop to re-check.
			pm.mu.Lock()
			pm.removeWaiterLocked(taskID, ch)
			pm.mu.Unlock()
			continue
		case <-ctx.Done():
			pm.mu.Lock()
			pm.removeWaiterLocked(taskID, ch)
			pm.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

const maxOutputSize = 1 << 20 // 1 MB

// writeOutputSideFile writes the full AI output to <stateDir>/outputs/<taskId>.txt.
func (pm *PoolManager) writeOutputSideFile(taskID, output string) {
	dir := filepath.Join(pm.cfg.StateDir, "outputs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("write output side-file: mkdir: %v", err)
		return
	}
	if len(output) > maxOutputSize {
		output = output[:maxOutputSize]
	}
	path := filepath.Join(dir, taskID+".txt")
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		log.Printf("write output side-file: %v", err)
	}
}

// ReadTaskOutput reads the full AI output from the side-file for the given task.
func (pm *PoolManager) ReadTaskOutput(taskID string) (string, error) {
	if err := ValidateID(taskID); err != nil {
		return "", err
	}
	path := filepath.Join(pm.cfg.StateDir, "outputs", filepath.Base(taskID)+".txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read task output: %w", err)
	}
	return string(data), nil
}
