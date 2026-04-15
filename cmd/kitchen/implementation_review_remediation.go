package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const AutoRemediationHardCap = 2

const (
	manualReviewRemediationDecisionMinor         = "manual_minor"
	manualReviewRemediationDecisionMinorNit      = "manual_minor_nits"
	manualReviewRemediationDecisionOperatorSteer = "manual_operator_steer"
)

func newAutoRemediationSourceRecord(decision, taskID string, artifact *adapter.ReviewCouncilTurnArtifact) *AutoRemediationSourceRecord {
	if artifact == nil {
		return nil
	}
	return &AutoRemediationSourceRecord{
		Decision:        strings.TrimSpace(decision),
		Verdict:         strings.TrimSpace(artifact.Verdict),
		Seat:            strings.TrimSpace(artifact.Seat),
		Turn:            artifact.Turn,
		ReviewTaskID:    strings.TrimSpace(taskID),
		Summary:         strings.TrimSpace(artifact.Summary),
		Findings:        append([]adapter.ReviewFinding(nil), artifact.Findings...),
		Disagreements:   append([]adapter.CouncilDisagreement(nil), artifact.Disagreements...),
		Strengths:       append([]string(nil), artifact.Strengths...),
		SeatMemo:        strings.TrimSpace(artifact.SeatMemo),
		RejectedReasons: append([]string(nil), artifact.RejectedAlternatives...),
	}
}

func newManualReviewRemediationSourceRecord(taskID string, artifact *adapter.ReviewCouncilTurnArtifact, includeNits bool) *AutoRemediationSourceRecord {
	if artifact == nil {
		return nil
	}
	filter := manualReviewRemediationSeverityFilter(includeNits)
	decision := manualReviewRemediationDecisionMinor
	if includeNits {
		decision = manualReviewRemediationDecisionMinorNit
	}
	return &AutoRemediationSourceRecord{
		Decision:        decision,
		Verdict:         strings.TrimSpace(artifact.Verdict),
		Seat:            strings.TrimSpace(artifact.Seat),
		Turn:            artifact.Turn,
		ReviewTaskID:    strings.TrimSpace(taskID),
		Summary:         strings.TrimSpace(artifact.Summary),
		Findings:        filterReviewFindings(artifact.Findings, filter),
		Disagreements:   filterReviewDisagreements(artifact.Disagreements, filter),
		Strengths:       append([]string(nil), artifact.Strengths...),
		SeatMemo:        strings.TrimSpace(artifact.SeatMemo),
		RejectedReasons: append([]string(nil), artifact.RejectedAlternatives...),
	}
}

func autoRemediationEligible(decision string, artifact *adapter.ReviewCouncilTurnArtifact) bool {
	if artifact == nil {
		return false
	}
	if strings.TrimSpace(artifact.Verdict) != pool.ReviewFail {
		return false
	}
	switch strings.TrimSpace(decision) {
	case reviewCouncilConverged, reviewCouncilReject:
	default:
		return false
	}
	for _, finding := range artifact.Findings {
		switch strings.TrimSpace(finding.Severity) {
		case pool.SeverityMajor, pool.SeverityCritical:
			return true
		}
	}
	return false
}

func autoRemediationFindings(source *AutoRemediationSourceRecord) []string {
	if source == nil {
		return nil
	}
	findings := reviewCouncilFindingsToStrings(source.Findings, source.Disagreements)
	if len(findings) == 0 && strings.TrimSpace(source.Summary) != "" {
		findings = []string{strings.TrimSpace(source.Summary)}
	}
	return findings
}

func autoRemediationPlanTaskID(attempt int) string {
	if attempt < 1 {
		attempt = 1
	}
	return fmt.Sprintf("review-fix-r%d", attempt)
}

func nextManualReviewRemediationPlanTaskID(plan PlanRecord) string {
	const prefix = "review-fix-manual-r"
	next := 1
	for _, task := range plan.Tasks {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(task.ID), prefix+"%d", &n); err == nil && n >= next {
			next = n + 1
		}
	}
	return fmt.Sprintf("%s%d", prefix, next)
}

