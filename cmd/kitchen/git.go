package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type GitManager struct {
	repoPath     string
	worktreeBase string
	mu           sync.Mutex
}

func NewGitManager(repoPath, worktreeBase string) (*GitManager, error) {
	if strings.TrimSpace(repoPath) == "" {
		return nil, fmt.Errorf("repo path must not be empty")
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}
	root, err := runGit(absRepo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("resolve git root: %w", err)
	}
	root = strings.TrimSpace(root)
	if worktreeBase == "" {
		return nil, fmt.Errorf("worktree base must not be empty")
	}
	if err := os.MkdirAll(worktreeBase, 0755); err != nil {
		return nil, fmt.Errorf("create worktree base: %w", err)
	}
	return &GitManager{
		repoPath:     root,
		worktreeBase: worktreeBase,
	}, nil
}

func (g *GitManager) CreateLineageBranch(lineage, anchorCommit string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if anchorCommit == "" {
		anchorCommit = "HEAD"
	}
	if err := validatePathComponent("lineage", lineage); err != nil {
		return err
	}
	branch := lineageBranchName(lineage)
	exists, err := g.branchExists(branch)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = runGit(g.repoPath, "branch", branch, anchorCommit)
	return err
}

func (g *GitManager) CreateChildWorktree(lineage, taskID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return "", err
	}
	if err := validatePathComponent("task ID", taskID); err != nil {
		return "", err
	}
	lineageBranch := lineageBranchName(lineage)
	childBranch := childBranchName(lineage, taskID)
	worktreePath := filepath.Join(g.worktreeBase, lineage, taskID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", fmt.Errorf("create lineage worktree dir: %w", err)
	}
	if _, err := os.Stat(worktreePath); err == nil {
		if _, err := runGit(g.repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
			if !isRecoverableWorktreeRemoveError(err) {
				return "", err
			}
			_, _ = runGit(g.repoPath, "worktree", "prune")
		}
		_ = os.RemoveAll(worktreePath)
		_, _ = runGit(g.repoPath, "worktree", "prune")
	}

	_, err := runGit(g.repoPath, "worktree", "add", "--detach", worktreePath, lineageBranch)
	if err != nil {
		return "", err
	}
	if _, err := runGit(worktreePath, "checkout", "-B", childBranch); err != nil {
		_, _ = runGit(g.repoPath, "worktree", "remove", "--force", worktreePath)
		return "", err
	}
	return worktreePath, nil
}

