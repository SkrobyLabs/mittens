package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const defaultPlanProgressHistoryLimit = 8

type PlanCycleProgress struct {
	Index               int    `json:"index"`
	PlannerTaskID       string `json:"plannerTaskId,omitempty"`
	PlannerTaskState    string `json:"plannerTaskState,omitempty"`
	ReviewTaskID        string `json:"reviewTaskId,omitempty"`
	ReviewTaskState     string `json:"reviewTaskState,omitempty"`
	ImplReviewTaskID    string `json:"implReviewTaskId,omitempty"`
	ImplReviewTaskState string `json:"implReviewTaskState,omitempty"`
}

type PlanProgress struct {
	PlanID                      string              `json:"planId"`
	Lineage                     string              `json:"lineage,omitempty"`
	Title                       string              `json:"title,omitempty"`
	State                       string              `json:"state,omitempty"`
	Phase                       string              `json:"phase,omitempty"`
	ImplReviewRequested         bool                `json:"implReviewRequested,omitempty"`
	ImplReviewStatus            string              `json:"implReviewStatus,omitempty"`
	ImplReviewFindings          []string            `json:"implReviewFindings,omitempty"`
	ReviewCouncilTurnsCompleted int                 `json:"reviewCouncilTurnsCompleted,omitempty"`
	ReviewCouncilMaxTurns       int                 `json:"reviewCouncilMaxTurns,omitempty"`
	ReviewCouncilFinalDecision  string              `json:"reviewCouncilFinalDecision,omitempty"`
	ReviewCouncilActiveSeat     string              `json:"reviewCouncilActiveSeat,omitempty"`
	PendingQuestions            int                 `json:"pendingQuestions"`
	PendingQuestionIDs          []string            `json:"pendingQuestionIds,omitempty"`
	ActiveTaskIDs               []string            `json:"activeTaskIds,omitempty"`
	CompletedTaskIDs            []string            `json:"completedTaskIds,omitempty"`
	FailedTaskIDs               []string            `json:"failedTaskIds,omitempty"`
	Cycles                      []PlanCycleProgress `json:"cycles,omitempty"`
	History                     []PlanHistoryEntry  `json:"history,omitempty"`
	HistoryTotal                int                 `json:"historyTotal,omitempty"`
	HistoryIncluded             int                 `json:"historyIncluded,omitempty"`
	HistoryTruncated            bool                `json:"historyTruncated,omitempty"`
}

type PlanDetail struct {
	Plan      PlanRecord         `json:"plan"`
	Execution ExecutionRecord    `json:"execution"`
	Affinity  AffinityRecord     `json:"affinity"`
	Progress  PlanProgress       `json:"progress"`
	History   []PlanHistoryEntry `json:"history,omitempty"`
}

func (k *Kitchen) PlanDetail(planID string) (PlanDetail, error) {
	bundle, err := k.GetPlan(planID)
	if err != nil {
		return PlanDetail{}, err
	}
	progress, err := k.planProgress(bundle)
	if err != nil {
		return PlanDetail{}, err
	}
	return PlanDetail{
		Plan:      bundle.Plan,
		Execution: bundle.Execution,
		Affinity:  bundle.Affinity,
		Progress:  progress,
		History:   append([]PlanHistoryEntry(nil), bundle.Execution.History...),
	}, nil
}

func (k *Kitchen) TaskActivity(taskID string) ([]pool.WorkerActivityRecord, error) {
	if k == nil || k.pm == nil || k.hostAPI == nil {
		return nil, nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task ID must not be empty")
	}
	task, ok := k.pm.Task(taskID)
	if !ok {
		return nil, os.ErrNotExist
	}
	workerID := strings.TrimSpace(task.WorkerID)
	if workerID == "" && task.Result != nil {
		workerID = strings.TrimSpace(task.Result.WorkerID)
	}
	if workerID == "" {
		return nil, nil
	}
	transcript, err := k.hostAPI.GetWorkerTranscript(context.Background(), workerID)
	if err != nil {
		return nil, err
	}
	if len(transcript) == 0 {
		return nil, nil
	}
	filtered := make([]pool.WorkerActivityRecord, 0, len(transcript))
	for _, record := range transcript {
		if strings.TrimSpace(record.TaskID) == taskID {
			filtered = append(filtered, record)
		}
	}
	if len(filtered) > 0 {
		return filtered, nil
	}
	return append([]pool.WorkerActivityRecord(nil), transcript...), nil
}

func (k *Kitchen) TaskOutput(taskID string) (string, error) {
	if k == nil || k.pm == nil {
		return "", nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", fmt.Errorf("task ID must not be empty")
	}
	return k.pm.ReadTaskOutput(taskID)
}

func (k *Kitchen) OpenPlanProgress() ([]PlanProgress, error) {
	return k.OpenPlanProgressWithLimit(k.snapshotPlanHistoryLimit())
}

