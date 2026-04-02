package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/internal/adapter"
	"github.com/SkrobyLabs/mittens/internal/pool"
)

// toolDef defines an MCP tool: its name, description, JSON Schema, and handler.
type toolDef struct {
	Name        string
	Description string
	Schema      map[string]any
	Handler     func(*pool.PoolManager, json.RawMessage) (any, error)
}

// mcpTools is the registry of all MCP tools exposed to the leader.
var mcpTools = []toolDef{
	{
		Name:        "spawn_worker",
		Description: "Create a new worker container. Optionally specify role, adapter, model, provider, memory, cpus.",
		Schema: obj(
			prop("role", "string", "Worker role (e.g. planner, implementer, reviewer)"),
			prop("adapter", "string", "AI adapter override"),
			prop("model", "string", "Model override"),
			prop("provider", "string", "Provider override"),
			prop("memory", "string", "Memory limit (e.g. 4g)"),
			prop("cpus", "string", "CPU limit (e.g. 2)"),
		),
		Handler: handleSpawnWorker,
	},
	{
		Name:        "kill_worker",
		Description: "Remove a worker container and mark it dead.",
		Schema: obj(
			required("workerId"),
			prop("workerId", "string", "ID of the worker to kill"),
		),
		Handler: handleKillWorker,
	},
	{
		Name:        "enqueue_task",
		Description: "Add a task to the priority queue for dispatch.",
		Schema: obj(
			required("prompt"),
			prop("prompt", "string", "Task prompt/instructions for the worker"),
			prop("role", "string", "Preferred worker role for this task"),
			prop("priority", "integer", "Priority (lower = higher priority, default 1)"),
			propArray("dependsOn", "Task IDs this task depends on", map[string]any{"type": "string"}),
			prop("planId", "string", "Optional plan ID to associate this task with"),
		),
		Handler: handleEnqueueTask,
	},
	{
		Name:        "dispatch_task",
		Description: "Assign a specific queued task to a specific idle worker.",
		Schema: obj(
			required("taskId", "workerId"),
			prop("taskId", "string", "ID of the queued task"),
			prop("workerId", "string", "ID of the idle worker"),
		),
		Handler: handleDispatchTask,
	},
	{
		Name:        "get_status",
		Description: "Get a structured overview of all workers, tasks, queued items, and pipelines.",
		Schema:      obj(),
		Handler:     handleGetStatus,
	},
	{
		Name:        "get_worker_activity",
		Description: "Inspect a specific worker's live activity with focused task, question, and worker-side artifact context. Use this after get_status when a live worker row needs deeper inspection.",
		Schema: obj(
			required("workerId"),
			prop("workerId", "string", "ID of the worker to inspect"),
		),
		Handler: handleGetWorkerActivity,
	},
	{
		Name:        "get_pool_state",
		Description: "Get a compact pool summary for cheap scheduling and capacity checks. Prefer this over get_status unless you need full worker/task inventories.",
		Schema:      obj(),
		Handler:     handleGetPoolState,
	},
	{
		Name:        "get_task_result",
		Description: "Get compact details and result of a specific task. Full worker output is available via get_task_output, and the full task prompt is optional via includePrompt.",
		Schema: obj(
			required("taskId"),
			prop("taskId", "string", "ID of the task"),
			prop("includePrompt", "boolean", "Include the full task prompt in the response (default false)"),
		),
		Handler: handleGetTaskResult,
	},
	{
		Name:        "get_task_state",
		Description: "Get a minimal per-task monitoring view for cheap polling. Use this for active task monitoring instead of get_status/get_task_result.",
		Schema: obj(
			required("taskId"),
			prop("taskId", "string", "ID of the task"),
		),
		Handler: handleGetTaskState,
	},
	{
		Name:        "get_task_output",
		Description: "Read the full worker output for a completed task. Use this to see the complete AI output (not just the summary).",
		Schema: obj(
			required("taskId"),
			prop("taskId", "string", "ID of the task"),
		),
		Handler: handleGetTaskOutput,
	},
	{
		Name:        "submit_pipeline",
		Description: "Submit a multi-stage pipeline for autonomous execution.",
		Schema: obj(
			required("goal", "stages"),
			prop("goal", "string", "High-level goal for the pipeline"),
			propObj("stages", "array", "Pipeline stages", map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Stage name"},
					"role": map[string]any{"type": "string", "description": "Worker role for this stage"},
					"fan":  map[string]any{"type": "string", "enum": []any{"fan-out", "fan-in", "streaming"}, "description": "Fan mode"},
					"tasks": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"properties": map[string]any{
								"id":         map[string]any{"type": "string"},
								"promptTmpl": map[string]any{"type": "string"},
								"dependsOn":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
						},
					},
				},
			}),
		),
		Handler: handleSubmitPipeline,
	},
	{
		Name:        "cancel_pipeline",
		Description: "Cancel a running pipeline and all its in-flight tasks.",
		Schema: obj(
			required("pipelineId"),
			prop("pipelineId", "string", "ID of the pipeline to cancel"),
		),
		Handler: handleCancelPipeline,
	},
	{
		Name:        "dispatch_review",
		Description: "Send a completed task to a reviewer. Auto-picks a reviewer if reviewerId is omitted.",
		Schema: obj(
			required("taskId"),
			prop("taskId", "string", "ID of the completed task to review"),
			prop("reviewerId", "string", "Optional: specific reviewer worker ID"),
		),
		Handler: handleDispatchReview,
	},
	{
		Name:        "report_review",
		Description: "Report a review verdict (pass/fail) for a task under review.",
		Schema: obj(
			required("taskId", "verdict"),
			prop("taskId", "string", "ID of the task being reviewed"),
			prop("verdict", "string", "Review verdict: pass or fail"),
			prop("feedback", "string", "Review feedback text"),
			prop("severity", "string", "Issue severity: minor, major, or critical"),
		),
		Handler: handleReportReview,
	},
	{
		Name:        "answer_question",
		Description: "Answer a pending question from a blocked worker.",
		Schema: obj(
			required("questionId", "answer"),
			prop("questionId", "string", "ID of the question to answer"),
			prop("answer", "string", "Answer text"),
		),
		Handler: handleAnswerQuestion,
	},
	{
		Name:        "resolve_escalation",
		Description: "Resolve an escalated task: accept, retry (with extra review cycles), or abort.",
		Schema: obj(
			required("taskId", "action"),
			prop("taskId", "string", "ID of the escalated task"),
			prop("action", "string", "Action: accept, retry, or abort"),
			prop("extraCycles", "integer", "Extra review cycles for retry (default 1)"),
		),
		Handler: handleResolveEscalation,
	},
	{
		Name:        "pending_questions",
		Description: "List all unanswered questions from workers.",
		Schema:      obj(),
		Handler:     handlePendingQuestions,
	},
	{
		Name:        "create_plan",
		Description: "Create a persistent plan from title and content. Returns the plan ID.",
		Schema: obj(
			required("title", "content"),
			prop("title", "string", "Short title for the plan"),
			prop("content", "string", "Full plan content (markdown)"),
		),
		Handler: handleCreatePlan,
	},
	{
		Name:        "list_plans",
		Description: "List all persistent plans with their status.",
		Schema:      obj(),
		Handler:     handleListPlans,
	},
	{
		Name:        "read_plan",
		Description: "Read the full content of a plan.",
		Schema: obj(
			required("planId"),
			prop("planId", "string", "ID of the plan to read"),
		),
		Handler: handleReadPlan,
	},
	{
		Name:        "claim_plan",
		Description: "Claim an existing plan for the current session.",
		Schema: obj(
			required("planId"),
			prop("planId", "string", "ID of the plan to claim"),
		),
		Handler: handleClaimPlan,
	},
	{
		Name:        "update_plan_progress",
		Description: "Append a progress entry to a plan's log.",
		Schema: obj(
			required("planId", "entry"),
			prop("planId", "string", "ID of the plan"),
			prop("entry", "string", "Progress entry text"),
		),
		Handler: handleUpdatePlanProgress,
	},
	{
		Name:        "complete_plan",
		Description: "Mark a plan as completed.",
		Schema: obj(
			required("planId"),
			prop("planId", "string", "ID of the plan to complete"),
		),
		Handler: handleCompletePlan,
	},
	{
		Name:        "check_session",
		Description: "Check if a team session is still alive (has a running leader container).",
		Schema: obj(
			required("sessionId"),
			prop("sessionId", "string", "Session ID to check"),
		),
		Handler: handleCheckSession,
	},
	{
		Name:        "wait_for_task",
		Description: "Block until a task leaves its active state (dispatched/reviewing) and return the full task with result. Call this as a BACKGROUND tool so the leader stays free to schedule more work.",
		Schema: obj(
			required("taskId"),
			prop("taskId", "string", "ID of the task to wait on"),
			prop("timeoutSec", "integer", "Timeout in seconds (default 300)"),
		),
		Handler: handleWaitForTask,
	},
}

