package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

// sessionBreakPrefix is prepended to prompts when reusing an existing Claude
// Code session for a new task, establishing a task boundary while preserving
// codebase knowledge.
const sessionBreakPrefix = "\n---NEW TASK---\n" +
	"Your previous task is complete. This is a new, independent task.\n" +
	"Your codebase knowledge from prior work is still valid and useful — keep it.\n" +
	"Do NOT carry over goals, decisions, or constraints from the previous task.\n" +
	"Files may have been modified by other workers — re-read any file before editing it.\n---\n\n"

// handoverSuffix is appended to each task prompt to instruct the adapter to
// produce a structured handover block.
const handoverSuffix = `

When you are done, output a handover block:
<handover>
<summary>What you did and why</summary>
<decisions>Key decisions made, one per line</decisions>
<files_changed>path:action:description, one per line</files_changed>
<open_questions>Unresolved questions or concerns, one per line</open_questions>
<context>500-word digest for the next task working in this area</context>
</handover>`

// BuildPrompt enriches a task prompt with prior context and handover instructions.
func BuildPrompt(taskPrompt string, priorContext string) string {
	var b strings.Builder
	if priorContext != "" {
		b.WriteString("## Prior Context\n\n")
		b.WriteString(priorContext)
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString(taskPrompt)
	b.WriteString(handoverSuffix)
	return b.String()
}

// ExtractHandover parses a <handover> block from adapter output text.
// Returns nil if no valid handover block is found.
func ExtractHandover(taskID, output string) *pool.TaskHandover {
	block, err := extractTaggedBlockAllowEmpty(output, "handover")
	if err != nil {
		return nil
	}

	h := &pool.TaskHandover{TaskID: taskID}
	h.Summary = extractTag(block, "summary")
	h.ContextForNext = extractTag(block, "context")

	if decisions := extractTag(block, "decisions"); decisions != "" {
		for _, line := range strings.Split(decisions, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				h.KeyDecisions = append(h.KeyDecisions, line)
			}
		}
	}

	if files := extractTag(block, "files_changed"); files != "" {
		for _, line := range strings.Split(files, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, ":", 3)
			fc := pool.FileChange{Path: parts[0]}
			if len(parts) > 1 {
				fc.Action = strings.TrimSpace(parts[1])
			}
			if len(parts) > 2 {
				fc.What = strings.TrimSpace(parts[2])
			}
			h.FilesChanged = append(h.FilesChanged, fc)
		}
	}

	if questions := extractTag(block, "open_questions"); questions != "" {
		for _, line := range strings.Split(questions, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				h.OpenQuestions = append(h.OpenQuestions, line)
			}
		}
	}

	return h
}

// reviewSuffix is the XML block template the reviewer AI must output.
const reviewSuffix = `

At the end of your review, output a review block:
<review>
<verdict>pass|fail</verdict>
<feedback>...</feedback>
<severity>minor|major|critical</severity>
</review>

Use verdict=pass if the implementation is correct and complete, fail otherwise.
Severity is only meaningful when verdict is fail: minor=small issues, major=significant gaps, critical=broken/unsafe.`

// BuildReviewPrompt creates a prompt that instructs the reviewer AI to review an
// implementation. The caller's Execute() will wrap this with BuildPrompt which
// appends the handover suffix — do not call BuildPrompt here.
func BuildReviewPrompt(taskPrompt, implementerSummary, priorContext string) string {
	var b strings.Builder
	if priorContext != "" {
		b.WriteString("## Prior Context\n\n")
		b.WriteString(priorContext)
		b.WriteString("\n\n---\n\n")
	}
	b.WriteString("## Review Request\n\n")
	b.WriteString("You are a code reviewer. Review the implementation described below.\n\n")
	b.WriteString("### Original Task\n\n")
	b.WriteString(taskPrompt)
	b.WriteString("\n\n### Implementer Summary\n\n")
	b.WriteString(implementerSummary)
	b.WriteString(reviewSuffix)
	return b.String()
}

// ExtractReviewVerdict parses a <review> block from reviewer AI output and returns
// (verdict, feedback, severity). Returns ("", "", "") if no valid block is found.
func ExtractReviewVerdict(output string) (verdict, feedback, severity string) {
	block, err := extractTaggedBlock(output, "review")
	if err != nil {
		return "", "", ""
	}
	verdict = extractTag(block, "verdict")
	feedback = extractTag(block, "feedback")
	severity = extractTag(block, "severity")
	if verdict == "" {
		return "", "", ""
	}
	return verdict, feedback, severity
}

func ExtractCouncilTurnArtifact(output string) (*CouncilTurnArtifact, error) {
	body, err := extractTaggedJSON(output, "council_turn")
	if err != nil {
		return nil, err
	}
	var artifact CouncilTurnArtifact
	if err := json.Unmarshal([]byte(body), &artifact); err != nil {
		return nil, fmt.Errorf("decode council turn JSON: %w", err)
	}
	if err := validateCouncilTurnArtifact(&artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}
