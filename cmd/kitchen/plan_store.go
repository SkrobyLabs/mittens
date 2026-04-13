package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
)

// ErrPlanNotFound is returned by PlanStore.Get when the plan directory or its
// core files have been removed on disk. Callers (typically the scheduler) can
// use this to distinguish genuinely missing plans from transient I/O errors
// and reap tasks that still reference the orphaned plan.
var ErrPlanNotFound = errors.New("plan not found")

const (
	planFileName      = "plan.json"
	executionFileName = "execution.json"
	affinityFileName  = "affinity.json"
)

type PlanAnchor struct {
	Commit    string    `json:"commit,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

type PlanOwnership struct {
	Packages  []string `json:"packages,omitempty"`
	Exclusive bool     `json:"exclusive,omitempty"`
}

type PlanOutputs struct {
	Files     []string `json:"files,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type PlanSuccessCriteria struct {
	Advisory   string   `json:"advisory,omitempty"`
	Verifiable []string `json:"verifiable,omitempty"`
}

type PlanDependency struct {
	Task     string   `json:"task"`
	Type     string   `json:"type,omitempty"`
	Consumes []string `json:"consumes,omitempty"`
}

func (d *PlanDependency) UnmarshalJSON(data []byte) error {
	var taskID string
	if err := json.Unmarshal(data, &taskID); err == nil {
		d.Task = strings.TrimSpace(taskID)
		d.Type = ""
		d.Consumes = nil
		return nil
	}

	type alias PlanDependency
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.Task = strings.TrimSpace(raw.Task)
	d.Type = strings.TrimSpace(raw.Type)
	d.Consumes = append([]string(nil), raw.Consumes...)
	return nil
}

type PlanTask struct {
	ID               string               `json:"id"`
	Title            string               `json:"title,omitempty"`
	Prompt           string               `json:"prompt"`
	Complexity       Complexity           `json:"complexity"`
	Dependencies     []PlanDependency     `json:"dependencies,omitempty"`
	Outputs          *PlanOutputs         `json:"outputs,omitempty"`
	SuccessCriteria  *PlanSuccessCriteria `json:"successCriteria,omitempty"`
	ReviewComplexity Complexity           `json:"reviewComplexity,omitempty"`
	TimeoutMinutes   int                  `json:"timeoutMinutes,omitempty"`
}