// --- Tool handlers ---

func handleSpawnWorker(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		Role     string `json:"role"`
		Adapter  string `json:"adapter"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Memory   string `json:"memory"`
		CPUs     string `json:"cpus"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	spec := pool.WorkerSpec{
		Role:     p.Role,
		Adapter:  p.Adapter,
		Model:    p.Model,
		Provider: p.Provider,
		Memory:   p.Memory,
		CPUs:     p.CPUs,
	}

	// Auto-resolve model from router when not explicitly provided.
	if spec.Model == "" && spec.Role != "" {
		mc := pm.ResolveModel(spec.Role)
		if mc.Model != "" {
			spec.Model = mc.Model
		}
		if spec.Provider == "" && mc.Provider != "" {
			spec.Provider = mc.Provider
		}
		if spec.Adapter == "" && mc.Adapter != "" {
			spec.Adapter = mc.Adapter
		}
	}
	if spec.Adapter == "" {
		spec.Adapter = adapter.DefaultAdapterForProvider(spec.Provider)
	}

	// SpawnWorker generates a per-worker token, injects it into
	// spec.Environment, and persists it in durable worker state via WAL.
	w, err := pm.SpawnWorker(spec)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"worker": w,
	}, nil
}

func handleKillWorker(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string `json:"workerId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.WorkerID == "" {
		return nil, fmt.Errorf("missing required field: workerId")
	}
	if err := pm.KillWorker(p.WorkerID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workerId": p.WorkerID}, nil
}

func handleEnqueueTask(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		Prompt    string   `json:"prompt"`
		Role      string   `json:"role"`
		Priority  int      `json:"priority"`
		DependsOn []string `json:"dependsOn"`
		PlanID    string   `json:"planId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Prompt == "" {
		return nil, fmt.Errorf("missing required field: prompt")
	}
	if p.Priority == 0 {
		p.Priority = 1
	}

	tid, err := pm.EnqueueTask(pool.TaskSpec{
		Prompt:    p.Prompt,
		Role:      p.Role,
		Priority:  p.Priority,
		DependsOn: p.DependsOn,
		PlanID:    p.PlanID,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"taskId": tid}, nil
}

