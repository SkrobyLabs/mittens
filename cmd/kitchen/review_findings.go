package main

import (
	"fmt"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const (
	reviewRemediationModeAuto   = "auto"
	reviewRemediationModeManual = "manual"
)

type reviewSeverityFilter func(string) bool

func allowAllReviewSeverities(string) bool { return true }

func reviewSeverityAtMost(maxSeverity string) reviewSeverityFilter {
	maxRank := pool.ReviewSeverityRank(maxSeverity)
	return func(severity string) bool {
		rank := pool.ReviewSeverityRank(severity)
		return rank > 0 && rank <= maxRank
	}
}

func manualReviewRemediationSeverityFilter(includeNits bool) reviewSeverityFilter {
	return func(severity string) bool {
		switch pool.NormalizeReviewSeverity(severity) {
		case pool.SeverityMinor:
			return true
		case pool.SeverityNit:
			return includeNits
		default:
			return false
		}
	}
}

func reviewFindingsContainSeverityAtLeast(findings []adapter.ReviewFinding, minSeverity string) bool {
	for _, finding := range findings {
		if pool.ReviewSeverityAtLeast(finding.Severity, minSeverity) {
			return true
		}
	}
	return false
}

func reviewDisagreementsContainSeverityAtLeast(disagreements []adapter.CouncilDisagreement, minSeverity string) bool {
	for _, item := range disagreements {
		if pool.ReviewSeverityAtLeast(item.Severity, minSeverity) {
			return true
		}
	}
	return false
}

func filterReviewFindings(findings []adapter.ReviewFinding, allow reviewSeverityFilter) []adapter.ReviewFinding {
	if allow == nil {
		allow = allowAllReviewSeverities
	}
	filtered := make([]adapter.ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if allow(finding.Severity) {
			filtered = append(filtered, finding)
		}
	}
	return filtered
}

func filterReviewDisagreements(disagreements []adapter.CouncilDisagreement, allow reviewSeverityFilter) []adapter.CouncilDisagreement {
	if allow == nil {
		allow = allowAllReviewSeverities
	}
	filtered := make([]adapter.CouncilDisagreement, 0, len(disagreements))
	for _, item := range disagreements {
		if allow(item.Severity) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func reviewCouncilFindingsToStringsFiltered(findings []adapter.ReviewFinding, disagreements []adapter.CouncilDisagreement, allow reviewSeverityFilter) []string {
	if allow == nil {
		allow = allowAllReviewSeverities
	}
	out := make([]string, 0, len(findings)+len(disagreements))
	for _, finding := range findings {
		if !allow(finding.Severity) {
			continue
		}
		parts := make([]string, 0, 3)
		if sev := strings.TrimSpace(finding.Severity); sev != "" {
			parts = append(parts, "["+sev+"]")
		}
		location := strings.TrimSpace(finding.File)
		if location != "" && finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		if location != "" {
			parts = append(parts, location)
		}
		if category := strings.TrimSpace(finding.Category); category != "" {
			parts = append(parts, category)
		}
		prefix := strings.Join(parts, " ")
		desc := strings.TrimSpace(finding.Description)
		if prefix != "" && desc != "" {
			out = append(out, prefix+" - "+desc)
		} else if prefix != "" {
			out = append(out, prefix)
		} else if desc != "" {
			out = append(out, desc)
		}
	}
	for _, item := range disagreements {
		if !allow(item.Severity) {
			continue
		}
		label := "[disagreement"
		if sev := strings.TrimSpace(item.Severity); sev != "" {
			label += "/" + sev
		}
		label += "]"
		text := strings.TrimSpace(item.Title)
		if impact := strings.TrimSpace(item.Impact); impact != "" {
			if text != "" {
				text += " - " + impact
			} else {
				text = impact
			}
		}
		out = append(out, strings.TrimSpace(label+" "+text))
	}
	return out
}

func normalizeReviewCouncilArtifact(artifact *adapter.ReviewCouncilTurnArtifact) bool {
	if artifact == nil {
		return false
	}
	for i := range artifact.Findings {
		artifact.Findings[i].Severity = pool.NormalizeReviewSeverity(artifact.Findings[i].Severity)
	}
	for i := range artifact.Disagreements {
		artifact.Disagreements[i].Severity = pool.NormalizeReviewSeverity(artifact.Disagreements[i].Severity)
	}
	if strings.TrimSpace(artifact.Verdict) != pool.ReviewPass {
		return false
	}
	if !reviewFindingsContainSeverityAtLeast(artifact.Findings, pool.SeverityMajor) &&
		!reviewDisagreementsContainSeverityAtLeast(artifact.Disagreements, pool.SeverityMajor) {
		return false
	}
	artifact.Verdict = pool.ReviewFail
	note := "Verdict normalized to fail because pass cannot include major or critical findings."
	if summary := strings.TrimSpace(artifact.Summary); summary == "" {
		artifact.Summary = note
	} else if !strings.Contains(summary, note) {
		artifact.Summary = summary + " " + note
	}
	if memo := strings.TrimSpace(artifact.SeatMemo); memo == "" {
		artifact.SeatMemo = note
	} else if !strings.Contains(memo, note) {
		artifact.SeatMemo = memo + " " + note
	}
	return true
}

func reviewRemediationMode(source *AutoRemediationSourceRecord) string {
	if source == nil {
		return ""
	}
	switch {
	case strings.HasPrefix(strings.TrimSpace(source.Decision), "manual_"):
		return reviewRemediationModeManual
	case strings.TrimSpace(source.Decision) != "":
		return reviewRemediationModeAuto
	default:
		return ""
	}
}
