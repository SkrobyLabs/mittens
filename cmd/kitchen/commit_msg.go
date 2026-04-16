package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const commitStyleGuide = `Commit message style guide:
  First line: <type>: <short description>
  Allowed types: feat, fix, refactor, docs, test, chore, perf, style, ci, mixed
  No scope — write "feat:" not "feat(scope):"
  Subject in imperative mood, aim for 50-72 chars (do not hard-enforce)
  Blank line after subject
  Then sections in this order when present:
    Features:
    - one bullet per item (single unbroken line)
    Fixes:
    - one bullet per item
    Refactors:
    - one bullet per item
    Docs:
    - one bullet per item
    Tests:
    - one bullet per item
    Other:
    - one bullet per item
  Each section header must end with ":"
  Each bullet must start with "- " on its own line`

func fallbackSquashCommitMessage(sourceBranch, targetBranch string) string {
	return "Squash merge " + sourceBranch + " into " + targetBranch
}

type squashCommitMessageFallbackRequired struct {
	Fallback string
	Reason   string
}

func (e *squashCommitMessageFallbackRequired) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Reason)
}

// claudeRunner is the seam used by generateSquashCommitMessage to invoke
// Claude. Tests may replace this variable to simulate success, failure,
// or timeout without requiring the real claude binary.
var claudeRunner = func(ctx context.Context, repoPath, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "--print", "--output-format", "text", prompt)
	cmd.Dir = repoPath
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return out.String(), nil
}

var allowedCommitTypes = []string{
	"feat:", "fix:", "refactor:", "docs:", "test:",
	"chore:", "perf:", "style:", "ci:", "mixed:",
}

var allowedSectionHeaders = []string{
	"Features:", "Fixes:", "Refactors:", "Docs:", "Tests:", "Other:",
}

// generateSquashCommitMessage asks Claude to produce a well-formed commit
// message for a squash merge of sourceBranch into targetBranch.
func generateSquashCommitMessage(repoPath, sourceBranch, targetBranch string) (string, error) {
	fallback := fallbackSquashCommitMessage(sourceBranch, targetBranch)

	logOut, err := runGit(repoPath, "log", "--oneline", targetBranch+".."+sourceBranch)
	if err != nil {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   fmt.Sprintf("failed to collect squash commit history: %v", err),
		}
	}
	if strings.TrimSpace(logOut) == "" {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   "failed to collect squash commit history: no commits found to summarize",
		}
	}

	diffOut, err := runGit(repoPath, "diff", "--stat", targetBranch+"..."+sourceBranch)
	if err != nil {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   fmt.Sprintf("failed to collect squash diff summary: %v", err),
		}
	}
	if strings.TrimSpace(diffOut) == "" {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   "failed to collect squash diff summary: no changed files found to summarize",
		}
	}

	prompt := commitStyleGuide + "\n\nCommit history being squashed:\n" + strings.TrimSpace(logOut) +
		"\n\nChanged files summary:\n" + strings.TrimSpace(diffOut) +
		"\n\nOutput ONLY the commit message with no prose, no explanation, and no code fences."

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	output, err := claudeRunner(ctx, repoPath, prompt)
	if err != nil {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   fmt.Sprintf("llm-generated squash commit message failed: %v", err),
		}
	}

	result := strings.TrimSpace(output)
	if result == "" {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   "llm-generated squash commit message was empty",
		}
	}
	if err := validateCommitMessage(result); err != nil {
		return "", &squashCommitMessageFallbackRequired{
			Fallback: fallback,
			Reason:   fmt.Sprintf("llm-generated squash commit message was invalid: %v", err),
		}
	}

	return result, nil
}

func buildSquashCommitMessageTaskPrompt(repoPath, sourceBranch, targetBranch, planTitle, planSummary string) (string, string, error) {
	fallback := fallbackSquashCommitMessage(sourceBranch, targetBranch)

	logOut, err := runGit(repoPath, "log", "--oneline", targetBranch+".."+sourceBranch)
	if err != nil {
		return "", fallback, fmt.Errorf("collect squash commit history: %w", err)
	}
	if strings.TrimSpace(logOut) == "" {
		return "", fallback, fmt.Errorf("collect squash commit history: no commits found to summarize")
	}

	diffOut, err := runGit(repoPath, "diff", "--stat", targetBranch+"..."+sourceBranch)
	if err != nil {
		return "", fallback, fmt.Errorf("collect squash diff summary: %w", err)
	}
	if strings.TrimSpace(diffOut) == "" {
		return "", fallback, fmt.Errorf("collect squash diff summary: no changed files found to summarize")
	}

	var prompt strings.Builder
	prompt.WriteString("You are preparing a squash merge commit message for Kitchen.\n")
	prompt.WriteString("Do not modify files. Do not run git commit. Read the repository and return only the requested commit message block.\n\n")
	prompt.WriteString(commitStyleGuide)
	prompt.WriteString("\n\n")
	if title := strings.TrimSpace(planTitle); title != "" {
		prompt.WriteString("Plan title:\n")
		prompt.WriteString(title)
		prompt.WriteString("\n\n")
	}
	if summary := strings.TrimSpace(planSummary); summary != "" {
		prompt.WriteString("Plan summary:\n")
		prompt.WriteString(summary)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("Commit history being squashed:\n")
	prompt.WriteString(strings.TrimSpace(logOut))
	prompt.WriteString("\n\nChanged files summary:\n")
	prompt.WriteString(strings.TrimSpace(diffOut))
	prompt.WriteString("\n\nReturn ONLY this exact envelope with no extra prose:\n<commit_message>\n")
	prompt.WriteString("type: subject\n\nFeatures:\n- item\n</commit_message>\n")
	return prompt.String(), fallback, nil
}

func extractCommitMessageFromTaskOutput(output string) (string, error) {
	const (
		openTag  = "<commit_message>"
		closeTag = "</commit_message>"
	)
	start := strings.Index(output, openTag)
	if start < 0 {
		return "", fmt.Errorf("missing <commit_message> block")
	}
	start += len(openTag)
	end := strings.Index(output[start:], closeTag)
	if end < 0 {
		return "", fmt.Errorf("missing </commit_message> block")
	}
	msg := strings.TrimSpace(output[start : start+end])
	if msg == "" {
		return "", fmt.Errorf("empty <commit_message> block")
	}
	if err := validateCommitMessage(msg); err != nil {
		return "", err
	}
	return msg, nil
}

// validateCommitMessage checks that msg meets the minimum structural
// requirements defined in the style guide.
func validateCommitMessage(msg string) error {
	lines := strings.Split(msg, "\n")
	if len(lines) == 0 {
		return fmt.Errorf("commit message is empty")
	}

	firstLine := strings.TrimSpace(lines[0])
	validType := false
	for _, t := range allowedCommitTypes {
		if strings.HasPrefix(firstLine, t) {
			validType = true
			break
		}
	}
	if !validType {
		return fmt.Errorf("first line must start with an allowed type prefix")
	}

	hasSection := false
	hasBullet := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		for _, s := range allowedSectionHeaders {
			if trimmed == s {
				hasSection = true
			}
		}
		if strings.HasPrefix(trimmed, "- ") {
			hasBullet = true
		}
	}
	if !hasSection {
		return fmt.Errorf("missing section header")
	}
	if !hasBullet {
		return fmt.Errorf("missing bullet list")
	}
	return nil
}
