package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitManagerCreateLineageAndChildWorktree(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	wt, err := gm.CreateChildWorktree("parser-errors", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("worktree .git missing: %v", err)
	}
	branch, err := runGit(wt, "branch", "--show-current")
	if err != nil {
		t.Fatalf("branch --show-current: %v", err)
	}
	if got := strings.TrimSpace(branch); got != "kitchen/parser-errors/tasks/t1" {
		t.Fatalf("branch = %q, want kitchen/parser-errors/tasks/t1", got)
	}
}

func TestGitManagerMergeChildFastForward(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("parser-errors", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}

	writeFile(t, filepath.Join(wt, "child.txt"), "hello\n")
	mustRunGit(t, wt, "add", "child.txt")
	mustRunGit(t, wt, "commit", "-m", "child change")

	if err := gm.MergeChild("parser-errors", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	shaBranch, err := runGit(repo, "rev-parse", "kitchen/parser-errors/lineage")
	if err != nil {
		t.Fatalf("rev-parse lineage branch: %v", err)
	}
	shaChild, err := runGit(repo, "rev-parse", "kitchen/parser-errors/tasks/t1")
	if err != nil {
		t.Fatalf("rev-parse child branch: %v", err)
	}
	if strings.TrimSpace(shaBranch) != strings.TrimSpace(shaChild) {
		t.Fatalf("lineage branch = %q, child branch = %q; want fast-forwarded lineage", shaBranch, shaChild)
	}
}

func TestGitManagerAnchorDrift(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	head, err := runGit(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	head = strings.TrimSpace(head)

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("parser-errors", head); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	mustRunGit(t, repo, "checkout", "kitchen/parser-errors/lineage")
	writeFile(t, filepath.Join(repo, "lineage.txt"), "drift\n")
	mustRunGit(t, repo, "add", "lineage.txt")
	mustRunGit(t, repo, "commit", "-m", "lineage change")
	mustRunGit(t, repo, "checkout", "main")

	drift, err := gm.AnchorDrift("parser-errors", head)
	if err != nil {
		t.Fatalf("AnchorDrift: %v", err)
	}
	if drift != 1 {
		t.Fatalf("drift = %d, want 1", drift)
	}
}

func TestGitManagerMergeCheckDetectsConflict(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	writeFile(t, filepath.Join(repo, "shared.txt"), "main change\n")
	mustRunGit(t, repo, "add", "shared.txt")
	mustRunGit(t, repo, "commit", "-m", "main change")

	mustRunGit(t, repo, "checkout", "kitchen/parser-errors/lineage")
	writeFile(t, filepath.Join(repo, "shared.txt"), "lineage change\n")
	mustRunGit(t, repo, "add", "shared.txt")
	mustRunGit(t, repo, "commit", "-m", "lineage change")
	mustRunGit(t, repo, "checkout", "main")

	clean, conflicts, err := gm.MergeCheck("parser-errors", "main")
	if err != nil {
		t.Fatalf("MergeCheck: %v", err)
	}
	if clean {
		t.Fatal("expected merge check to report conflict")
	}
	if len(conflicts) != 1 || conflicts[0] != "shared.txt" {
		t.Fatalf("conflicts = %+v, want [shared.txt]", conflicts)
	}
}

func TestGitManagerMergeCheckAllowsSlashInBaseBranch(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	mustRunGit(t, repo, "checkout", "-b", "feat/kitchen")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	clean, conflicts, err := gm.MergeCheck("parser-errors", "feat/kitchen")
	if err != nil {
		t.Fatalf("MergeCheck: %v", err)
	}
	if !clean {
		t.Fatalf("clean = false, conflicts = %+v, want clean merge", conflicts)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", conflicts)
	}
}

func TestGitManagerMergeLineageSquashNoOpWhenBaseAlreadyContainsChanges(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	mustRunGit(t, repo, "checkout", "-b", "feat/kitchen")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("parser-errors", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	writeFile(t, filepath.Join(repo, "base-only.txt"), "already on base\n")
	mustRunGit(t, repo, "add", "base-only.txt")
	mustRunGit(t, repo, "commit", "-m", "advance base branch")

	before, err := runGit(repo, "rev-parse", "feat/kitchen")
	if err != nil {
		t.Fatalf("rev-parse before: %v", err)
	}
	if err := gm.MergeLineage("parser-errors", "feat/kitchen", "squash"); err != nil {
		t.Fatalf("MergeLineage: %v", err)
	}
	after, err := runGit(repo, "rev-parse", "feat/kitchen")
	if err != nil {
		t.Fatalf("rev-parse after: %v", err)
	}
	if strings.TrimSpace(before) != strings.TrimSpace(after) {
		t.Fatalf("base branch changed on no-op squash merge: before=%q after=%q", before, after)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRunGit(t, repo, "init", "-b", "main")
	mustRunGit(t, repo, "config", "user.name", "Test User")
	mustRunGit(t, repo, "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(repo, "README.md"), "base\n")
	mustRunGit(t, repo, "add", "README.md")
	mustRunGit(t, repo, "commit", "-m", "initial")
	return repo
}

func mustRunGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	if _, err := runGit(repo, args...); err != nil {
		t.Fatalf("runGit(%q): %v", strings.Join(args, " "), err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