type PlanRecord struct {
	PlanID    string        `json:"planId"`
	PlannerID string        `json:"plannerId,omitempty"`
	Source    string        `json:"source,omitempty"`
	Anchor    PlanAnchor    `json:"anchor,omitempty"`
	Lineage   string        `json:"lineage"`
	Title     string        `json:"title"`
	Summary   string        `json:"summary,omitempty"`
	Ownership PlanOwnership `json:"ownership,omitempty"`
	Tasks     []PlanTask    `json:"tasks,omitempty"`
	DependsOn []string      `json:"dependsOn,omitempty"`
	State     string        `json:"state,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

type ExecutionRecord struct {
	PlanID                               string                        `json:"planId"`
	State                                string                        `json:"state"`
	AutoApproved                         bool                          `json:"autoApproved,omitempty"`
	Approved                             bool                          `json:"approved,omitempty"`
	History                              []PlanHistoryEntry            `json:"history,omitempty"`
	Branch                               string                        `json:"branch,omitempty"`
	Anchor                               PlanAnchor                    `json:"anchor,omitempty"`
	ActiveTaskIDs                        []string                      `json:"activeTaskIds,omitempty"`
	CompletedTaskIDs                     []string                      `json:"completedTaskIds,omitempty"`
	FailedTaskIDs                        []string                      `json:"failedTaskIds,omitempty"`
	CreatedAt                            time.Time                     `json:"createdAt"`
	UpdatedAt                            time.Time                     `json:"updatedAt"`
	ApprovedAt                           *time.Time                    `json:"approvedAt,omitempty"`
	ActivatedAt                          *time.Time                    `json:"activatedAt,omitempty"`
	CompletedAt                          *time.Time                    `json:"completedAt,omitempty"`
	ImplReviewRequested                  bool                          `json:"implReviewRequested,omitempty"`
	ImplReviewStatus                     string                        `json:"implReviewStatus,omitempty"`
	ImplReviewFindings                   []string                      `json:"implReviewFindings,omitempty"`
	ImplReviewedAt                       *time.Time                    `json:"implReviewedAt,omitempty"`
	CouncilMaxTurns                      int                           `json:"councilMaxTurns,omitempty"`
	CouncilTurnsCompleted                int                           `json:"councilTurnsCompleted,omitempty"`
	CouncilAwaitingAnswers               bool                          `json:"councilAwaitingAnswers,omitempty"`
	CouncilFinalDecision                 string                        `json:"councilFinalDecision,omitempty"`
	CouncilSeats                         [2]CouncilSeatRecord          `json:"councilSeats,omitempty"`
	CouncilTurns                         []CouncilTurnRecord           `json:"councilTurns,omitempty"`
	CouncilWarnings                      []adapter.CouncilDisagreement `json:"councilWarnings,omitempty"`
	CouncilUnresolvedDisagreements       []adapter.CouncilDisagreement `json:"councilUnresolvedDisagreements,omitempty"`
	ReviewCouncilMaxTurns                int                           `json:"reviewCouncilMaxTurns,omitempty"`
	ReviewCouncilTurnsCompleted          int                           `json:"reviewCouncilTurnsCompleted,omitempty"`
	ReviewCouncilAwaitingAnswers         bool                          `json:"reviewCouncilAwaitingAnswers,omitempty"`
	ReviewCouncilFinalDecision           string                        `json:"reviewCouncilFinalDecision,omitempty"`
	ReviewCouncilSeats                   [2]CouncilSeatRecord          `json:"reviewCouncilSeats,omitempty"`
	ReviewCouncilTurns                   []ReviewCouncilTurnRecord     `json:"reviewCouncilTurns,omitempty"`
	ReviewCouncilWarnings                []adapter.CouncilDisagreement `json:"reviewCouncilWarnings,omitempty"`
	ReviewCouncilUnresolvedDisagreements []adapter.CouncilDisagreement `json:"reviewCouncilUnresolvedDisagreements,omitempty"`
	RejectedBy                           string                        `json:"rejectedBy,omitempty"`
}

type CouncilSeatRecord struct {
	Seat         string     `json:"seat"`
	WorkerID     string     `json:"workerId,omitempty"`
	PoolKey      PoolKey    `json:"poolKey,omitempty"`
	IdleSince    *time.Time `json:"idleSince,omitempty"`
	Rehydrated   bool       `json:"rehydrated,omitempty"`
	RehydratedAt *time.Time `json:"rehydratedAt,omitempty"`
}

type CouncilTurnRecord struct {
	Seat       string                       `json:"seat"`
	Turn       int                          `json:"turn"`
	Artifact   *adapter.CouncilTurnArtifact `json:"artifact"`
	OccurredAt time.Time                    `json:"occurredAt"`
}

type ReviewCouncilTurnRecord struct {
	Seat       string                             `json:"seat"`
	Turn       int                                `json:"turn"`
	Artifact   *adapter.ReviewCouncilTurnArtifact `json:"artifact"`
	OccurredAt time.Time                          `json:"occurredAt"`
}

type AffinityRecord struct {
	PlanID             string     `json:"planId"`
	PlannerWorkerID    string     `json:"plannerWorkerId,omitempty"`
	PreferredProviders []PoolKey  `json:"preferredProviders,omitempty"`
	LastWorkerID       string     `json:"lastWorkerId,omitempty"`
	LastQuestionID     string     `json:"lastQuestionId,omitempty"`
	Invalidated        bool       `json:"invalidated,omitempty"`
	InvalidationReason string     `json:"invalidationReason,omitempty"`
	InvalidatedAt      *time.Time `json:"invalidatedAt,omitempty"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type StoredPlan struct {
	Plan      PlanRecord      `json:"plan"`
	Execution ExecutionRecord `json:"execution"`
	Affinity  AffinityRecord  `json:"affinity"`
}

type PlanStore struct {
	plansDir string
}

func NewPlanStore(plansDir string) *PlanStore {
	if strings.TrimSpace(plansDir) == "" {
		return nil
	}
	_ = os.MkdirAll(plansDir, 0755)
	return &PlanStore{plansDir: plansDir}
}

func dependencyTaskIDs(deps []PlanDependency) []string {
	ids := make([]string, 0, len(deps))
	for _, dep := range deps {
		taskID := strings.TrimSpace(dep.Task)
		if taskID == "" {
			continue
		}
		ids = append(ids, taskID)
	}
	return ids
}

func (ps *PlanStore) Create(bundle StoredPlan) (string, error) {
	if ps == nil {
		return "", fmt.Errorf("plan store not configured")
	}
	plan := bundle.Plan
	if strings.TrimSpace(plan.PlanID) == "" {
		plan.PlanID = generatePlanID(plan.Title)
	}
	if err := validatePathComponent("plan ID", plan.PlanID); err != nil {
		return "", err
	}
	if err := validatePathComponent("lineage", plan.Lineage); err != nil {
		return "", err
	}
	if strings.TrimSpace(plan.Title) == "" {
		return "", fmt.Errorf("plan title must not be empty")
	}

	now := time.Now().UTC()
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = now
	}
	plan.UpdatedAt = now
	if plan.State == "" {
		plan.State = "pending_approval"
	}

	execution := bundle.Execution
	execution.PlanID = plan.PlanID
	if execution.State == "" {
		execution.State = plan.State
	}
	if execution.CreatedAt.IsZero() {
		execution.CreatedAt = now
	}
	execution.UpdatedAt = now
	if execution.Anchor.Timestamp.IsZero() {
		execution.Anchor = plan.Anchor
	}

	affinity := bundle.Affinity
	affinity.PlanID = plan.PlanID
	affinity.UpdatedAt = now

	planDir := filepath.Join(ps.plansDir, plan.PlanID)
	if err := os.MkdirAll(planDir, 0755); err != nil {
		return "", fmt.Errorf("create plan dir: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(planDir, planFileName), plan); err != nil {
		return "", fmt.Errorf("write plan: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(planDir, executionFileName), execution); err != nil {
		return "", fmt.Errorf("write execution: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(planDir, affinityFileName), affinity); err != nil {
		return "", fmt.Errorf("write affinity: %w", err)
	}
	return plan.PlanID, nil
}

func (ps *PlanStore) Get(planID string) (StoredPlan, error) {
	if ps == nil {
		return StoredPlan{}, fmt.Errorf("plan store not configured")
	}
	if err := validatePathComponent("plan ID", planID); err != nil {
		return StoredPlan{}, err
	}
	planDir := filepath.Join(ps.plansDir, planID)
	var bundle StoredPlan
	if err := readJSONFile(filepath.Join(planDir, planFileName), &bundle.Plan); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoredPlan{}, fmt.Errorf("%w: %s", ErrPlanNotFound, planID)
		}
		return StoredPlan{}, err
	}
	if err := readJSONFile(filepath.Join(planDir, executionFileName), &bundle.Execution); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoredPlan{}, fmt.Errorf("%w: %s", ErrPlanNotFound, planID)
		}
		return StoredPlan{}, err
	}
	if err := readJSONFile(filepath.Join(planDir, affinityFileName), &bundle.Affinity); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoredPlan{}, fmt.Errorf("%w: %s", ErrPlanNotFound, planID)
		}
		return StoredPlan{}, err
	}
	return bundle, nil
}