func maxImplementationTaskTimeout(plan PlanRecord) int {
	maxTimeout := 0
	for _, task := range plan.Tasks {
		if task.TimeoutMinutes > maxTimeout {
			maxTimeout = task.TimeoutMinutes
		}
	}
	return maxTimeout
}

func maxImplementationTaskComplexity(plan PlanRecord) Complexity {
	maxLevel := ComplexityLow
	for _, task := range plan.Tasks {
		level := task.Complexity
		switch level {
		case ComplexityHigh:
			return ComplexityHigh
		case ComplexityMedium:
			maxLevel = ComplexityMedium
		}
	}
	return maxLevel
}

func buildAutoRemediationPrompt(bundle StoredPlan, source *AutoRemediationSourceRecord) string {
	var b strings.Builder
	operatorNotes := implementationSteeringNotesForTask(bundle.Execution, strings.TrimSpace(bundle.Execution.AutoRemediationPlanTaskID))
	b.WriteString("You are fixing implementation-review findings for an existing Kitchen plan.\n\n")
	b.WriteString("## Plan\n")
	b.WriteString("- Title: `")
	b.WriteString(strings.TrimSpace(bundle.Plan.Title))
	b.WriteString("`\n- Lineage: `")
	b.WriteString(strings.TrimSpace(bundle.Plan.Lineage))
	b.WriteString("`\n")
	if strings.TrimSpace(bundle.Plan.Summary) != "" {
		b.WriteString("- Summary: ")
		b.WriteString(strings.TrimSpace(bundle.Plan.Summary))
		b.WriteString("\n")
	}
	if source != nil {
		b.WriteString("\n## Review Outcome\n")
		b.WriteString("- Decision: `")
		b.WriteString(firstNonEmpty(strings.TrimSpace(source.Decision), "fail"))
		b.WriteString("`\n- Verdict: `")
		b.WriteString(firstNonEmpty(strings.TrimSpace(source.Verdict), pool.ReviewFail))
		b.WriteString("`\n")
		if source.Turn > 0 {
			b.WriteString(fmt.Sprintf("- Source turn: %d (seat %s)\n", source.Turn, firstNonEmpty(strings.TrimSpace(source.Seat), "?")))
		}
		if strings.TrimSpace(source.ReviewTaskID) != "" {
			b.WriteString("- Source task: `")
			b.WriteString(strings.TrimSpace(source.ReviewTaskID))
			b.WriteString("`\n")
		}
		if strings.TrimSpace(source.Summary) != "" {
			b.WriteString("- Summary: ")
			b.WriteString(strings.TrimSpace(source.Summary))
			b.WriteString("\n")
		}
	}
	if len(operatorNotes) > 0 {
		b.WriteString("\n## Operator Guidance\n")
		for _, note := range operatorNotes {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(note))
			b.WriteString("\n")
		}
	}

	findings := autoRemediationFindings(source)
	if len(findings) > 0 {
		b.WriteString("\n## Findings To Address\n")
		for _, finding := range findings {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(finding))
			b.WriteString("\n")
		}
	}

	if source != nil && len(source.Findings) > 0 {
		b.WriteString("\n## Structured Findings\n")
		for _, finding := range source.Findings {
			location := strings.TrimSpace(finding.File)
			if location != "" && finding.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, finding.Line)
			}
			parts := []string{}
			if sev := strings.TrimSpace(finding.Severity); sev != "" {
				parts = append(parts, "["+sev+"]")
			}
			if location != "" {
				parts = append(parts, location)
			}
			if category := strings.TrimSpace(finding.Category); category != "" {
				parts = append(parts, category)
			}
			line := strings.Join(parts, " ")
			if line != "" {
				line += " - "
			}
			line += strings.TrimSpace(finding.Description)
			if suggestion := strings.TrimSpace(finding.Suggestion); suggestion != "" {
				line += " | Suggested fix: " + suggestion
			}
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(line))
			b.WriteString("\n")
		}
	}
	if source != nil && len(source.Disagreements) > 0 {
		b.WriteString("\n## Supporting Disagreements\n")
		for _, item := range source.Disagreements {
			line := fmt.Sprintf("[%s] %s", firstNonEmpty(strings.TrimSpace(item.Severity), "unknown"), firstNonEmpty(strings.TrimSpace(item.Title), item.ID))
			if impact := strings.TrimSpace(item.Impact); impact != "" {
				line += " - " + impact
			}
			if change := strings.TrimSpace(item.SuggestedChange); change != "" {
				line += " | Suggested change: " + change
			}
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(line))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## Requirements\n")
	if reviewRemediationMode(source) == reviewRemediationModeManual && len(findings) > 0 {
		b.WriteString("- Fix the requested lower-severity follow-up findings directly.\n")
	} else if len(findings) > 0 {
		b.WriteString("- Fix the review findings directly.\n")
	}
	if len(operatorNotes) > 0 {
		b.WriteString("- Apply the operator guidance as required implementation direction.\n")
	}
	b.WriteString("- Keep the changes scoped; do not perform unrelated refactors.\n")
	b.WriteString("- Update or add tests when needed to prove the findings are resolved.\n")
	b.WriteString("- Leave the lineage branch in a state ready for implementation review again.\n")
	return b.String()
}