func handleDispatchTask(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID   string `json:"taskId"`
		WorkerID string `json:"workerId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" || p.WorkerID == "" {
		return nil, fmt.Errorf("missing required fields: taskId, workerId")
	}
	if err := pm.DispatchTask(p.TaskID, p.WorkerID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "taskId": p.TaskID, "workerId": p.WorkerID}, nil
}

type statusResult struct {
	Workers          []statusWorkerView `json:"workers"`
	AliveCount       int                `json:"aliveWorkers"`
	Tasks            []pool.TaskSummary `json:"tasks"`
	QueuedCount      int                `json:"queuedTasks"`
	Pipelines        []pool.Pipeline    `json:"pipelines,omitempty"`
	PendingQuestions int                `json:"pendingQuestions"`
}

type statusWorkerView struct {
	pool.Worker
	ActivitySummary   string `json:"activitySummary,omitempty"`
	PendingQuestion   string `json:"pendingQuestion,omitempty"`
	PendingQuestionID string `json:"pendingQuestionId,omitempty"`
	InspectionTool    string `json:"inspectionTool,omitempty"`
}

type poolStateResult struct {
	TotalWorkers      int            `json:"totalWorkers"`
	AliveWorkers      int            `json:"aliveWorkers"`
	MaxWorkers        int            `json:"maxWorkers"`
	IdleWorkers       int            `json:"idleWorkers"`
	WorkingWorkers    int            `json:"workingWorkers"`
	BlockedWorkers    int            `json:"blockedWorkers"`
	SpawningWorkers   int            `json:"spawningWorkers"`
	DeadWorkers       int            `json:"deadWorkers"`
	WorkersByRole     map[string]int `json:"workersByRole,omitempty"`
	IdleWorkersByRole map[string]int `json:"idleWorkersByRole,omitempty"`
	TotalTasks        int            `json:"totalTasks"`
	QueuedTasks       int            `json:"queuedTasks"`
	ActiveTasks       int            `json:"activeTasks"`
	ReviewingTasks    int            `json:"reviewingTasks"`
	TerminalTasks     int            `json:"terminalTasks"`
	PendingQuestions  int            `json:"pendingQuestions"`
}

type workerActivityResult struct {
	Worker          statusWorkerView         `json:"worker"`
	Task            *workerActivityTaskView  `json:"task,omitempty"`
	PendingQuestion *pool.Question           `json:"pendingQuestion,omitempty"`
	Artifacts       *workerActivityArtifacts `json:"artifacts,omitempty"`
	RecentActivity  []workerActivityEntry    `json:"recentActivity,omitempty"`
}

type workerActivityTaskView struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	Role            string     `json:"role,omitempty"`
	PlanID          string     `json:"planId,omitempty"`
	PipelineID      string     `json:"pipelineId,omitempty"`
	ReviewCycles    int        `json:"reviewCycles"`
	MaxReviews      int        `json:"maxReviews"`
	DispatchedAt    *time.Time `json:"dispatchedAt,omitempty"`
	PromptPreview   string     `json:"promptPreview,omitempty"`
	PromptTruncated bool       `json:"promptTruncated,omitempty"`
	ResultSummary   string     `json:"resultSummary,omitempty"`
	HasHandover     bool       `json:"hasHandover,omitempty"`
}

type workerActivityArtifacts struct {
	HasTaskFile           bool   `json:"hasTaskFile,omitempty"`
	TaskPreview           string `json:"taskPreview,omitempty"`
	TaskPreviewTruncated  bool   `json:"taskPreviewTruncated,omitempty"`
	HasResultFile         bool   `json:"hasResultFile,omitempty"`
	HasHandoverFile       bool   `json:"hasHandoverFile,omitempty"`
	HasErrorFile          bool   `json:"hasErrorFile,omitempty"`
	ErrorPreview          string `json:"errorPreview,omitempty"`
	ErrorPreviewTruncated bool   `json:"errorPreviewTruncated,omitempty"`
}

type workerActivityEntry struct {
	RecordedAt time.Time            `json:"recordedAt"`
	TaskID     string               `json:"taskId,omitempty"`
	Activity   *pool.WorkerActivity `json:"activity,omitempty"`
}

const liveWorkerActivityMaxAge = 90 * time.Second
const workerActivityHistoryTail = 8

func handleGetStatus(pm *pool.PoolManager, _ json.RawMessage) (any, error) {
	summaries := pm.TaskSummaries()
	pendingQuestions := pm.PendingQuestions()
	pendingByWorker := pendingQuestionsByWorker(pendingQuestions)
	workers := pm.Workers()
	workerViews := make([]statusWorkerView, 0, len(workers))
	for _, w := range workers {
		workerViews = append(workerViews, buildStatusWorkerView(w, pendingByWorker[w.ID]))
	}

	// Collect unique pipeline IDs from task summaries.
	pipeIDs := map[string]bool{}
	allTasks := pm.Tasks()
	for _, t := range allTasks {
		if t.PipelineID != "" {
			pipeIDs[t.PipelineID] = true
		}
	}
	var pipes []pool.Pipeline
	for pid := range pipeIDs {
		if p, ok := pm.GetPipeline(pid); ok {
			pipes = append(pipes, *p)
		}
	}

	return statusResult{
		Workers:          workerViews,
		AliveCount:       pm.AliveWorkers(),
		Tasks:            summaries,
		QueuedCount:      len(pm.QueuedTasks()),
		Pipelines:        pipes,
		PendingQuestions: len(pendingQuestions),
	}, nil
}

func handleGetWorkerActivity(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string `json:"workerId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.WorkerID == "" {
		return nil, fmt.Errorf("missing required field: workerId")
	}

	worker, ok := pm.Worker(p.WorkerID)
	if !ok {
		return nil, fmt.Errorf("worker %q not found", p.WorkerID)
	}

	pendingByWorker := pendingQuestionsByWorker(pm.PendingQuestions())
	pending := pendingByWorker[p.WorkerID]
	result := workerActivityResult{
		Worker: buildStatusWorkerView(*worker, pending),
	}
	if pending != nil {
		q := *pending
		result.PendingQuestion = &q
	}
	if worker.CurrentTaskID != "" {
		if task, ok := pm.Task(worker.CurrentTaskID); ok {
			result.Task = buildWorkerActivityTaskView(*task)
		}
	}
	if shouldSurfaceWorkerArtifacts(*worker) {
		artifacts, err := readWorkerActivityArtifacts(pm.StateDir(), worker.ID)
		if err != nil {
			return nil, err
		}
		result.Artifacts = artifacts
	}
	history, err := readWorkerActivityHistory(pm.StateDir(), worker.ID, workerActivityHistoryTail)
	if err != nil {
		return nil, err
	}
	result.RecentActivity = history
	return result, nil
}

