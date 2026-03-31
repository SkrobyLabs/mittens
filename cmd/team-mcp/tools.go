package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/SkrobyLabs/mittens/internal/adapter"
	"github.com/SkrobyLabs/mittens/internal/pool"
)

// activeBroker is set by main after the WorkerBroker is created, so that
// the spawn_worker handler can register per-worker tokens.
var activeBroker *WorkerBroker

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
			prop("dependsOn", "array", "Task IDs this task depends on"),
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
		Name:        "get_task_result",
		Description: "Get details and result of a specific task. For completed tasks, full worker output is available via get_task_output.",
		Schema: obj(
			required("taskId"),
			prop("taskId", "string", "ID of the task"),
		),
		Handler: handleGetTaskResult,
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
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Stage name"},
					"role": map[string]any{"type": "string", "description": "Worker role for this stage"},
					"fan":  map[string]any{"type": "string", "enum": []any{"fan-out", "fan-in", "streaming"}, "description": "Fan mode"},
					"tasks": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
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
	// spec.Environment, and persists it via WAL.
	w, err := pm.SpawnWorker(spec)
	if err != nil {
		return nil, err
	}

	// Register the token→workerID mapping so the broker can validate identity.
	if activeBroker != nil && w.Token != "" {
		activeBroker.RegisterWorkerToken(w.ID, w.Token)
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
	Workers          []pool.Worker      `json:"workers"`
	AliveCount       int                `json:"aliveWorkers"`
	Tasks            []pool.TaskSummary `json:"tasks"`
	QueuedCount      int                `json:"queuedTasks"`
	Pipelines        []pool.Pipeline    `json:"pipelines,omitempty"`
	PendingQuestions int                `json:"pendingQuestions"`
}

func handleGetStatus(pm *pool.PoolManager, _ json.RawMessage) (any, error) {
	summaries := pm.TaskSummaries()

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
		Workers:          pm.Workers(),
		AliveCount:       pm.AliveWorkers(),
		Tasks:            summaries,
		QueuedCount:      len(pm.QueuedTasks()),
		Pipelines:        pipes,
		PendingQuestions: len(pm.PendingQuestions()),
	}, nil
}

func handleGetTaskResult(pm *pool.PoolManager, params json.RawMessage) (any, error) {
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

	type taskWithHint struct {
		*pool.Task
		OutputHint string `json:"_outputHint,omitempty"`
	}
	result := taskWithHint{Task: t}
	if t.Result != nil && t.Result.Summary != "" {
		result.OutputHint = "Full worker output available via get_task_output tool"
	}
	return result, nil
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
		"type":       "object",
		"properties": map[string]any{},
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
