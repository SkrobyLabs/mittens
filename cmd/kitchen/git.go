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

	ff, err := g.isAncestor(baseBranch, lineageBranch)
	if err != nil {
		return err
	}
	if ff {
		return g.forceBranchTo(baseBranch, lineageBranch)
	}

	squash := strings.EqualFold(mode, "squash")
	head, err := g.mergeIntoTemp(baseBranch, lineageBranch, squash)
	if err != nil {
		return err
	}
	return g.updateBranchRef(baseBranch, head)
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

	squash := strings.EqualFold(mode, "squash")
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
	return g.updateBranchRef(branch, strings.TrimSpace(sha))
}

func (g *GitManager) updateBranchRef(branch, sha string) error {
	_, err := runGit(g.repoPath, "update-ref", "refs/heads/"+branch, sha)
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