func pendingQuestionsByWorker(questions []pool.Question) map[string]*pool.Question {
	if len(questions) == 0 {
		return nil
	}
	byWorker := make(map[string]*pool.Question, len(questions))
	for i := range questions {
		q := questions[i]
		existing := byWorker[q.WorkerID]
		if existing != nil && !shouldPreferPendingQuestion(q, *existing) {
			continue
		}
		cp := q
		byWorker[q.WorkerID] = &cp
	}
	return byWorker
}

func shouldPreferPendingQuestion(candidate, existing pool.Question) bool {
	if candidate.Blocking != existing.Blocking {
		return candidate.Blocking
	}
	if existing.AskedAt.IsZero() {
		return true
	}
	if candidate.AskedAt.IsZero() {
		return false
	}
	if !candidate.AskedAt.Equal(existing.AskedAt) {
		return candidate.AskedAt.Before(existing.AskedAt)
	}
	return candidate.ID < existing.ID
}

func buildStatusWorkerView(worker pool.Worker, pending *pool.Question) statusWorkerView {
	if !canSurfaceLiveWorkerState(worker) {
		worker.CurrentActivity = nil
		worker.CurrentTool = ""
	}
	view := statusWorkerView{
		Worker: worker,
	}
	if pending != nil {
		view.PendingQuestionID = pending.ID
		view.PendingQuestion = pending.Question
	}
	view.ActivitySummary = summarizeWorkerActivity(worker, pending)
	if shouldOfferWorkerInspection(worker, pending) {
		view.InspectionTool = "get_worker_activity"
	}
	return view
}