// CommitChildIfDirty stages and commits any changes left in a child
// worktree by the worker. Kitchen does not tell workers to `git commit`
// themselves, so without this step workers that only edit files would
// see MergeChild no-op (child branch still at lineage HEAD) and
// DiscardChild would then erase the uncommitted work. Returns (true,
// nil) when a commit was created, (false, nil) when the worktree was
// clean or missing.
func (g *GitManager) CommitChildIfDirty(lineage, taskID, message string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return false, err
	}
	if err := validatePathComponent("task ID", taskID); err != nil {
		return false, err
	}
	worktreePath := filepath.Join(g.worktreeBase, lineage, taskID)
	if _, err := os.Stat(worktreePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat child worktree: %w", err)
	}
	status, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) == "" {
		return false, nil
	}
	if _, err := runGit(worktreePath, "add", "-A"); err != nil {
		return false, err
	}
	if strings.TrimSpace(message) == "" {
		message = "Kitchen task " + taskID
	}
	if _, err := runGit(worktreePath, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// commitWorktreeIfDirty stages and commits any pending changes in the
// given worktree path directly, bypassing the lineage/taskID lookup
// that CommitChildIfDirty uses. It's the fallback for worker-managed
// worktrees (e.g. the fix-lineage-merge worktree) where the path does
// not match the standard lineage/<task-id> layout.
func (g *GitManager) commitWorktreeIfDirty(worktreePath, message string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := os.Stat(worktreePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat worktree: %w", err)
	}
	status, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) == "" {
		return false, nil
	}
	if _, err := runGit(worktreePath, "add", "-A"); err != nil {
		return false, err
	}
	if strings.TrimSpace(message) == "" {
		message = "Kitchen auto-commit"
	}
	if _, err := runGit(worktreePath, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

func (g *GitManager) MergeChild(lineage, taskID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return err
	}
	if err := validatePathComponent("task ID", taskID); err != nil {
		return err
	}
	lineageBranch := lineageBranchName(lineage)
	childBranch := childBranchName(lineage, taskID)

	ff, err := g.isAncestor(lineageBranch, childBranch)
	if err != nil {
		return err
	}
	if ff {
		return g.forceBranchTo(lineageBranch, childBranch)
	}

	head, err := g.mergeIntoTemp(lineageBranch, childBranch, false)
	if err != nil {
		return err
	}
	return g.updateBranchRef(lineageBranch, head)
}

func (g *GitManager) DiscardChild(lineage, taskID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return err
	}
	if err := validatePathComponent("task ID", taskID); err != nil {
		return err
	}
	worktreePath := filepath.Join(g.worktreeBase, lineage, taskID)
	if _, err := os.Stat(worktreePath); err == nil {
		if _, err := runGit(g.repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
			if !isRecoverableWorktreeRemoveError(err) {
				return err
			}
			_, _ = runGit(g.repoPath, "worktree", "prune")
		}
	}
	_ = os.RemoveAll(worktreePath)
	_, _ = runGit(g.repoPath, "worktree", "prune")
	_, err := runGit(g.repoPath, "branch", "-D", childBranchName(lineage, taskID))
	if err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "branch '-D'") && !strings.Contains(err.Error(), "branch '"+childBranchName(lineage, taskID)+"' not found") {
		return err
	}
	return nil
}

func isRecoverableWorktreeRemoveError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "validation failed") ||
		strings.Contains(msg, "not a .git file") ||
		strings.Contains(msg, "is not a working tree")
}

func (g *GitManager) MergeLineage(lineage, baseBranch, mode string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return err
	}
	if err := g.validateBranchName("base branch", baseBranch); err != nil {
		return err
	}
	lineageBranch := lineageBranchName(lineage)
	hasChanges, err := g.lineageHasPendingChanges(baseBranch, lineageBranch)
	if err != nil {
		return err
	}
	if !hasChanges {
		return fmt.Errorf("lineage %s has no changes to merge into %s", lineage, baseBranch)
	}

	squash := strings.EqualFold(mode, "squash")

	if !squash {
		ff, err := g.isAncestor(baseBranch, lineageBranch)
		if err != nil {
			return err
		}
		if ff {
			return g.forceBranchTo(baseBranch, lineageBranch)
		}
	}

	head, err := g.mergeIntoTemp(baseBranch, lineageBranch, squash)
	if err != nil {
		return err
	}
	return g.advanceBranchToSHA(baseBranch, head)
}

func (g *GitManager) ReapplyLineageOnBase(lineage, baseBranch string) (bool, []string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return false, nil, err
	}
	if err := g.validateBranchName("base branch", baseBranch); err != nil {
		return false, nil, err
	}
	lineageBranch := lineageBranchName(lineage)

	upToDate, err := g.isAncestor(baseBranch, lineageBranch)
	if err != nil {
		return false, nil, err
	}
	if upToDate {
		return true, nil, nil
	}

	head, err := g.mergeIntoTemp(lineageBranch, baseBranch, false)
	if err != nil {
		conflicts := parseConflictList(err.Error())
		if len(conflicts) > 0 {
			return false, conflicts, nil
		}
		return false, nil, err
	}
	return true, nil, g.advanceBranchToSHA(lineageBranch, head)
}

