package pool

import (
	"context"
	"strings"
	"time"
)

// Worker status constants.
const (
	WorkerSpawning = "spawning"
	WorkerIdle     = "idle"
	WorkerWorking  = "working"
	WorkerBlocked  = "blocked"
	WorkerDead     = "dead"
)

// Task status constants.
const (
	TaskQueued     = "queued"
	TaskDispatched = "dispatched"
	TaskCompleted  = "completed"
	TaskFailed     = "failed"
	TaskCanceled   = "canceled"
	TaskReviewing  = "reviewing"
	TaskAccepted   = "accepted"
	TaskRejected   = "rejected"
	TaskEscalated  = "escalated"
)

// Pipeline status constants.
const (
	PipelineRunning   = "running"
	PipelineCompleted = "completed"
	PipelineFailed    = "failed"
	PipelineBlocked   = "blocked"
)

// Plan status constants.
const (
	PlanPending   = "pending"
	PlanActive    = "active"
	PlanCompleted = "completed"
	PlanOrphaned  = "orphaned"
)

// Review verdict constants.
const (
	ReviewPass = "pass"
	ReviewFail = "fail"
)

// Review severity constants.
const (
	SeverityMinor    = "minor"
	SeverityMajor    = "major"
	SeverityCritical = "critical"
)

// Escalation action constants.
const (
	EscalationAccept = "accept"
	EscalationRetry  = "retry"
	EscalationAbort  = "abort"
)

// FanMode describes how tasks within a pipeline stage relate.
type FanMode string

const (
	FanOut    FanMode = "fan-out"
	FanIn     FanMode = "fan-in"
	Streaming FanMode = "streaming"
)

// Worker represents a container running an AI agent.
type Worker struct {
	ID              string          `json:"id"`
	ContainerID     string          `json:"containerId,omitempty"`
	ContainerName   string          `json:"containerName,omitempty"`
	Provider        string          `json:"provider,omitempty"`
	Role            string          `json:"role,omitempty"`
	Token           string          `json:"-"` // per-worker auth token (never serialised to clients)
	Status          string          `json:"status"`
	CurrentTaskID   string          `json:"currentTaskId,omitempty"`
	CurrentActivity *WorkerActivity `json:"currentActivity,omitempty"`
	CurrentTool     string          `json:"currentTool,omitempty"`
	LastHeartbeat   time.Time       `json:"lastHeartbeat,omitempty"`
	SpawnedAt       time.Time       `json:"spawnedAt"`
}