func summarizeWorkerActivity(worker pool.Worker, pending *pool.Question) string {
	if shouldPrioritizePendingQuestion(pending) {
		if question := strings.TrimSpace(pending.Question); question != "" {
			return question
		}
		return fmt.Sprintf("Awaiting answer to %s", pending.ID)
	}
	if canSurfaceLiveWorkerState(worker) && worker.CurrentActivity != nil {
		if summary := strings.TrimSpace(worker.CurrentActivity.Summary); summary != "" {
			return summary
		}
		if label := formatActivityLabel(worker.CurrentActivity); label != "" {
			return label
		}
	}
	if canSurfaceLiveWorkerState(worker) && worker.CurrentTool != "" {
		return fmt.Sprintf("Using %s", worker.CurrentTool)
	}
	if worker.CurrentTaskID != "" {
		switch worker.Status {
		case pool.WorkerBlocked:
			return fmt.Sprintf("Blocked on %s", worker.CurrentTaskID)
		case pool.WorkerWorking:
			return fmt.Sprintf("Working on %s", worker.CurrentTaskID)
		default:
			return fmt.Sprintf("Assigned %s", worker.CurrentTaskID)
		}
	}
	return ""
}

func shouldPrioritizePendingQuestion(pending *pool.Question) bool {
	return pending != nil && pending.Blocking
}

func canSurfaceLiveWorkerState(worker pool.Worker) bool {
	if worker.CurrentActivity == nil && worker.CurrentTool == "" {
		return false
	}
	switch worker.Status {
	case pool.WorkerWorking, pool.WorkerBlocked:
	default:
		return false
	}
	if worker.LastHeartbeat.IsZero() {
		return false
	}
	return time.Since(worker.LastHeartbeat) <= liveWorkerActivityMaxAge
}

func formatActivityLabel(activity *pool.WorkerActivity) string {
	if activity == nil {
		return ""
	}
	kind := strings.TrimSpace(activity.Kind)
	name := strings.TrimSpace(activity.Name)
	phase := strings.TrimSpace(activity.Phase)

	label := ""
	switch {
	case kind != "" && name != "":
		label = fmt.Sprintf("%s %s", kind, name)
	case name != "":
		label = name
	case kind != "":
		label = kind
	}
	if phase == "" {
		return label
	}
	if label == "" {
		return phase
	}
	return fmt.Sprintf("%s (%s)", label, phase)
}

func shouldOfferWorkerInspection(worker pool.Worker, pending *pool.Question) bool {
	return pending != nil || worker.CurrentTaskID != "" || canSurfaceLiveWorkerState(worker)
}

func shouldSurfaceWorkerArtifacts(worker pool.Worker) bool {
	return worker.CurrentTaskID != "" || canSurfaceLiveWorkerState(worker)
}

func buildWorkerActivityTaskView(task pool.Task) *workerActivityTaskView {
	view := &workerActivityTaskView{
		ID:           task.ID,
		Status:       task.Status,
		Role:         task.Role,
		PlanID:       task.PlanID,
		PipelineID:   task.PipelineID,
		ReviewCycles: task.ReviewCycles,
		MaxReviews:   task.MaxReviews,
		DispatchedAt: task.DispatchedAt,
		HasHandover:  task.Handover != nil,
	}
	if task.Prompt != "" {
		view.PromptPreview, view.PromptTruncated = compactTextPreview(task.Prompt, 240)
	}
	if task.Result != nil {
		view.ResultSummary = task.Result.Summary
	}
	return view
}

func readWorkerActivityArtifacts(stateDir, workerID string) (*workerActivityArtifacts, error) {
	dir := filepath.Join(stateDir, "workers", workerID)
	result := &workerActivityArtifacts{}
	var found bool

	if preview, truncated, ok, err := readWorkerArtifactPreview(dir, "task.md", 400); err != nil {
		return nil, fmt.Errorf("read worker activity task.md: %w", err)
	} else if ok {
		result.HasTaskFile = true
		result.TaskPreview = preview
		result.TaskPreviewTruncated = truncated
		found = true
	}

	for _, name := range []string{"result.txt", "handover.json"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			found = true
			if name == "result.txt" {
				result.HasResultFile = true
			} else {
				result.HasHandoverFile = true
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat worker activity artifact %s: %w", name, err)
		}
	}

	if preview, truncated, ok, err := readWorkerArtifactPreview(dir, "error.txt", 240); err != nil {
		return nil, fmt.Errorf("read worker activity error.txt: %w", err)
	} else if ok {
		result.HasErrorFile = true
		result.ErrorPreview = preview
		result.ErrorPreviewTruncated = truncated
		found = true
	}

	if !found {
		return nil, nil
	}
	return result, nil
}