func (k *Kitchen) OpenPlanProgressWithLimit(historyLimit int) ([]PlanProgress, error) {
	if k == nil || k.planStore == nil {
		return nil, fmt.Errorf("kitchen plan store not configured")
	}

	plans, err := k.planStore.List()
	if err != nil {
		return nil, err
	}

	progress := make([]PlanProgress, 0, len(plans))
	for _, plan := range plans {
		switch plan.State {
		case planStateCompleted, planStateMerged, planStateClosed:
			continue
		}
		detail, err := k.PlanDetail(plan.PlanID)
		if err != nil {
			return nil, err
		}
		if detail.Plan.State == planStateRejected && !canExtendReviewCouncil(detail.Plan.State, detail.Execution) {
			continue
		}
		detail.Progress.History, detail.Progress.HistoryTotal, detail.Progress.HistoryIncluded, detail.Progress.HistoryTruncated = windowPlanProgressHistory(detail.History, historyLimit)
		progress = append(progress, detail.Progress)
	}
	sort.Slice(progress, func(i, j int) bool {
		if progress[i].State == progress[j].State {
			return progress[i].PlanID < progress[j].PlanID
		}
		return progress[i].State < progress[j].State
	})
	return progress, nil
}

func (k *Kitchen) planProgress(bundle StoredPlan) (PlanProgress, error) {
	planID := strings.TrimSpace(bundle.Plan.PlanID)
	if planID == "" {
		return PlanProgress{}, fmt.Errorf("plan ID must not be empty")
	}

	var pendingQuestionIDs []string
	for _, q := range pendingQuestionsForPlan(k.pm, planID) {
		pendingQuestionIDs = append(pendingQuestionIDs, q.ID)
	}
	sort.Strings(pendingQuestionIDs)

	progress := PlanProgress{
		PlanID:                      planID,
		Lineage:                     bundle.Plan.Lineage,
		Title:                       bundle.Plan.Title,
		State:                       bundle.Execution.State,
		ImplReviewRequested:         bundle.Execution.ImplReviewRequested,
		ImplReviewStatus:            bundle.Execution.ImplReviewStatus,
		ImplReviewFindings:          append([]string(nil), bundle.Execution.ImplReviewFindings...),
		ReviewCouncilTurnsCompleted: bundle.Execution.ReviewCouncilTurnsCompleted,
		ReviewCouncilMaxTurns:       bundle.Execution.ReviewCouncilMaxTurns,
		ReviewCouncilFinalDecision:  bundle.Execution.ReviewCouncilFinalDecision,
		PendingQuestions:            len(pendingQuestionIDs),
		PendingQuestionIDs:          pendingQuestionIDs,
		ActiveTaskIDs:               append([]string(nil), bundle.Execution.ActiveTaskIDs...),
		CompletedTaskIDs:            append([]string(nil), bundle.Execution.CompletedTaskIDs...),
		FailedTaskIDs:               append([]string(nil), bundle.Execution.FailedTaskIDs...),
	}
	sort.Strings(progress.ActiveTaskIDs)
	sort.Strings(progress.CompletedTaskIDs)
	sort.Strings(progress.FailedTaskIDs)
	if progress.ReviewCouncilFinalDecision == "" && progress.ReviewCouncilMaxTurns > 0 {
		progress.ReviewCouncilActiveSeat = reviewCouncilSeatForTurn(progress.ReviewCouncilTurnsCompleted + 1)
	}
	progress.Cycles = k.planCycles(bundle)
	progress.History = append([]PlanHistoryEntry(nil), bundle.Execution.History...)
	progress.HistoryTotal = len(progress.History)
	progress.HistoryIncluded = len(progress.History)
	progress.Phase = planPhase(bundle, progress.PendingQuestions)
	return progress, nil
}

func windowPlanProgressHistory(history []PlanHistoryEntry, limit int) ([]PlanHistoryEntry, int, int, bool) {
	total := len(history)
	if total == 0 {
		return nil, 0, 0, false
	}
	if limit == 0 {
		return nil, total, 0, true
	}
	if limit < 0 || total <= limit {
		copied := append([]PlanHistoryEntry(nil), history...)
		return copied, total, len(copied), false
	}
	copied := append([]PlanHistoryEntry(nil), history[total-limit:]...)
	return copied, total, len(copied), true
}

func (k *Kitchen) snapshotPlanHistoryLimit() int {
	if k == nil {
		return defaultPlanProgressHistoryLimit
	}
	if k.cfg.Snapshots.PlanHistoryLimit < 0 {
		return defaultPlanProgressHistoryLimit
	}
	return k.cfg.Snapshots.PlanHistoryLimit
}