func (g *GitManager) PreviewMergeLineage(lineage, baseBranch, mode string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return "", err
	}
	if err := g.validateBranchName("base branch", baseBranch); err != nil {
		return "", err
	}
	lineageBranch := lineageBranchName(lineage)
	hasChanges, err := g.lineageHasPendingChanges(baseBranch, lineageBranch)
	if err != nil {
		return "", err
	}
	if !hasChanges {
		return "", fmt.Errorf("lineage %s has no changes to merge into %s", lineage, baseBranch)
	}

	squash := strings.EqualFold(mode, "squash")

	if !squash {
		ff, err := g.isAncestor(baseBranch, lineageBranch)
		if err != nil {
			return "", err
		}
		if ff {
			sha, err := runGit(g.repoPath, "rev-parse", lineageBranch)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(sha), nil
		}
	}

	return g.mergeIntoTemp(baseBranch, lineageBranch, squash)
}

func (g *GitManager) MergeCheck(lineage, baseBranch string) (bool, []string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return false, nil, err
	}
	if err := g.validateBranchName("base branch", baseBranch); err != nil {
		return false, nil, err
	}
	lineageBranch := lineageBranchName(lineage)
	hasChanges, err := g.lineageHasPendingChanges(baseBranch, lineageBranch)
	if err != nil {
		return false, nil, err
	}
	if !hasChanges {
		return false, nil, fmt.Errorf("lineage %s has no changes to merge into %s", lineage, baseBranch)
	}

	ff, err := g.isAncestor(baseBranch, lineageBranch)
	if err != nil {
		return false, nil, err
	}
	if ff {
		return true, nil, nil
	}

	tmpDir, err := os.MkdirTemp(g.worktreeBase, ".merge-check-*")
	if err != nil {
		return false, nil, fmt.Errorf("create merge check dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if _, err := runGit(g.repoPath, "worktree", "add", "--detach", tmpDir, baseBranch); err != nil {
		return false, nil, err
	}
	defer func() {
		_, _ = runGit(g.repoPath, "worktree", "remove", "--force", tmpDir)
	}()

	_, err = runGit(tmpDir, "merge", "--no-commit", "--no-ff", lineageBranch)
	if err == nil {
		_, _ = runGit(tmpDir, "merge", "--abort")
		return true, nil, nil
	}
	conflicts, conflictErr := conflictFiles(tmpDir)
	if conflictErr != nil {
		return false, nil, conflictErr
	}
	_, _ = runGit(tmpDir, "merge", "--abort")
	return false, conflicts, nil
}