func readWorkerArtifactPreview(dir, name string, maxLen int) (string, bool, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, false, nil
		}
		return "", false, false, err
	}
	preview, truncated := compactTextPreview(string(data), maxLen)
	return preview, truncated, true, nil
}

func readWorkerActivityHistory(stateDir, workerID string, maxEntries int) ([]workerActivityEntry, error) {
	if maxEntries <= 0 {
		return nil, nil
	}

	dir := pool.WorkerStateDir(stateDir, workerID)
	records, err := appendWorkerActivityRecords(nil, filepath.Join(dir, pool.WorkerActivityArchiveFile))
	if err != nil {
		return nil, fmt.Errorf("read worker activity archive: %w", err)
	}
	records, err = appendWorkerActivityRecords(records, filepath.Join(dir, pool.WorkerActivityLogFile))
	if err != nil {
		return nil, fmt.Errorf("read worker activity log: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	if len(records) > maxEntries {
		records = records[len(records)-maxEntries:]
	}

	history := make([]workerActivityEntry, 0, len(records))
	for _, record := range records {
		activity := record.Activity
		history = append(history, workerActivityEntry{
			RecordedAt: record.RecordedAt,
			TaskID:     record.TaskID,
			Activity:   &activity,
		})
	}
	return history, nil
}

func appendWorkerActivityRecords(dst []pool.WorkerActivityRecord, path string) ([]pool.WorkerActivityRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return dst, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var record pool.WorkerActivityRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		dst = append(dst, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return dst, nil
}

func handleGetPoolState(pm *pool.PoolManager, _ json.RawMessage) (any, error) {
	workers := pm.Workers()
	tasks := pm.Tasks()

	result := poolStateResult{
		TotalWorkers:      len(workers),
		AliveWorkers:      pm.AliveWorkers(),
		MaxWorkers:        pm.MaxWorkers(),
		WorkersByRole:     map[string]int{},
		IdleWorkersByRole: map[string]int{},
		TotalTasks:        len(tasks),
		PendingQuestions:  len(pm.PendingQuestions()),
	}

	for _, w := range workers {
		if w.Role != "" {
			result.WorkersByRole[w.Role]++
		}
		switch w.Status {
		case pool.WorkerIdle:
			result.IdleWorkers++
			if w.Role != "" {
				result.IdleWorkersByRole[w.Role]++
			}
		case pool.WorkerWorking:
			result.WorkingWorkers++
		case pool.WorkerBlocked:
			result.BlockedWorkers++
		case pool.WorkerSpawning:
			result.SpawningWorkers++
		case pool.WorkerDead:
			result.DeadWorkers++
		}
	}

	for _, t := range tasks {
		switch t.Status {
		case pool.TaskQueued:
			result.QueuedTasks++
		case pool.TaskDispatched:
			result.ActiveTasks++
		case pool.TaskReviewing:
			result.ActiveTasks++
			result.ReviewingTasks++
		default:
			if isTerminalTaskStatus(t.Status) {
				result.TerminalTasks++
			}
		}
	}

	if len(result.WorkersByRole) == 0 {
		result.WorkersByRole = nil
	}
	if len(result.IdleWorkersByRole) == 0 {
		result.IdleWorkersByRole = nil
	}

	return result, nil
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case pool.TaskCompleted, pool.TaskFailed, pool.TaskCanceled, pool.TaskAccepted, pool.TaskRejected, pool.TaskEscalated:
		return true
	default:
		return false
	}
}

func handleGetTaskResult(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID        string `json:"taskId"`
		IncludePrompt bool   `json:"includePrompt"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" {
		return nil, fmt.Errorf("missing required field: taskId")
	}
	t, ok := pm.Task(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("task %q not found", p.TaskID)
	}

	type taskResultView struct {
		pool.TaskSummary
		PlanID          string           `json:"planId,omitempty"`
		PipelineID      string           `json:"pipelineId,omitempty"`
		MaxReviews      int              `json:"maxReviews"`
		Result          *pool.TaskResult `json:"result,omitempty"`
		Prompt          string           `json:"prompt,omitempty"`
		PromptPreview   string           `json:"promptPreview,omitempty"`
		PromptTruncated bool             `json:"promptTruncated,omitempty"`
	}
	result := taskResultView{
		TaskSummary: t.Summary(),
		PlanID:      t.PlanID,
		PipelineID:  t.PipelineID,
		MaxReviews:  t.MaxReviews,
		Result:      t.Result,
	}
	if p.IncludePrompt {
		result.Prompt = t.Prompt
	} else if t.Prompt != "" {
		result.PromptPreview, result.PromptTruncated = compactPromptPreview(t.Prompt, 240)
	}
	return result, nil
}

func handleGetTaskState(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" {
		return nil, fmt.Errorf("missing required field: taskId")
	}
	t, ok := pm.Task(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("task %q not found", p.TaskID)
	}

	type taskStateView struct {
		ID            string     `json:"id"`
		Status        string     `json:"status"`
		Role          string     `json:"role,omitempty"`
		WorkerID      string     `json:"workerId,omitempty"`
		ReviewerID    string     `json:"reviewerId,omitempty"`
		PlanID        string     `json:"planId,omitempty"`
		PipelineID    string     `json:"pipelineId,omitempty"`
		ReviewCycles  int        `json:"reviewCycles"`
		MaxReviews    int        `json:"maxReviews"`
		DispatchedAt  *time.Time `json:"dispatchedAt,omitempty"`
		CompletedAt   *time.Time `json:"completedAt,omitempty"`
		ResultSummary string     `json:"resultSummary,omitempty"`
	}

	result := taskStateView{
		ID:           t.ID,
		Status:       t.Status,
		Role:         t.Role,
		WorkerID:     t.WorkerID,
		ReviewerID:   t.ReviewerID,
		PlanID:       t.PlanID,
		PipelineID:   t.PipelineID,
		ReviewCycles: t.ReviewCycles,
		MaxReviews:   t.MaxReviews,
		DispatchedAt: t.DispatchedAt,
		CompletedAt:  t.CompletedAt,
	}
	if t.Result != nil {
		result.ResultSummary = t.Result.Summary
	}
	return result, nil
}

func compactPromptPreview(prompt string, maxLen int) (string, bool) {
	return compactTextPreview(prompt, maxLen)
}

func compactTextPreview(text string, maxLen int) (string, bool) {
	if maxLen <= 0 || len(text) <= maxLen {
		return text, false
	}
	if maxLen <= 3 {
		return text[:maxLen], true
	}
	return text[:maxLen-3] + "...", true
}

func handleGetTaskOutput(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" {
		return nil, fmt.Errorf("missing required field: taskId")
	}

	// Verify the task exists.
	if _, ok := pm.Task(p.TaskID); !ok {
		return nil, fmt.Errorf("task %q not found", p.TaskID)
	}

	output, err := pm.ReadTaskOutput(p.TaskID)
	if err != nil {
		return nil, fmt.Errorf("no output stored for task %q: %w", p.TaskID, err)
	}
	return map[string]any{"taskId": p.TaskID, "output": output}, nil
}

func handleSubmitPipeline(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		Goal   string       `json:"goal"`
		Stages []pool.Stage `json:"stages"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Goal == "" {
		return nil, fmt.Errorf("missing required field: goal")
	}
	if len(p.Stages) == 0 {
		return nil, fmt.Errorf("missing required field: stages")
	}

	pid, err := pm.SubmitPipeline(pool.Pipeline{
		Goal:   p.Goal,
		Stages: p.Stages,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"pipelineId": pid}, nil
}

func handleCancelPipeline(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		PipelineID string `json:"pipelineId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PipelineID == "" {
		return nil, fmt.Errorf("missing required field: pipelineId")
	}
	if err := pm.CancelPipeline(p.PipelineID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "pipelineId": p.PipelineID}, nil
}

func handleDispatchReview(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID     string `json:"taskId"`
		ReviewerID string `json:"reviewerId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" {
		return nil, fmt.Errorf("missing required field: taskId")
	}

	// Auto-pick reviewer when not specified.
	reviewerID := p.ReviewerID
	if reviewerID == "" {
		rid, err := pm.PickReviewer(p.TaskID)
		if err != nil {
			return nil, err
		}
		reviewerID = rid
	}

	if err := pm.DispatchReview(p.TaskID, reviewerID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "taskId": p.TaskID, "reviewerId": reviewerID}, nil
}

func handleReportReview(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID   string `json:"taskId"`
		Verdict  string `json:"verdict"`
		Feedback string `json:"feedback"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" || p.Verdict == "" {
		return nil, fmt.Errorf("missing required fields: taskId, verdict")
	}
	if p.Verdict != "pass" && p.Verdict != "fail" {
		return nil, fmt.Errorf("invalid verdict %q: must be \"pass\" or \"fail\"", p.Verdict)
	}
	if err := pm.ReportReview(p.TaskID, p.Verdict, p.Feedback, p.Severity); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "taskId": p.TaskID, "verdict": p.Verdict}, nil
}

func handleAnswerQuestion(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		QuestionID string `json:"questionId"`
		Answer     string `json:"answer"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.QuestionID == "" || p.Answer == "" {
		return nil, fmt.Errorf("missing required fields: questionId, answer")
	}
	if err := pm.AnswerQuestion(p.QuestionID, p.Answer, "leader"); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "questionId": p.QuestionID}, nil
}

func handleResolveEscalation(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID      string `json:"taskId"`
		Action      string `json:"action"`
		ExtraCycles int    `json:"extraCycles"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" || p.Action == "" {
		return nil, fmt.Errorf("missing required fields: taskId, action")
	}
	if p.Action != "accept" && p.Action != "retry" && p.Action != "abort" {
		return nil, fmt.Errorf("invalid action %q: must be \"accept\", \"retry\", or \"abort\"", p.Action)
	}
	if p.ExtraCycles == 0 {
		p.ExtraCycles = 1
	}
	if err := pm.ResolveEscalation(p.TaskID, p.Action, p.ExtraCycles); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "taskId": p.TaskID, "action": p.Action}, nil
}

func handlePendingQuestions(pm *pool.PoolManager, _ json.RawMessage) (any, error) {
	qs := pm.PendingQuestions()
	return map[string]any{"questions": qs, "count": len(qs)}, nil
}

func handleWaitForTask(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		TaskID     string `json:"taskId"`
		TimeoutSec int    `json:"timeoutSec"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" {
		return nil, fmt.Errorf("missing required field: taskId")
	}
	if p.TimeoutSec <= 0 {
		p.TimeoutSec = 300
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.TimeoutSec)*time.Second)
	defer cancel()

	t, err := pm.WaitForTask(ctx, p.TaskID)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// --- Plan tool handlers ---