func (k *Kitchen) resolveSnapshotHistoryLimit(requested int) (int, map[string]any) {
	configured := k.snapshotPlanHistoryLimit()
	applied := requested
	overridden := true
	if requested < 0 {
		applied = configured
		overridden = false
	}
	return applied, map[string]any{
		"planHistoryLimit":           applied,
		"configuredPlanHistoryLimit": configured,
		"historyLimitOverridden":     overridden,
	}
}

func (k *Kitchen) planCycles(bundle StoredPlan) []PlanCycleProgress {
	planID := strings.TrimSpace(bundle.Plan.PlanID)
	if planID == "" {
		return nil
	}

	tasks := make(map[string]poolTaskState)
	if k != nil && k.pm != nil {
		for _, task := range k.pm.Tasks() {
			if task.PlanID == planID {
				tasks[task.ID] = poolTaskState(task.Status)
			}
		}
	}

	cycleCount := max(1, bundle.Execution.CouncilTurnsCompleted)
	if bundle.Execution.State == planStatePlanning || bundle.Execution.State == planStateReviewing {
		nextTurn := bundle.Execution.CouncilTurnsCompleted + 1
		if nextTurn > cycleCount && nextTurn <= bundle.Execution.CouncilMaxTurns {
			cycleCount = nextTurn
		}
	}

	cycles := make([]PlanCycleProgress, 0, cycleCount)
	for i := 1; i <= cycleCount; i++ {
		plannerTaskID := councilTaskID(planID, i)
		cycle := PlanCycleProgress{
			Index:            i,
			PlannerTaskID:    plannerTaskID,
			PlannerTaskState: string(planProgressTaskState(bundle.Execution, plannerTaskID, tasks)),
		}
		cycles = append(cycles, cycle)
	}

	reviewTurns := maxObservedReviewCouncilTurn(bundle, tasks)
	if bundle.Execution.State == planStateImplementationReview && reviewTurns == 0 {
		reviewTurns = 1
	}
	for i := 1; i <= reviewTurns; i++ {
		irTaskID := reviewCouncilTaskID(planID, i)
		cycles = append(cycles, PlanCycleProgress{
			Index:               i,
			ImplReviewTaskID:    irTaskID,
			ImplReviewTaskState: string(planProgressTaskState(bundle.Execution, irTaskID, tasks)),
		})
	}

	return cycles
}

func maxObservedReviewCouncilTurn(bundle StoredPlan, tasks map[string]poolTaskState) int {
	planID := strings.TrimSpace(bundle.Plan.PlanID)
	maxTurn := bundle.Execution.ReviewCouncilTurnsCompleted
	for taskID := range tasks {
		if turn := reviewCouncilTurnNumberFromTaskID(planID, taskID); turn > maxTurn {
			maxTurn = turn
		}
	}
	for _, taskID := range bundle.Execution.ActiveTaskIDs {
		if turn := reviewCouncilTurnNumberFromTaskID(planID, taskID); turn > maxTurn {
			maxTurn = turn
		}
	}
	for _, taskID := range bundle.Execution.CompletedTaskIDs {
		if turn := reviewCouncilTurnNumberFromTaskID(planID, taskID); turn > maxTurn {
			maxTurn = turn
		}
	}
	for _, taskID := range bundle.Execution.FailedTaskIDs {
		if turn := reviewCouncilTurnNumberFromTaskID(planID, taskID); turn > maxTurn {
			maxTurn = turn
		}
	}
	for _, entry := range bundle.Execution.History {
		if turn := reviewCouncilTurnNumberFromTaskID(planID, entry.TaskID); turn > maxTurn {
			maxTurn = turn
		}
	}
	return maxTurn
}

func planProgressTaskState(exec ExecutionRecord, taskID string, tasks map[string]poolTaskState) poolTaskState {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ""
	}
	if state, ok := tasks[taskID]; ok && state != "" {
		return state
	}
	for _, id := range exec.ActiveTaskIDs {
		if strings.TrimSpace(id) == taskID {
			return poolTaskState(pool.TaskQueued)
		}
	}
	for _, id := range exec.CompletedTaskIDs {
		if strings.TrimSpace(id) == taskID {
			return poolTaskState(pool.TaskCompleted)
		}
	}
	for _, id := range exec.FailedTaskIDs {
		if strings.TrimSpace(id) == taskID {
			return poolTaskState(pool.TaskFailed)
		}
	}
	return ""
}

type poolTaskState string

func initialPlannerRuntimeID(planID string) string {
	return councilTaskID(planID, 1)
}

func planPhase(bundle StoredPlan, pendingQuestions int) string {
	switch bundle.Execution.State {
	case planStatePlanning:
		return "planning"
	case planStateReviewing:
		return "reviewing"
	case planStateImplementationReview:
		return "reviewing_implementation"
	case planStatePendingApproval:
		if pendingQuestions > 0 {
			return "awaiting_questions"
		}
		return "awaiting_approval"
	case planStateActive:
		return "executing"
	case "":
		return bundle.Plan.State
	default:
		return bundle.Execution.State
	}
}