func implementationSteeringNotesForTask(exec ExecutionRecord, planTaskID string) []string {
	planTaskID = strings.TrimSpace(planTaskID)
	if planTaskID == "" {
		return nil
	}
	notes := make([]string, 0, len(exec.ImplementationSteeringNotes))
	for _, item := range exec.ImplementationSteeringNotes {
		if strings.TrimSpace(item.AppliedTaskID) != planTaskID {
			continue
		}
		if note := strings.TrimSpace(item.Note); note != "" {
			notes = append(notes, note)
		}
	}
	return notes
}

func autoRemediationPlanTask(bundle StoredPlan, attempt int) PlanTask {
	source := bundle.Execution.AutoRemediationSource
	planTaskID := strings.TrimSpace(bundle.Execution.AutoRemediationPlanTaskID)
	if planTaskID == "" {
		planTaskID = autoRemediationPlanTaskID(attempt)
	}
	findings := autoRemediationFindings(source)
	sort.Strings(findings)
	return PlanTask{
		ID:         planTaskID,
		Title:      "Address implementation review findings",
		Prompt:     buildAutoRemediationPrompt(bundle, source),
		Complexity: maxImplementationTaskComplexity(bundle.Plan),
		SuccessCriteria: &PlanSuccessCriteria{
			Advisory:   "Address the implementation review findings without broad unrelated changes.",
			Verifiable: findings,
		},
		ReviewComplexity: implementationReviewComplexityForPlan(bundle.Plan),
		TimeoutMinutes:   maxImplementationTaskTimeout(bundle.Plan),
	}
}

func clearAutoRemediationState(exec *ExecutionRecord, resetCount bool) {
	if exec == nil {
		return
	}
	if resetCount {
		exec.AutoRemediationAttempt = 0
	}
	exec.AutoRemediationActive = false
	exec.AutoRemediationPlanTaskID = ""
	exec.AutoRemediationTaskID = ""
	exec.AutoRemediationSourceTaskID = ""
	exec.AutoRemediationSource = nil
}

func completeAutoRemediationState(exec *ExecutionRecord) {
	if exec == nil {
		return
	}
	exec.AutoRemediationActive = false
	exec.AutoRemediationPlanTaskID = ""
	exec.AutoRemediationTaskID = ""
	exec.AutoRemediationSourceTaskID = ""
	exec.AutoRemediationSource = nil
}

func planHasTask(plan PlanRecord, planTaskID string) bool {
	planTaskID = strings.TrimSpace(planTaskID)
	if planTaskID == "" {
		return false
	}
	for _, task := range plan.Tasks {
		if strings.TrimSpace(task.ID) == planTaskID {
			return true
		}
	}
	return false
}