func handleCreatePlan(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Title == "" || p.Content == "" {
		return nil, fmt.Errorf("missing required fields: title, content")
	}
	ps := pm.PlanStore()
	if ps == nil {
		return nil, fmt.Errorf("plans not configured")
	}
	id, err := ps.CreatePlan(p.Title, p.Content)
	if err != nil {
		return nil, err
	}
	return map[string]any{"planId": id}, nil
}

func handleListPlans(pm *pool.PoolManager, _ json.RawMessage) (any, error) {
	ps := pm.PlanStore()
	if ps == nil {
		return nil, fmt.Errorf("plans not configured")
	}
	plans, err := ps.ListPlans()
	if err != nil {
		return nil, err
	}
	return map[string]any{"plans": plans}, nil
}

func handleReadPlan(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		PlanID string `json:"planId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PlanID == "" {
		return nil, fmt.Errorf("missing required field: planId")
	}
	ps := pm.PlanStore()
	if ps == nil {
		return nil, fmt.Errorf("plans not configured")
	}
	content, err := ps.ReadPlan(p.PlanID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"planId": p.PlanID, "content": content}, nil
}

func handleClaimPlan(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		PlanID string `json:"planId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PlanID == "" {
		return nil, fmt.Errorf("missing required field: planId")
	}
	ps := pm.PlanStore()
	if ps == nil {
		return nil, fmt.Errorf("plans not configured")
	}
	sessionID := os.Getenv("MITTENS_SESSION_ID")
	if err := ps.ClaimPlan(p.PlanID, sessionID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "planId": p.PlanID}, nil
}