func (g *GitManager) lineageHasPendingChanges(baseBranch, lineageBranch string) (bool, error) {
	mergeBase, err := runGit(g.repoPath, "merge-base", baseBranch, lineageBranch)
	if err != nil {
		return false, err
	}
	_, err = runGit(g.repoPath, "diff", "--quiet", strings.TrimSpace(mergeBase)+".."+lineageBranch)
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// CreateFixLineageMergeWorktree prepares a worker worktree pre-loaded
// with an in-progress merge of base INTO lineage that hit conflicts,
// so an AI worker can resolve it and commit on the lineage branch
// itself. The worktree starts at the LINEAGE branch HEAD, runs
// `git merge --no-ff --no-commit <baseBranch>`, and leaves the
// index/WT in its conflicted state. The worker resolves, `git add`s,
// and `git commit`s, producing a merge commit on the lineage whose
// parents are lineage + base. The scheduler then fast-forwards the
// lineage branch onto that commit — the base branch is left alone,
// and a subsequent `kitchen merge` / `M merge` becomes a trivial
// fast-forward because the lineage now strictly contains base.
//
// Returns the worktree path and the fix branch name (a dedicated
// `kitchen/<lineage>/fix-merge/<ts>` head that the worker commits
// onto). If the merge turns out to be clean (no conflicts), the
// worktree is torn down and ("", "", nil) is returned so the caller
// can treat it as a no-op.
func (g *GitManager) CreateFixLineageMergeWorktree(lineage, baseBranch, fixTaskID string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return "", "", err
	}
	if err := g.validateBranchName("base branch", baseBranch); err != nil {
		return "", "", err
	}
	if err := validatePathComponent("fix task ID", fixTaskID); err != nil {
		return "", "", err
	}
	lineageBranch := lineageBranchName(lineage)
	fixBranch := "kitchen/" + lineage + "/fix-merge/" + fixTaskID
	worktreePath := filepath.Join(g.worktreeBase, lineage, "fix-merge-"+fixTaskID)

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", "", fmt.Errorf("create fix-merge worktree dir: %w", err)
	}
	if _, err := os.Stat(worktreePath); err == nil {
		if _, err := runGit(g.repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
			if !isRecoverableWorktreeRemoveError(err) {
				return "", "", err
			}
			_, _ = runGit(g.repoPath, "worktree", "prune")
		}
		_ = os.RemoveAll(worktreePath)
		_, _ = runGit(g.repoPath, "worktree", "prune")
	}

	// Fork the worktree off the lineage tip and carry edits on a
	// dedicated fix branch so the worker can't accidentally rewrite
	// the live lineage branch while it's running.
	if _, err := runGit(g.repoPath, "worktree", "add", "--detach", worktreePath, lineageBranch); err != nil {
		return "", "", err
	}
	if _, err := runGit(worktreePath, "checkout", "-B", fixBranch); err != nil {
		_, _ = runGit(g.repoPath, "worktree", "remove", "--force", worktreePath)
		return "", "", err
	}

	_, err := runGit(worktreePath, "merge", "--no-ff", "--no-commit", baseBranch)
	if err == nil {
		// Merge was clean after all — no fix needed. Tear down.
		_, _ = runGit(worktreePath, "merge", "--abort")
		_, _ = runGit(g.repoPath, "worktree", "remove", "--force", worktreePath)
		_, _ = runGit(g.repoPath, "branch", "-D", fixBranch)
		return "", "", nil
	}
	// Leave the worktree in its conflicted state for the worker.
	return worktreePath, fixBranch, nil
}

// FinalizeFixLineageMerge fast-forwards the lineage branch onto the
// resolved fix-branch head. The resolution commit has lineage + base
// as parents, so advancing lineage to it means lineage now strictly
// contains base — a subsequent `kitchen merge` into base becomes a
// fast-forward. The base branch is left untouched.
//
// Advancing the lineage ref is the only step that MUST succeed; the
// temporary fix branch and worktree are cleaned up on a best-effort
// basis (worker containers can leave root-owned files behind, which
// makes `git worktree remove` fail with EACCES) and any cleanup
// failure is returned via the second result so the caller can log
// it without losing the successful ref advance.
func (g *GitManager) FinalizeFixLineageMerge(lineage, fixBranch, worktreePath string) (cleanupErr error, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return nil, err
	}
	sha, err := runGit(g.repoPath, "rev-parse", fixBranch)
	if err != nil {
		return nil, fmt.Errorf("rev-parse fix branch: %w", err)
	}
	if err := g.advanceBranchToSHA(lineageBranchName(lineage), strings.TrimSpace(sha)); err != nil {
		return nil, fmt.Errorf("advance lineage to fix head: %w", err)
	}
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		if _, removeErr := runGit(g.repoPath, "worktree", "remove", "--force", worktreePath); removeErr != nil {
			if !isRecoverableWorktreeRemoveError(removeErr) {
				cleanupErr = fmt.Errorf("worktree remove %s: %w", worktreePath, removeErr)
			}
			_, _ = runGit(g.repoPath, "worktree", "prune")
		}
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			if cleanupErr == nil {
				cleanupErr = fmt.Errorf("rm worktree %s: %w", worktreePath, rmErr)
			}
		}
	}
	if _, branchErr := runGit(g.repoPath, "branch", "-D", fixBranch); branchErr != nil {
		// Branch delete failing on its own is rare but shouldn't
		// block the ref advance either.
		if !strings.Contains(branchErr.Error(), "not found") && cleanupErr == nil {
			cleanupErr = fmt.Errorf("delete fix branch %s: %w", fixBranch, branchErr)
		}
	}
	return cleanupErr, nil
}