func latestPassedReviewCouncilArtifact(bundle StoredPlan) (string, *adapter.ReviewCouncilTurnArtifact) {
	for i := len(bundle.Execution.ReviewCouncilTurns) - 1; i >= 0; i-- {
		turn := bundle.Execution.ReviewCouncilTurns[i]
		if turn.Artifact == nil || strings.TrimSpace(turn.Artifact.Verdict) != pool.ReviewPass {
			continue
		}
		taskID := reviewCouncilTaskIDForCycle(bundle.Plan.PlanID, currentReviewCouncilCycle(bundle.Execution), turn.Turn)
		return taskID, turn.Artifact
	}
	return "", nil
}

func reviewCouncilFollowupStrings(turns []ReviewCouncilTurnRecord, allow reviewSeverityFilter) []string {
	findings := make([]adapter.ReviewFinding, 0)
	disagreements := make([]adapter.CouncilDisagreement, 0)
	for _, turn := range turns {
		if turn.Artifact == nil || strings.TrimSpace(turn.Artifact.Verdict) != pool.ReviewPass {
			continue
		}
		findings = append(findings, filterReviewFindings(turn.Artifact.Findings, allow)...)
		disagreements = append(disagreements, filterReviewDisagreements(turn.Artifact.Disagreements, allow)...)
	}
	return reviewCouncilFindingsToStringsFiltered(findings, disagreements, allowAllReviewSeverities)
}

func manualReviewRemediationSource(bundle StoredPlan, includeNits bool) (string, *AutoRemediationSourceRecord) {
	taskID, latest := latestPassedReviewCouncilArtifact(bundle)
	if latest == nil {
		return "", nil
	}
	source := newManualReviewRemediationSourceRecord(taskID, latest, includeNits)
	source.Findings = nil
	source.Disagreements = nil
	filter := manualReviewRemediationSeverityFilter(includeNits)
	for _, turn := range bundle.Execution.ReviewCouncilTurns {
		if turn.Artifact == nil || strings.TrimSpace(turn.Artifact.Verdict) != pool.ReviewPass {
			continue
		}
		source.Findings = append(source.Findings, filterReviewFindings(turn.Artifact.Findings, filter)...)
		source.Disagreements = append(source.Disagreements, filterReviewDisagreements(turn.Artifact.Disagreements, filter)...)
	}
	return taskID, source
}

func latestReviewCouncilArtifact(bundle StoredPlan) (string, *adapter.ReviewCouncilTurnArtifact) {
	for i := len(bundle.Execution.ReviewCouncilTurns) - 1; i >= 0; i-- {
		turn := bundle.Execution.ReviewCouncilTurns[i]
		if turn.Artifact == nil {
			continue
		}
		taskID := reviewCouncilTaskIDForCycle(bundle.Plan.PlanID, currentReviewCouncilCycle(bundle.Execution), turn.Turn)
		return taskID, turn.Artifact
	}
	return "", nil
}

func implementationSteeringRemediationSource(bundle StoredPlan) (string, *AutoRemediationSourceRecord) {
	taskID, latest := latestReviewCouncilArtifact(bundle)
	if latest == nil {
		source := &AutoRemediationSourceRecord{
			Decision: manualReviewRemediationDecisionOperatorSteer,
			Verdict:  strings.TrimSpace(bundle.Execution.ImplReviewStatus),
		}
		if strings.TrimSpace(bundle.Execution.ImplReviewStatus) == planReviewStatusFailed && len(bundle.Execution.ImplReviewFindings) > 0 {
			source.Summary = strings.Join(bundle.Execution.ImplReviewFindings, "; ")
		}
		return "", source
	}
	source := newAutoRemediationSourceRecord(manualReviewRemediationDecisionOperatorSteer, taskID, latest)
	if strings.TrimSpace(bundle.Execution.State) != planStateImplementationReviewFailed &&
		strings.TrimSpace(bundle.Execution.ImplReviewStatus) != planReviewStatusFailed {
		source.Findings = nil
		source.Disagreements = nil
	}
	return taskID, source
}
