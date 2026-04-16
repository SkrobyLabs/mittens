package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	planHistoryPlanningStarted              = "planning_started"
	planHistoryPlanningCompleted            = "planning_completed"
	planHistoryPlanningFailed               = "planning_failed"
	planHistoryCouncilStarted               = "council_started"
	planHistoryCouncilTurnCompleted         = "council_turn_completed"
	planHistoryCouncilWaitingAnswers        = "council_waiting_answers"
	planHistoryCouncilResumed               = "council_resumed"
	planHistoryCouncilConverged             = "council_converged"
	planHistoryCouncilAutoConverged         = "council_auto_converged"
	planHistoryCouncilRejected              = "council_rejected"
	planHistoryCouncilSeatRehydrated        = "council_seat_rehydrated"
	planHistoryCouncilExtended              = "council_extended"
	planHistoryCouncilSteered               = "council_steered"
	planHistoryReviewCouncilStarted         = "review_council_started"
	planHistoryReviewCouncilTurnCompleted   = "review_council_turn_completed"
	planHistoryReviewCouncilWaitingAnswers  = "review_council_waiting_answers"
	planHistoryReviewCouncilResumed         = "review_council_resumed"
	planHistoryReviewCouncilConverged       = "review_council_converged"
	planHistoryReviewCouncilRejected        = "review_council_rejected"
	planHistoryReviewCouncilSeatRehydrated  = "review_council_seat_rehydrated"
	planHistoryReviewCouncilExtended        = "review_council_extended"
	planHistoryConflictRetried              = "conflict_retried"
	planHistoryManualRetried                = "manual_retried"
	planHistoryConflictFixRequested         = "conflict_fix_requested"
	planHistoryLineageFixMergeRequested     = "lineage_fix_merge_requested"
	planHistoryLineageFixMergeCompleted     = "lineage_fix_merge_completed"
	planHistoryLineageMergeRequested        = "lineage_merge_requested"
	planHistoryLineageMergeCompleted        = "lineage_merge_completed"
	planHistoryLineageMergeFailed           = "lineage_merge_failed"
	planHistoryLineageMergeFallbackPrompt   = "lineage_merge_fallback_prompted"
	planHistoryLineageMergeFallbackDeclined = "lineage_merge_fallback_declined"
	planHistoryImplReviewRequested          = "impl_review_requested"
	planHistoryImplReviewPassed             = "impl_review_passed"
	planHistoryImplReviewFailed             = "impl_review_failed"
	planHistoryImplementationSteered        = "implementation_steered"
	planHistoryManualReviewRemediation      = "manual_review_remediation_requested"
	planHistoryAutoRemediationRequested     = "auto_remediation_requested"
	planHistoryAutoRemediationRecovered     = "auto_remediation_recovered"
	planHistoryAutoRemediationSkipped       = "auto_remediation_skipped"
	planHistoryQuickPlanSubmitted           = "quick_plan_submitted"
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
	entry = normalizePlanHistoryEntry(entry)
	ex.History = append(ex.History, entry)
	return ex
}

func upsertPlanHistory(ex ExecutionRecord, entry PlanHistoryEntry, match func(PlanHistoryEntry) bool) ExecutionRecord {
	entry = normalizePlanHistoryEntry(entry)
	for i := range ex.History {
		if match == nil || !match(ex.History[i]) {
			continue
		}
		if !ex.History[i].OccurredAt.IsZero() {
			entry.OccurredAt = ex.History[i].OccurredAt
		}
		ex.History[i] = entry
		return ex
	}
	ex.History = append(ex.History, entry)
	return ex
}

func normalizePlanHistoryEntry(entry PlanHistoryEntry) PlanHistoryEntry {
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
	return entry
}

func matchesPlanHistoryEntry(entry PlanHistoryEntry, entryType, taskID string, cycle int) bool {
	return strings.TrimSpace(entry.Type) == strings.TrimSpace(entryType) &&
		strings.TrimSpace(entry.TaskID) == strings.TrimSpace(taskID) &&
		entry.Cycle == cycle
}

func plannerCycleForTask(planID, taskID string) int {
	if turn := councilTurnNumberFromTaskID(planID, taskID); turn > 0 {
		return turn
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
	case planHistoryCouncilStarted:
		return "Council started"
	case planHistoryCouncilTurnCompleted:
		return "Council turn completed"
	case planHistoryCouncilWaitingAnswers:
		return "Council waiting for answers"
	case planHistoryCouncilResumed:
		return "Council resumed"
	case planHistoryCouncilConverged:
		return "Council converged"
	case planHistoryCouncilAutoConverged:
		return "Council auto-converged (structural match)"
	case planHistoryCouncilRejected:
		return "Council rejected"
	case planHistoryCouncilSeatRehydrated:
		return "Council seat rehydrated"
	case planHistoryCouncilExtended:
		return "Council extended"
	case planHistoryCouncilSteered:
		return "Council steered"
	case planHistoryReviewCouncilStarted:
		return "Review council started"
	case planHistoryReviewCouncilTurnCompleted:
		return "Review council turn completed"
	case planHistoryReviewCouncilWaitingAnswers:
		return "Review council waiting for answers"
	case planHistoryReviewCouncilResumed:
		return "Review council resumed"
	case planHistoryReviewCouncilConverged:
		return "Review council converged"
	case planHistoryReviewCouncilRejected:
		return "Review council rejected"
	case planHistoryReviewCouncilSeatRehydrated:
		return "Review council seat rehydrated"
	case planHistoryReviewCouncilExtended:
		return "Review council extended"
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
	case planHistoryLineageMergeRequested:
		return "Lineage merge requested"
	case planHistoryLineageMergeCompleted:
		return "Lineage merge completed"
	case planHistoryLineageMergeFailed:
		return "Lineage merge failed"
	case planHistoryLineageMergeFallbackPrompt:
		return "Lineage merge fallback prompted"
	case planHistoryLineageMergeFallbackDeclined:
		return "Lineage merge fallback declined"
	case planHistoryImplementationSteered:
		return "Implementation steered"
	case planHistoryManualReviewRemediation:
		return "Manual review remediation requested"
	case planHistoryAutoRemediationRequested:
		return "Auto-remediation requested"
	case planHistoryAutoRemediationRecovered:
		return "Auto-remediation recovered"
	case planHistoryAutoRemediationSkipped:
		return "Auto-remediation skipped"
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