func (g *GitManager) CleanOrphans(activeTasks map[string]bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	entries, err := os.ReadDir(g.worktreeBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read worktree base: %w", err)
	}
	for _, lineageEntry := range entries {
		if !lineageEntry.IsDir() || strings.HasPrefix(lineageEntry.Name(), ".") {
			continue
		}
		lineageDir := filepath.Join(g.worktreeBase, lineageEntry.Name())
		taskEntries, err := os.ReadDir(lineageDir)
		if err != nil {
			return fmt.Errorf("read lineage worktrees: %w", err)
		}
		for _, taskEntry := range taskEntries {
			if !taskEntry.IsDir() {
				continue
			}
			taskID := taskEntry.Name()
			if activeTasks[taskID] {
				continue
			}
			if err := g.discardChildLocked(lineageEntry.Name(), taskID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ConflictDiff returns the diff of the given files between baseSHA and lineageBranch.
// It shows what the lineage branch changed in the conflicting files since the task's
// base commit, giving context for why the merge conflict occurred.
func (g *GitManager) ConflictDiff(baseSHA, lineageBranch string, files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	args := append([]string{"diff", baseSHA + ".." + lineageBranch, "--"}, files...)
	return runGit(g.repoPath, args...)
}

func (g *GitManager) AnchorDrift(lineage string, anchorCommit string) (int, error) {
	if err := validatePathComponent("lineage", lineage); err != nil {
		return 0, err
	}
	if strings.TrimSpace(anchorCommit) == "" {
		return 0, fmt.Errorf("anchor commit must not be empty")
	}
	out, err := runGit(g.repoPath, "rev-list", "--count", anchorCommit+".."+lineageBranchName(lineage))
	if err != nil {
		return 0, err
	}
	var count int
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &count); err != nil {
		return 0, fmt.Errorf("parse anchor drift: %w", err)
	}
	return count, nil
}

func (g *GitManager) validateBranchName(kind, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	cmd.Dir = g.repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s is not a valid git branch name %q: %s", kind, branch, msg)
	}
	return nil
}

func (g *GitManager) discardChildLocked(lineage, taskID string) error {
	worktreePath := filepath.Join(g.worktreeBase, lineage, taskID)
	if _, err := os.Stat(worktreePath); err == nil {
		if _, err := runGit(g.repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(worktreePath)
	_, err := runGit(g.repoPath, "branch", "-D", childBranchName(lineage, taskID))
	if err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "not a valid branch point") {
		return err
	}
	return nil
}

func (g *GitManager) DeleteLineageBranch(lineage string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err := validatePathComponent("lineage", lineage); err != nil {
		return err
	}
	branch := lineageBranchName(lineage)
	exists, err := g.branchExists(branch)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err = runGit(g.repoPath, "branch", "-D", branch)
	return err
}

func (g *GitManager) branchExists(branch string) (bool, error) {
	_, err := runGit(g.repoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (g *GitManager) isAncestor(ancestor, descendant string) (bool, error) {
	_, err := runGit(g.repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (g *GitManager) forceBranchTo(branch, target string) error {
	sha, err := runGit(g.repoPath, "rev-parse", target)
	if err != nil {
		return err
	}
	return g.advanceBranchToSHA(branch, strings.TrimSpace(sha))
}

func (g *GitManager) updateBranchRef(branch, sha string) error {
	_, err := runGit(g.repoPath, "update-ref", "refs/heads/"+branch, sha)
	return err
}

// advanceBranchToSHA points `branch` at `sha` and, when HEAD is the
// symbolic ref for that branch in the main repo, also updates the
// working tree and index so the operator's checkout stays in sync.
// Plain `update-ref` would move the ref without touching the WT,
// leaving phantom "deleted" entries in the operator's next
// `git status` that get swept into unrelated commits. Dirty worktrees
// are preserved (with a warning logged) so we never clobber
// operator-in-flight work.
func (g *GitManager) advanceBranchToSHA(branch, sha string) error {
	headRef, headErr := runGit(g.repoPath, "symbolic-ref", "--quiet", "HEAD")
	isCheckedOut := false
	if headErr == nil {
		isCheckedOut = strings.TrimSpace(headRef) == "refs/heads/"+branch
	} else {
		var exitErr *exec.ExitError
		if !errors.As(headErr, &exitErr) || exitErr.ExitCode() != 1 {
			return headErr
		}
	}
	if !isCheckedOut {
		return g.updateBranchRef(branch, sha)
	}
	dirty, err := hasWorktreeChanges(g.repoPath)
	if err != nil {
		return err
	}
	if dirty {
		if err := g.updateBranchRef(branch, sha); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "kitchen: branch %s advanced by merge, but the working tree has uncommitted changes; run `git reset --hard %s` when ready to sync\n", branch, branch)
		return nil
	}
	_, err = runGit(g.repoPath, "reset", "--hard", sha)
	return err
}

func (g *GitManager) mergeIntoTemp(targetBranch, sourceBranch string, squash bool) (string, error) {
	tmpDir, err := os.MkdirTemp(g.worktreeBase, ".merge-*")
	if err != nil {
		return "", fmt.Errorf("create merge temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if _, err := runGit(g.repoPath, "worktree", "add", "--detach", tmpDir, targetBranch); err != nil {
		return "", err
	}
	defer func() {
		_, _ = runGit(g.repoPath, "worktree", "remove", "--force", tmpDir)
	}()

	mergeArgs := []string{"merge"}
	if squash {
		mergeArgs = append(mergeArgs, "--squash", sourceBranch)
	} else {
		mergeArgs = append(mergeArgs, "--no-ff", "--no-edit", sourceBranch)
	}
	_, err = runGit(tmpDir, mergeArgs...)
	if err != nil {
		conflicts, conflictErr := conflictFiles(tmpDir)
		if conflictErr == nil && len(conflicts) > 0 {
			sort.Strings(conflicts)
			if !squash {
				_, _ = runGit(tmpDir, "merge", "--abort")
			} else {
				_, _ = runGit(tmpDir, "reset", "--hard", "HEAD")
			}
			return "", fmt.Errorf("merge conflicts: %s", strings.Join(conflicts, ", "))
		}
		return "", err
	}
	if squash {
		dirty, err := hasWorktreeChanges(tmpDir)
		if err != nil {
			return "", err
		}
		if !dirty {
			head, err := runGit(tmpDir, "rev-parse", "HEAD")
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(head), nil
		}
		if _, err := runGit(tmpDir, "commit", "-m", "Squash merge "+sourceBranch+" into "+targetBranch); err != nil {
			return "", err
		}
	}
	head, err := runGit(tmpDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

func hasWorktreeChanges(repoPath string) (bool, error) {
	if _, err := runGit(repoPath, "diff", "--cached", "--quiet"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return true, nil
		}
		return false, err
	}
	if _, err := runGit(repoPath, "diff", "--quiet"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func conflictFiles(repoPath string) ([]string, error) {
	out, err := runGit(repoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func lineageBranchName(lineage string) string {
	// Git refs cannot contain both "kitchen/<lineage>" and
	// "kitchen/<lineage>/<task>" simultaneously, so the lineage branch gets a
	// dedicated leaf name under the lineage namespace.
	return "kitchen/" + lineage + "/lineage"
}

func childBranchName(lineage, taskID string) string {
	return "kitchen/" + lineage + "/tasks/" + taskID
}

func runGit(repoPath string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Kitchen",
		"GIT_AUTHOR_EMAIL=kitchen@localhost",
		"GIT_COMMITTER_NAME=Kitchen",
		"GIT_COMMITTER_EMAIL=kitchen@localhost",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}
