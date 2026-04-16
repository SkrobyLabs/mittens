package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const validCommitMsg = `feat: add parser error recovery

Features:
- add error recovery for malformed input tokens
- handle EOF gracefully in all parser states`

func TestGenerateSquashCommitMessage_Success(t *testing.T) {
	repo := initGitRepo(t)

	// Set up a lineage branch with a commit so git log/diff return real output.
	worktrees := filepath.Join(t.TempDir(), "worktrees")
	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("feat", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("feat", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wt, "feat.go"), "package main\n")
	mustRunGit(t, wt, "add", "feat.go")
	mustRunGit(t, wt, "commit", "-m", "add parser error recovery")
	if err := gm.MergeChild("feat", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	orig := claudeRunner
	defer func() { claudeRunner = orig }()
	claudeRunner = func(_ context.Context, _, _ string) (string, error) {
		return validCommitMsg, nil
	}

	lineageBranch := lineageBranchName("feat")
	got := generateSquashCommitMessage(repo, lineageBranch, "main")
	if got != validCommitMsg {
		t.Fatalf("got %q, want %q", got, validCommitMsg)
	}
}

func TestGenerateSquashCommitMessage_ExecFailure(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")
	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("feat", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("feat", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wt, "f.txt"), "x\n")
	mustRunGit(t, wt, "add", "f.txt")
	mustRunGit(t, wt, "commit", "-m", "add f")
	if err := gm.MergeChild("feat", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	orig := claudeRunner
	defer func() { claudeRunner = orig }()
	claudeRunner = func(_ context.Context, _, _ string) (string, error) {
		return "", errors.New("exec: not found")
	}

	lineageBranch := lineageBranchName("feat")
	fallback := "Squash merge " + lineageBranch + " into main"
	got := generateSquashCommitMessage(repo, lineageBranch, "main")
	if got != fallback {
		t.Fatalf("got %q, want fallback %q", got, fallback)
	}
}

func TestGenerateSquashCommitMessage_Timeout(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")
	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("feat", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("feat", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wt, "f.txt"), "x\n")
	mustRunGit(t, wt, "add", "f.txt")
	mustRunGit(t, wt, "commit", "-m", "add f")
	if err := gm.MergeChild("feat", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	orig := claudeRunner
	defer func() { claudeRunner = orig }()
	claudeRunner = func(ctx context.Context, _, _ string) (string, error) {
		// Simulate timeout by returning the context error.
		return "", context.DeadlineExceeded
	}

	lineageBranch := lineageBranchName("feat")
	fallback := "Squash merge " + lineageBranch + " into main"
	got := generateSquashCommitMessage(repo, lineageBranch, "main")
	if got != fallback {
		t.Fatalf("got %q, want fallback %q", got, fallback)
	}
}

func TestGenerateSquashCommitMessage_MalformedOutput(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")
	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("feat", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("feat", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wt, "f.txt"), "x\n")
	mustRunGit(t, wt, "add", "f.txt")
	mustRunGit(t, wt, "commit", "-m", "add f")
	if err := gm.MergeChild("feat", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	lineageBranch := lineageBranchName("feat")
	fallback := "Squash merge " + lineageBranch + " into main"

	cases := []struct {
		name   string
		output string
	}{
		{"empty", ""},
		{"prose no type", "Here is your commit message:\nfeat: add something\n\nFeatures:\n- bullet"},
		{"missing section", "feat: add something\n\nThis has no section header."},
		{"missing bullet", "feat: add something\n\nFeatures:\nno bullet prefix here"},
		{"code fence", "```\nfeat: add something\n\nFeatures:\n- bullet\n```"},
		{"wrong type", "update: add something\n\nFeatures:\n- bullet"},
	}

	orig := claudeRunner
	defer func() { claudeRunner = orig }()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claudeRunner = func(_ context.Context, _, _ string) (string, error) {
				return tc.output, nil
			}
			got := generateSquashCommitMessage(repo, lineageBranch, "main")
			if got != fallback {
				t.Fatalf("output=%q: got %q, want fallback %q", tc.output, got, fallback)
			}
		})
	}
}

func TestIsValidCommitMessage(t *testing.T) {
	cases := []struct {
		name  string
		msg   string
		valid bool
	}{
		{
			name:  "valid full message",
			msg:   validCommitMsg,
			valid: true,
		},
		{
			name:  "valid fix message",
			msg:   "fix: correct nil deref\n\nFixes:\n- prevent panic when token is nil",
			valid: true,
		},
		{
			name:  "missing type prefix",
			msg:   "add something nice\n\nFeatures:\n- add thing",
			valid: false,
		},
		{
			name:  "missing section header",
			msg:   "feat: add something\n\nSome prose without a section.",
			valid: false,
		},
		{
			name:  "missing bullet",
			msg:   "feat: add something\n\nFeatures:\nno dash prefix",
			valid: false,
		},
		{
			name:  "empty string",
			msg:   "",
			valid: false,
		},
		{
			name:  "mixed type",
			msg:   "mixed: various improvements\n\nFeatures:\n- added thing\n\nFixes:\n- fixed bug",
			valid: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidCommitMessage(tc.msg)
			if got != tc.valid {
				t.Fatalf("isValidCommitMessage(%q) = %v, want %v", strings.TrimSpace(tc.msg), got, tc.valid)
			}
		})
	}
}