func (ps *PlanStore) Delete(planID string) error {
	if ps == nil {
		return fmt.Errorf("plan store not configured")
	}
	if err := validatePathComponent("plan ID", planID); err != nil {
		return err
	}
	planDir := filepath.Join(ps.plansDir, planID)
	if err := os.RemoveAll(planDir); err != nil {
		return fmt.Errorf("delete plan dir: %w", err)
	}
	return nil
}

func (ps *PlanStore) List() ([]PlanRecord, error) {
	if ps == nil {
		return nil, fmt.Errorf("plan store not configured")
	}
	entries, err := os.ReadDir(ps.plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plans dir: %w", err)
	}

	var plans []PlanRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var plan PlanRecord
		if err := readJSONFile(filepath.Join(ps.plansDir, entry.Name(), planFileName), &plan); err != nil {
			continue
		}
		plans = append(plans, plan)
	}

	sort.Slice(plans, func(i, j int) bool {
		return plans[i].UpdatedAt.After(plans[j].UpdatedAt)
	})
	return plans, nil
}

func (ps *PlanStore) UpdatePlan(plan PlanRecord) error {
	if ps == nil {
		return fmt.Errorf("plan store not configured")
	}
	if err := validatePathComponent("plan ID", plan.PlanID); err != nil {
		return err
	}
	plan.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(ps.plansDir, plan.PlanID, planFileName), plan)
}

func (ps *PlanStore) UpdateExecution(planID string, execution ExecutionRecord) error {
	if ps == nil {
		return fmt.Errorf("plan store not configured")
	}
	if err := validatePathComponent("plan ID", planID); err != nil {
		return err
	}
	execution.PlanID = planID
	execution.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(ps.plansDir, planID, executionFileName), execution)
}

func (ps *PlanStore) AddTask(planID string, task PlanTask) error {
	if ps == nil {
		return fmt.Errorf("plan store not configured")
	}
	if err := validatePathComponent("plan ID", planID); err != nil {
		return err
	}
	bundle, err := ps.Get(planID)
	if err != nil {
		return err
	}
	bundle.Plan.Tasks = append(bundle.Plan.Tasks, task)
	bundle.Plan.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(ps.plansDir, planID, planFileName), bundle.Plan)
}

func (ps *PlanStore) UpdateAffinity(planID string, affinity AffinityRecord) error {
	if ps == nil {
		return fmt.Errorf("plan store not configured")
	}
	if err := validatePathComponent("plan ID", planID); err != nil {
		return err
	}
	affinity.PlanID = planID
	affinity.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(ps.plansDir, planID, affinityFileName), affinity)
}

func generatePlanID(seed string) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(seed + now))
	return "plan_" + hex.EncodeToString(sum[:])[:10]
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write temp %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}

func readJSONFile(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func validatePathComponent(kind, value string) error {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return fmt.Errorf("%s must not be empty", kind)
	case value == "." || value == "..":
		return fmt.Errorf("%s must not be %q", kind, value)
	case strings.Contains(value, "/") || strings.Contains(value, `\`):
		return fmt.Errorf("%s must not contain path separators", kind)
	default:
		return nil
	}
}
