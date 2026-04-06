package main

import (
	"fmt"
	"context"
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
	PlanID               string              `json:"planId"`
	Lineage              string              `json:"lineage,omitempty"`
	Title                string              `json:"title,omitempty"`
	State                string              `json:"state,omitempty"`
	Phase                string              `json:"phase,omitempty"`
	ReviewRequested      bool                `json:"reviewRequested,omitempty"`
	ReviewStatus         string              `json:"reviewStatus,omitempty"`
	ReviewRounds         int                 `json:"reviewRounds,omitempty"`
	ReviewAttempts       int                 `json:"reviewAttempts,omitempty"`
	ReviewRevisions      int                 `json:"reviewRevisions,omitempty"`
	MaxReviewRevisions   int                 `json:"maxReviewRevisions,omitempty"`
	ImplReviewRequested  bool                `json:"implReviewRequested,omitempty"`
	ImplReviewStatus     string              `json:"implReviewStatus,omitempty"`
	ImplReviewFindings   []string            `json:"implReviewFindings,omitempty"`
	PendingQuestions     int                 `json:"pendingQuestions"`
	PendingQuestionIDs   []string            `json:"pendingQuestionIds,omitempty"`
	ActiveTaskIDs        []string            `json:"activeTaskIds,omitempty"`
	CompletedTaskIDs     []string            `json:"completedTaskIds,omitempty"`
	FailedTaskIDs        []string            `json:"failedTaskIds,omitempty"`
	Cycles               []PlanCycleProgress `json:"cycles,omitempty"`
	History              []PlanHistoryEntry  `json:"history,omitempty"`
	HistoryTotal         int                 `json:"historyTotal,omitempty"`
	HistoryIncluded      int                 `json:"historyIncluded,omitempty"`
	HistoryTruncated     bool                `json:"historyTruncated,omitempty"`
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
		case planStateCompleted, planStateMerged, planStateClosed, planStateRejected:
			continue
		}
		detail, err := k.PlanDetail(plan.PlanID)
		if err != nil {
			return nil, err
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
		PlanID:              planID,
		Lineage:             bundle.Plan.Lineage,
		Title:               bundle.Plan.Title,
		State:               bundle.Execution.State,
		ReviewRequested:     bundle.Execution.ReviewRequested,
		ReviewStatus:        bundle.Execution.ReviewStatus,
		ReviewRounds:        bundle.Execution.ReviewRounds,
		ReviewAttempts:      bundle.Execution.ReviewAttempts,
		ReviewRevisions:     bundle.Execution.ReviewRevisions,
		MaxReviewRevisions:  bundle.Execution.MaxReviewRevisions,
		ImplReviewRequested: bundle.Execution.ImplReviewRequested,
		ImplReviewStatus:    bundle.Execution.ImplReviewStatus,
		ImplReviewFindings:  append([]string(nil), bundle.Execution.ImplReviewFindings...),
		PendingQuestions:    len(pendingQuestionIDs),
		PendingQuestionIDs:  pendingQuestionIDs,
		ActiveTaskIDs:       append([]string(nil), bundle.Execution.ActiveTaskIDs...),
		CompletedTaskIDs:    append([]string(nil), bundle.Execution.CompletedTaskIDs...),
		FailedTaskIDs:       append([]string(nil), bundle.Execution.FailedTaskIDs...),
	}
	sort.Strings(progress.ActiveTaskIDs)
	sort.Strings(progress.CompletedTaskIDs)
	sort.Strings(progress.FailedTaskIDs)
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

	cycleCount := 1
	if bundle.Execution.ReviewRevisions+1 > cycleCount {
		cycleCount = bundle.Execution.ReviewRevisions + 1
	}
	if bundle.Execution.ReviewAttempts > cycleCount {
		cycleCount = bundle.Execution.ReviewAttempts
	}
	if bundle.Execution.State == planStateReviewing && bundle.Execution.ReviewAttempts+1 > cycleCount {
		cycleCount = bundle.Execution.ReviewAttempts + 1
	}

	cycles := make([]PlanCycleProgress, 0, cycleCount)
	for i := 1; i <= cycleCount; i++ {
		plannerTaskID := initialPlannerRuntimeID(planID)
		if i > 1 {
			plannerTaskID = planRevisionRuntimeID(planID, i-1)
		}
		cycle := PlanCycleProgress{
			Index:            i,
			PlannerTaskID:    plannerTaskID,
			PlannerTaskState: string(tasks[plannerTaskID]),
		}

		if bundle.Execution.ReviewRequested && (i <= bundle.Execution.ReviewAttempts || (bundle.Execution.State == planStateReviewing && i == bundle.Execution.ReviewAttempts+1)) {
			reviewTaskID := planReviewRuntimeID(planID, i)
			cycle.ReviewTaskID = reviewTaskID
			cycle.ReviewTaskState = string(tasks[reviewTaskID])
		}
		cycles = append(cycles, cycle)
	}

	implAttempts := bundle.Execution.ImplReviewAttempts
	if bundle.Execution.State == planStateImplementationReview && implAttempts < 1 {
		implAttempts = 1
	}
	for i := 1; i <= implAttempts; i++ {
		irTaskID := planImplReviewRuntimeID(planID, i)
		cycles = append(cycles, PlanCycleProgress{
			ImplReviewTaskID:    irTaskID,
			ImplReviewTaskState: string(tasks[irTaskID]),
		})
	}

	return cycles
}

type poolTaskState string

func initialPlannerRuntimeID(planID string) string {
	return planTaskRuntimeID(planID, plannerTaskID)
}

func planPhase(bundle StoredPlan, pendingQuestions int) string {
	switch bundle.Execution.State {
	case planStatePlanning:
		if hasActivePlanRevision(bundle) || bundle.Execution.ReviewRevisions > 0 {
			return "revising"
		}
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

func hasActivePlanRevision(bundle StoredPlan) bool {
	prefix := planTaskRuntimeID(bundle.Plan.PlanID, planRevisionTaskID+"-")
	for _, taskID := range bundle.Execution.ActiveTaskIDs {
		if strings.HasPrefix(taskID, prefix) {
			return true
		}
	}
	return false
}
