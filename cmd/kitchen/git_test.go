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

func TestGitManagerCommitChildIfDirty(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("impl", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("impl", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}

	// Clean worktree should be a no-op.
	committed, err := gm.CommitChildIfDirty("impl", "t1", "should-skip")
	if err != nil {
		t.Fatalf("CommitChildIfDirty clean: %v", err)
	}
	if committed {
		t.Fatalf("CommitChildIfDirty on clean worktree reported committed=true")
	}

	// Leave a dirty worker state: modify a tracked file AND add an
	// untracked one. Simulates a worker that edited code without
	// committing. Kitchen must auto-commit or the change is lost.
	writeFile(t, filepath.Join(wt, "README.md"), "base\nworker edit\n")
	writeFile(t, filepath.Join(wt, "new.txt"), "added\n")

	committed, err = gm.CommitChildIfDirty("impl", "t1", "worker work")
	if err != nil {
		t.Fatalf("CommitChildIfDirty dirty: %v", err)
	}
	if !committed {
		t.Fatalf("CommitChildIfDirty on dirty worktree reported committed=false")
	}

	// After auto-commit, MergeChild must advance the lineage branch so
	// the edits survive DiscardChild.
	if err := gm.MergeChild("impl", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}
	shaLineage, err := runGit(repo, "rev-parse", "kitchen/impl/lineage")
	if err != nil {
		t.Fatalf("rev-parse lineage: %v", err)
	}
	shaChild, err := runGit(repo, "rev-parse", "kitchen/impl/tasks/t1")
	if err != nil {
		t.Fatalf("rev-parse child: %v", err)
	}
	if strings.TrimSpace(shaLineage) != strings.TrimSpace(shaChild) {
		t.Fatalf("lineage %q != child %q after auto-commit merge", shaLineage, shaChild)
	}

	// Commit message and tree content should reflect the auto-commit.
	subject, err := runGit(repo, "log", "-1", "--format=%s", "kitchen/impl/lineage")
	if err != nil {
		t.Fatalf("log subject: %v", err)
	}
	if strings.TrimSpace(subject) != "worker work" {
		t.Fatalf("commit subject = %q, want %q", strings.TrimSpace(subject), "worker work")
	}
	files, err := runGit(repo, "show", "--name-only", "--format=", "kitchen/impl/lineage")
	if err != nil {
		t.Fatalf("show files: %v", err)
	}
	names := strings.Fields(strings.TrimSpace(files))
	wantFiles := map[string]bool{"README.md": false, "new.txt": false}
	for _, n := range names {
		if _, ok := wantFiles[n]; ok {
			wantFiles[n] = true
		}
	}
	for name, seen := range wantFiles {
		if !seen {
			t.Fatalf("auto-commit missing %s; commit files = %v", name, names)
		}
	}

	// Missing worktree should be a no-op, not an error.
	if _, err := runGit(repo, "worktree", "remove", "--force", wt); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}
	committed, err = gm.CommitChildIfDirty("impl", "t1", "skip")
	if err != nil {
		t.Fatalf("CommitChildIfDirty missing: %v", err)
	}
	if committed {
		t.Fatalf("CommitChildIfDirty missing worktree reported committed=true")
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

func TestGitManagerFixLineageMergeWorktreeAndFinalize(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("conflict", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}

	lineageWT, err := gm.CreateChildWorktree("conflict", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(lineageWT, "README.md"), "base\nlineage-change\n")
	mustRunGit(t, lineageWT, "add", "README.md")
	mustRunGit(t, lineageWT, "commit", "-m", "lineage edits README")
	if err := gm.MergeChild("conflict", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	// Advance base with a DIFFERENT change to the same file so the
	// lineage→base merge is guaranteed to conflict.
	writeFile(t, filepath.Join(repo, "README.md"), "base\nmain-change\n")
	mustRunGit(t, repo, "add", "README.md")
	mustRunGit(t, repo, "commit", "-m", "main edits README")

	worktreePath, fixBranch, err := gm.CreateFixLineageMergeWorktree("conflict", "main", "fix-1")
	if err != nil {
		t.Fatalf("CreateFixLineageMergeWorktree: %v", err)
	}
	if worktreePath == "" || fixBranch == "" {
		t.Fatal("expected a conflicted fix worktree, got clean")
	}
	status, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(status, "UU README.md") && !strings.Contains(status, "AA README.md") {
		t.Fatalf("expected README.md in conflicted state, status = %q", status)
	}

	// Simulate the worker resolving conflicts and committing.
	writeFile(t, filepath.Join(worktreePath, "README.md"), "base\nlineage-change\nmain-change\n")
	mustRunGit(t, worktreePath, "add", "README.md")
	mustRunGit(t, worktreePath, "commit", "-m", "resolve")

	mainHeadBefore, err := runGit(repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("rev-parse main before finalize: %v", err)
	}

	cleanupErr, err := gm.FinalizeFixLineageMerge("conflict", fixBranch, worktreePath)
	if err != nil {
		t.Fatalf("FinalizeFixLineageMerge: %v", err)
	}
	if cleanupErr != nil {
		t.Fatalf("FinalizeFixLineageMerge cleanup: %v", cleanupErr)
	}
	if _, err := runGit(repo, "rev-parse", "--verify", fixBranch); err == nil {
		t.Fatalf("fix branch %s should have been cleaned up", fixBranch)
	}

	// The base branch must be untouched by the fix-merge flow — only
	// the lineage advances. The operator still runs `kitchen merge`
	// to deliver the work.
	mainHeadAfter, err := runGit(repo, "rev-parse", "main")
	if err != nil {
		t.Fatalf("rev-parse main after finalize: %v", err)
	}
	if strings.TrimSpace(mainHeadBefore) != strings.TrimSpace(mainHeadAfter) {
		t.Fatalf("main head changed by fix-merge: before=%q after=%q", mainHeadBefore, mainHeadAfter)
	}

	// The lineage should now strictly contain base — a subsequent
	// lineage→base merge is a fast-forward.
	if _, err := runGit(repo, "merge-base", "--is-ancestor", "main", "kitchen/conflict/lineage"); err != nil {
		t.Fatalf("base should be ancestor of lineage after fix-merge, merge-base err: %v", err)
	}
}

func TestGitManagerFixLineageMergeWorktreeNoOpWhenClean(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("clean", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	lineageWT, err := gm.CreateChildWorktree("clean", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(lineageWT, "new.txt"), "content\n")
	mustRunGit(t, lineageWT, "add", "new.txt")
	mustRunGit(t, lineageWT, "commit", "-m", "add new file")
	if err := gm.MergeChild("clean", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	worktreePath, fixBranch, err := gm.CreateFixLineageMergeWorktree("clean", "main", "fix-1")
	if err != nil {
		t.Fatalf("CreateFixLineageMergeWorktree: %v", err)
	}
	if worktreePath != "" || fixBranch != "" {
		t.Fatalf("expected no-op, got worktreePath=%q fixBranch=%q", worktreePath, fixBranch)
	}
}

func TestGitManagerMergeLineageSyncsCheckedOutBaseWorktree(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	// Base is main. Create the lineage off main, add a commit to it,
	// then merge back while main is still the checked-out branch in
	// the main repo. The operator's working tree must be synced to
	// the new HEAD so the next `git status` doesn't show phantom
	// deletions for files added by the merge.
	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("adds-file", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("adds-file", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wt, "new.txt"), "new\n")
	mustRunGit(t, wt, "add", "new.txt")
	mustRunGit(t, wt, "commit", "-m", "add new.txt")
	if err := gm.MergeChild("adds-file", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	// Now merge the lineage into main — which is checked out in the
	// main repo's working tree.
	if err := gm.MergeLineage("adds-file", "main", "squash"); err != nil {
		t.Fatalf("MergeLineage: %v", err)
	}

	// Working tree should now contain new.txt.
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Fatalf("expected new.txt to appear in main repo worktree, stat err = %v", err)
	}
	// And `git status` must be clean — no phantom deletions.
	status, err := runGit(repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		t.Fatalf("git status should be clean after merge sync, got: %q", status)
	}
}

func TestGitManagerMergeLineagePreservesDirtyOperatorWorktree(t *testing.T) {
	repo := initGitRepo(t)
	worktrees := filepath.Join(t.TempDir(), "worktrees")

	gm, err := NewGitManager(repo, worktrees)
	if err != nil {
		t.Fatalf("NewGitManager: %v", err)
	}
	if err := gm.CreateLineageBranch("adds-file", "HEAD"); err != nil {
		t.Fatalf("CreateLineageBranch: %v", err)
	}
	wt, err := gm.CreateChildWorktree("adds-file", "t1")
	if err != nil {
		t.Fatalf("CreateChildWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wt, "new.txt"), "new\n")
	mustRunGit(t, wt, "add", "new.txt")
	mustRunGit(t, wt, "commit", "-m", "add new.txt")
	if err := gm.MergeChild("adds-file", "t1"); err != nil {
		t.Fatalf("MergeChild: %v", err)
	}

	// Operator has uncommitted changes in the main repo.
	writeFile(t, filepath.Join(repo, "README.md"), "operator edit\n")

	if err := gm.MergeLineage("adds-file", "main", "squash"); err != nil {
		t.Fatalf("MergeLineage: %v", err)
	}

	// Operator's edit must still be present (we did not reset over it).
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if strings.TrimSpace(string(data)) != "operator edit" {
		t.Fatalf("operator edit lost: README.md = %q", string(data))
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
