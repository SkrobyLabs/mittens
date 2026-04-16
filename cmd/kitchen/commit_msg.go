package main

import (
	"bytes"
	"context"
	"io"
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

// claudeRunner is the seam used by generateSquashCommitMessage to invoke
// Claude. Tests may replace this variable to simulate success, failure,
// or timeout without requiring the real claude binary.
var claudeRunner = func(ctx context.Context, repoPath, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, "claude", "--print", "--output-format", "text", prompt)
	cmd.Dir = repoPath
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
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
// message for a squash merge of sourceBranch into targetBranch. It always
// returns a non-empty string: the generated message when Claude succeeds and
// produces valid output, or the hardcoded fallback otherwise.
func generateSquashCommitMessage(repoPath, sourceBranch, targetBranch string) string {
	fallback := "Squash merge " + sourceBranch + " into " + targetBranch

	logOut, err := runGit(repoPath, "log", "--oneline", targetBranch+".."+sourceBranch)
	if err != nil || strings.TrimSpace(logOut) == "" {
		return fallback
	}

	diffOut, err := runGit(repoPath, "diff", "--stat", targetBranch+"..."+sourceBranch)
	if err != nil || strings.TrimSpace(diffOut) == "" {
		return fallback
	}

	prompt := commitStyleGuide + "\n\nCommit history being squashed:\n" + strings.TrimSpace(logOut) +
		"\n\nChanged files summary:\n" + strings.TrimSpace(diffOut) +
		"\n\nOutput ONLY the commit message with no prose, no explanation, and no code fences."

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	output, err := claudeRunner(ctx, repoPath, prompt)
	if err != nil {
		return fallback
	}

	result := strings.TrimSpace(output)
	if result == "" || !isValidCommitMessage(result) {
		return fallback
	}

	return result
}

// isValidCommitMessage checks that msg meets the minimum structural
// requirements defined in the style guide. Returns false if any check fails,
// triggering the fallback path in generateSquashCommitMessage.
func isValidCommitMessage(msg string) bool {
	lines := strings.Split(msg, "\n")
	if len(lines) == 0 {
		return false
	}

	// First line must start with an allowed type prefix.
	firstLine := strings.TrimSpace(lines[0])
	validType := false
	for _, t := range allowedCommitTypes {
		if strings.HasPrefix(firstLine, t) {
			validType = true
			break
		}
	}
	if !validType {
		return false
	}

	// Body must contain at least one recognized section header and one bullet.
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

	return hasSection && hasBullet
}