func handleUpdatePlanProgress(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		PlanID string `json:"planId"`
		Entry  string `json:"entry"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PlanID == "" || p.Entry == "" {
		return nil, fmt.Errorf("missing required fields: planId, entry")
	}
	ps := pm.PlanStore()
	if ps == nil {
		return nil, fmt.Errorf("plans not configured")
	}
	if err := ps.UpdateProgress(p.PlanID, p.Entry); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func handleCompletePlan(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		PlanID string `json:"planId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PlanID == "" {
		return nil, fmt.Errorf("missing required field: planId")
	}
	ps := pm.PlanStore()
	if ps == nil {
		return nil, fmt.Errorf("plans not configured")
	}
	if err := ps.CompletePlan(p.PlanID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "planId": p.PlanID}, nil
}

func handleCheckSession(pm *pool.PoolManager, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("missing required field: sessionId")
	}
	alive, err := pm.CheckSession(context.Background(), p.SessionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"alive": alive, "sessionId": p.SessionID}, nil
}

// --- JSON Schema helpers ---

type schemaPart func(map[string]any)

func obj(parts ...schemaPart) map[string]any {
	s := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
	for _, p := range parts {
		p(s)
	}
	return s
}

func prop(name, typ, desc string) schemaPart {
	return func(s map[string]any) {
		props := s["properties"].(map[string]any)
		props[name] = map[string]any{"type": typ, "description": desc}
	}
}

func propArray(name, desc string, items map[string]any) schemaPart {
	return func(s map[string]any) {
		props := s["properties"].(map[string]any)
		props[name] = map[string]any{"type": "array", "description": desc, "items": items}
	}
}

func propObj(name, typ, desc string, items map[string]any) schemaPart {
	return func(s map[string]any) {
		props := s["properties"].(map[string]any)
		props[name] = map[string]any{"type": typ, "description": desc, "items": items}
	}
}

func required(names ...string) schemaPart {
	return func(s map[string]any) {
		s["required"] = names
	}
}
