package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	planHistoryPlanningStarted          = "planning_started"
	planHistoryPlanningCompleted        = "planning_completed"
	planHistoryPlanningFailed           = "planning_failed"
	planHistoryReviewRequested          = "review_requested"
	planHistoryReviewPassed             = "review_passed"
	planHistoryReviewFailed             = "review_failed"
	planHistoryConflictRetried          = "conflict_retried"
	planHistoryManualRetried            = "manual_retried"
	planHistoryConflictFixRequested     = "conflict_fix_requested"
	planHistoryLineageFixMergeRequested = "lineage_fix_merge_requested"
	planHistoryLineageFixMergeCompleted = "lineage_fix_merge_completed"
	planHistoryImplReviewRequested      = "impl_review_requested"
	planHistoryImplReviewPassed         = "impl_review_passed"
	planHistoryImplReviewFailed         = "impl_review_failed"
)

type PlanHistoryEntry struct {
	Type       string    `json:"type"`
	Cycle      int       `json:"cycle,omitempty"`
	TaskID     string    `json:"taskId,omitempty"`
	Verdict    string    `json:"verdict,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Findings   []string  `json:"findings,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

func appendPlanHistory(ex ExecutionRecord, entry PlanHistoryEntry) ExecutionRecord {
	entry.Type = strings.TrimSpace(entry.Type)
	entry.TaskID = strings.TrimSpace(entry.TaskID)
	entry.Verdict = strings.TrimSpace(entry.Verdict)
	entry.Summary = strings.TrimSpace(entry.Summary)
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now().UTC()
	}
	if entry.Cycle < 0 {
		entry.Cycle = 0
	}
	entry.Findings = append([]string(nil), entry.Findings...)
	ex.History = append(ex.History, entry)
	return ex
}

func plannerCycleForTask(planID, taskID string) int {
	taskID = strings.TrimSpace(taskID)
	if taskID == initialPlannerRuntimeID(planID) {
		return 1
	}
	prefix := planTaskRuntimeID(planID, planRevisionTaskID+"-")
	if rest, ok := strings.CutPrefix(taskID, prefix); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && n > 0 {
			return n + 1
		}
	}
	return 1
}

func reviewCycleForTask(planID, taskID string) int {
	prefix := planTaskRuntimeID(planID, planReviewTaskID+"-")
	if rest, ok := strings.CutPrefix(strings.TrimSpace(taskID), prefix); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func implReviewCycleForTask(planID, taskID string) int {
	prefix := planTaskRuntimeID(planID, implReviewTaskID+"-")
	if rest, ok := strings.CutPrefix(strings.TrimSpace(taskID), prefix); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func (k *Kitchen) PlanHistory(planID string, cycle int) ([]PlanHistoryEntry, error) {
	detail, err := k.PlanDetail(planID)
	if err != nil {
		return nil, err
	}
	history := append([]PlanHistoryEntry(nil), detail.History...)
	if cycle <= 0 {
		return history, nil
	}
	filtered := make([]PlanHistoryEntry, 0, len(history))
	for _, entry := range history {
		if entry.Cycle == cycle {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func writePlanHistory(out io.Writer, history []PlanHistoryEntry) error {
	for _, entry := range history {
		if _, err := fmt.Fprintf(out, "%d\t%s\t%s\t%s\n", entry.Cycle, entry.Type, entry.TaskID, summarizePlanHistoryEntry(entry)); err != nil {
			return err
		}
	}
	return nil
}

func summarizePlanHistoryEntry(entry PlanHistoryEntry) string {
	if summary := strings.TrimSpace(entry.Summary); summary != "" {
		return summary
	}
	if len(entry.Findings) > 0 {
		return strings.Join(entry.Findings, " | ")
	}
	if verdict := strings.TrimSpace(entry.Verdict); verdict != "" {
		return verdict
	}
	return "-"
}

func planHistoryEntryLabel(entryType string) string {
	switch strings.TrimSpace(entryType) {
	case planHistoryPlanningStarted:
		return "Planning started"
	case planHistoryPlanningCompleted:
		return "Planning completed"
	case planHistoryPlanningFailed:
		return "Planning failed"
	case planHistoryReviewRequested:
		return "Review requested"
	case planHistoryReviewPassed:
		return "Review passed"
	case planHistoryReviewFailed:
		return "Review failed"
	case planHistoryConflictRetried:
		return "Conflict retried"
	case planHistoryManualRetried:
		return "Manual retry"
	case planHistoryImplReviewRequested:
		return "Implementation review requested"
	case planHistoryImplReviewPassed:
		return "Implementation review passed"
	case planHistoryImplReviewFailed:
		return "Implementation review failed"
	default:
		return strings.TrimSpace(entryType)
	}
}

func historyTypeForNotification(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "plan_submitted", "plan_revising":
		return planHistoryPlanningStarted
	case "plan_ready":
		return planHistoryPlanningCompleted
	case "plan_failed":
		return planHistoryPlanningFailed
	case "plan_review_requested":
		return planHistoryReviewRequested
	case "plan_review_passed":
		return planHistoryReviewPassed
	case "plan_review_failed":
		return planHistoryReviewFailed
	case "plan_impl_review_requested":
		return planHistoryImplReviewRequested
	case "plan_impl_review_passed":
		return planHistoryImplReviewPassed
	case "plan_impl_review_failed":
		return planHistoryImplReviewFailed
	default:
		return ""
	}
}

func historyEntryForNotification(history []PlanHistoryEntry, eventType string) (PlanHistoryEntry, bool) {
	wantType := historyTypeForNotification(eventType)
	if wantType == "" {
		return PlanHistoryEntry{}, false
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type == wantType {
			return history[i], true
		}
	}
	return PlanHistoryEntry{}, false
}