// WorkerActivity is the normalized live activity snapshot exposed by a worker.
// It stores only the latest activity to keep heartbeats and status polling cheap.
type WorkerActivity struct {
	Kind    string `json:"kind,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Assignment is a runtime-directed unit of work sent to a specific worker.
type Assignment struct {
	ID      string         `json:"assignmentId"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// AssignmentSummary is the lightweight worker-status view of an assignment.
type AssignmentSummary struct {
	ID   string `json:"assignmentId"`
	Type string `json:"type"`
}

// Task represents a unit of work dispatched to a worker.
type Task struct {
	ID                 string         `json:"id"`
	PipelineID         string         `json:"pipelineId,omitempty"`
	PlanID             string         `json:"planId,omitempty"`
	StageIndex         int            `json:"stageIndex,omitempty"`
	Prompt             string         `json:"prompt"`
	Complexity         string         `json:"complexity,omitempty"`
	Role               string         `json:"role,omitempty"`
	Priority           int            `json:"priority"`
	DependsOn          []string       `json:"dependsOn,omitempty"`
	TimeoutMinutes     int            `json:"timeoutMinutes,omitempty"`
	Status             string         `json:"status"`
	WorkerID           string         `json:"workerId,omitempty"`
	ReviewerID         string         `json:"reviewerId,omitempty"`
	RetryCount         int            `json:"retryCount,omitempty"`
	RequireFreshWorker bool           `json:"requireFreshWorker,omitempty"`
	ReviewCycles       int            `json:"reviewCycles"`
	MaxReviews         int            `json:"maxReviews"`
	Result             *TaskResult    `json:"result,omitempty"`
	Handover           *TaskHandover  `json:"handover,omitempty"`
	Reviews            []ReviewRecord `json:"reviews,omitempty"`
	CreatedAt          time.Time      `json:"createdAt"`
	DispatchedAt       *time.Time     `json:"dispatchedAt,omitempty"`
	CompletedAt        *time.Time     `json:"completedAt,omitempty"`
}

// TaskSummary is a lightweight projection of Task for status views.
// It omits the full prompt, result output, git diff, handover, and review records.
type TaskSummary struct {
	ID           string     `json:"id"`
	Role         string     `json:"role,omitempty"`
	Priority     int        `json:"priority"`
	Status       string     `json:"status"`
	WorkerID     string     `json:"workerId,omitempty"`
	RetryCount   int        `json:"retryCount,omitempty"`
	DependsOn    []string   `json:"dependsOn,omitempty"`
	ReviewCycles int        `json:"reviewCycles"`
	HasHandover  bool       `json:"hasHandover,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	DispatchedAt *time.Time `json:"dispatchedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

// Summary returns a TaskSummary projection of the task.
func (t *Task) Summary() TaskSummary {
	return TaskSummary{
		ID:           t.ID,
		Role:         t.Role,
		Priority:     t.Priority,
		Status:       t.Status,
		WorkerID:     t.WorkerID,
		RetryCount:   t.RetryCount,
		DependsOn:    t.DependsOn,
		ReviewCycles: t.ReviewCycles,
		HasHandover:  t.Handover != nil,
		CreatedAt:    t.CreatedAt,
		DispatchedAt: t.DispatchedAt,
		CompletedAt:  t.CompletedAt,
	}
}

// TaskSpec is the input for creating a new task.
type TaskSpec struct {
	ID             string   `json:"id,omitempty"`
	PipelineID     string   `json:"pipelineId,omitempty"`
	PlanID         string   `json:"planId,omitempty"`
	StageIndex     int      `json:"stageIndex,omitempty"`
	Prompt         string   `json:"prompt"`
	Complexity     string   `json:"complexity,omitempty"`
	Role           string   `json:"role,omitempty"`
	Priority       int      `json:"priority"`
	DependsOn      []string `json:"dependsOn,omitempty"`
	TimeoutMinutes int      `json:"timeoutMinutes,omitempty"`
	MaxReviews     int      `json:"maxReviews,omitempty"`
}

// WorkerSpec is the input for spawning a new worker.
type WorkerSpec struct {
	ID            string            `json:"id,omitempty"`
	Role          string            `json:"role,omitempty"`
	Adapter       string            `json:"adapter,omitempty"`
	Model         string            `json:"model,omitempty"`
	Provider      string            `json:"provider,omitempty"`
	Memory        string            `json:"memory,omitempty"`
	CPUs          string            `json:"cpus,omitempty"`
	WorkspacePath string            `json:"workspacePath,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
}

// TaskResult contains the outcome of a completed or failed task.
type TaskResult struct {
	TaskID       string   `json:"taskId"`
	WorkerID     string   `json:"workerId"`
	State        string   `json:"state"`
	Summary      string   `json:"summary,omitempty"`
	FilesChanged []string `json:"filesChanged,omitempty"`
	GitDiff      string   `json:"gitDiff,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// TaskHandover carries structured context from one task to the next.
type TaskHandover struct {
	TaskID         string       `json:"taskId"`
	Summary        string       `json:"summary"`
	KeyDecisions   []string     `json:"keyDecisions,omitempty"`
	FilesChanged   []FileChange `json:"filesChanged,omitempty"`
	OpenQuestions  []string     `json:"openQuestions,omitempty"`
	ContextForNext string       `json:"contextForNext,omitempty"`
}

// FileChange records a single file modification within a task.
type FileChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	What   string `json:"what"`
}

// ReviewRecord stores the result of one review cycle.
type ReviewRecord struct {
	ReviewerID string    `json:"reviewerId"`
	Verdict    string    `json:"verdict"`
	Feedback   string    `json:"feedback,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	ReviewedAt time.Time `json:"reviewedAt"`
}

// ModelConfig describes the AI provider settings for a worker role.
type ModelConfig struct {
	Provider string            `json:"provider" yaml:"provider"`
	Model    string            `json:"model" yaml:"model"`
	APIKey   string            `json:"apiKey,omitempty" yaml:"apiKey,omitempty"`
	Adapter  string            `json:"adapter,omitempty" yaml:"adapter,omitempty"`
	Flags    map[string]string `json:"flags,omitempty" yaml:"flags,omitempty"`
}

// Pipeline defines a multi-stage execution plan.
type Pipeline struct {
	ID          string     `json:"id"`
	Goal        string     `json:"goal"`
	Stages      []Stage    `json:"stages"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// Stage is a single phase within a pipeline.
type Stage struct {
	Name        string      `json:"name"`
	Role        string      `json:"role,omitempty"`
	Fan         FanMode     `json:"fan,omitempty"`
	Tasks       []StageTask `json:"tasks,omitempty"`
	DependsOn   []string    `json:"dependsOn,omitempty"`
	AutoAdvance bool        `json:"autoAdvance,omitempty"`
}

// StageTask is a task template within a pipeline stage.
type StageTask struct {
	ID         string   `json:"id"`
	PromptTmpl string   `json:"promptTmpl,omitempty"`
	DependsOn  []string `json:"dependsOn,omitempty"`
}

// Question represents a question from a worker needing an answer.
type Question struct {
	ID         string    `json:"id"`
	WorkerID   string    `json:"workerId"`
	TaskID     string    `json:"taskId,omitempty"`
	Question   string    `json:"question"`
	Category   string    `json:"category,omitempty"`
	Options    []string  `json:"options,omitempty"`
	Blocking   bool      `json:"blocking"`
	Context    string    `json:"context,omitempty"`
	Answer     string    `json:"answer,omitempty"`
	Answered   bool      `json:"answered"`
	AnsweredBy string    `json:"answeredBy,omitempty"`
	AskedAt    time.Time `json:"askedAt"`
	AnsweredAt time.Time `json:"answeredAt,omitempty"`
}

// Notification is sent through the notify channel to the leader.
type Notification struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
}

// Plan represents a persistent execution plan.
type Plan struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Owner       string    `json:"owner,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	ClaimedAt   time.Time `json:"claimedAt,omitempty"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
	TaskIDs     []string  `json:"taskIds,omitempty"`
}

// PoolConfig holds configuration for a PoolManager instance.
type PoolConfig struct {
	SessionID  string                                   `json:"sessionId"`
	MaxWorkers int                                      `json:"maxWorkers"`
	StateDir   string                                   `json:"stateDir"`
	Router     interface{ Resolve(string) ModelConfig } `json:"-"`
	PlanStore  *PlanStore                               `json:"-"`
}

// ContainerInfo describes a session container discovered via Docker.
type ContainerInfo struct {
	ContainerID string `json:"containerId"`
	WorkerID    string `json:"workerId"`
	State       string `json:"state,omitempty"`
	Status      string `json:"status"`
}

// RuntimeWorker is the runtime-side worker view exposed by the Mittens daemon.
type RuntimeWorker struct {
	ID                string             `json:"id"`
	ContainerID       string             `json:"containerId,omitempty"`
	Status            string             `json:"status"`
	Provider          string             `json:"provider,omitempty"`
	Model             string             `json:"model,omitempty"`
	Adapter           string             `json:"adapter,omitempty"`
	Role              string             `json:"role,omitempty"`
	WorkspacePath     string             `json:"workspacePath,omitempty"`
	CurrentAssignment *AssignmentSummary `json:"currentAssignment,omitempty"`
	CurrentActivity   *WorkerActivity    `json:"currentActivity,omitempty"`
}

// RuntimeEvent is one event emitted by the Mittens daemon event stream.
type RuntimeEvent struct {
	Type         string    `json:"type"`
	WorkerID     string    `json:"workerId,omitempty"`
	AssignmentID string    `json:"assignmentId,omitempty"`
	Message      string    `json:"message,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

// IsRunning reports whether the container still counts as live for recovery.
// Recovery now inspects `docker ps -a`, so only containers that are actually
// running should keep a worker alive.
func (c ContainerInfo) IsRunning() bool {
	switch strings.ToLower(strings.TrimSpace(c.State)) {
	case "running":
		return true
	default:
		if c.State != "" {
			return false
		}
		status := strings.ToLower(strings.TrimSpace(c.Status))
		return strings.HasPrefix(status, "up ") && !strings.Contains(status, "paused")
	}
}

// HostAPI defines the interface for container lifecycle operations.
type HostAPI interface {
	SpawnWorker(ctx context.Context, spec WorkerSpec) (containerName, containerID string, err error)
	KillWorker(ctx context.Context, workerID string) error
	ListContainers(ctx context.Context, sessionID string) ([]ContainerInfo, error)
}

// RuntimeAPI extends HostAPI with runtime lifecycle and telemetry operations.
type RuntimeAPI interface {
	HostAPI
	RecycleWorker(ctx context.Context, workerID string) error
	GetWorkerActivity(ctx context.Context, workerID string) (*WorkerActivity, error)
	GetWorkerTranscript(ctx context.Context, workerID string) ([]WorkerActivityRecord, error)
	SubscribeEvents(ctx context.Context) (<-chan RuntimeEvent, error)
	SubmitAssignment(ctx context.Context, workerID string, assignment Assignment) error
}
