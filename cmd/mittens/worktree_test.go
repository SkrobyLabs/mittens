package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a git repository at dir with one commit and returns dir.
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	return dir
}

func newWorktreeApp() *App {
	return &App{}
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// worktreePath placement
// ---------------------------------------------------------------------------

func TestWorktreePath_SiblingDefault(t *testing.T) {
	a := newWorktreeApp()
	repo := "/src/Quix.Portal.Frontend"
	got := a.worktreePath(repo, "wt-123")
	want := "/src/Quix.Portal.Frontend.wt-123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorktreePath_CustomRoot(t *testing.T) {
	a := newWorktreeApp()
	a.WorktreeRoot = "/tmp/run/worktrees"
	got := a.worktreePath("/src/Quix.Portal.Frontend", "wt-123")
	want := "/tmp/run/worktrees/Quix.Portal.Frontend"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorktreePath_CollisionHash(t *testing.T) {
	a := newWorktreeApp()
	a.WorktreeRoot = "/tmp/run/worktrees"
	// Simulate a previously allocated worktree at the unhashed path.
	a.worktrees = append(a.worktrees, &worktreeRecord{Path: "/tmp/run/worktrees/repo"})

	got := a.worktreePath("/other/repo", "wt-123")
	if got == "/tmp/run/worktrees/repo" {
		t.Fatalf("expected collision disambiguation, got base path %q", got)
	}
	if !strings.HasPrefix(got, "/tmp/run/worktrees/repo-") {
		t.Errorf("expected hashed suffix under root, got %q", got)
	}
	// Stable: same repo path always yields the same disambiguated path.
	if got2 := a.worktreePath("/other/repo", "wt-123"); got2 != got {
		t.Errorf("collision path not stable: %q vs %q", got, got2)
	}
}

// ---------------------------------------------------------------------------
// createWorktree
// ---------------------------------------------------------------------------

func TestCreateWorktree_DetachedDefault(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	a := newWorktreeApp()

	rec, err := a.createWorktree(repo, "wt-999", true)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Primary {
		t.Error("expected primary record")
	}
	if rec.Branch != "" {
		t.Errorf("expected detached (empty branch), got %q", rec.Branch)
	}
	// Sibling placement.
	if filepath.Dir(rec.Path) != filepath.Dir(repo) {
		t.Errorf("expected sibling placement, got %q", rec.Path)
	}
	if b := currentBranch(t, rec.Path); b != "HEAD" {
		t.Errorf("expected detached HEAD, got branch %q", b)
	}
	if len(a.worktrees) != 1 {
		t.Errorf("expected 1 tracked worktree, got %d", len(a.worktrees))
	}
}

func TestCreateWorktree_CustomRoot(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "Quix.Portal.Frontend"))
	root := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	a := newWorktreeApp()
	a.WorktreeRoot = root

	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Quix.Portal.Frontend")
	if rec.Path != want {
		t.Errorf("got %q, want %q", rec.Path, want)
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
}

func TestCreateWorktree_NewBranch(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	a := newWorktreeApp()
	a.WorktreeBranch = "feature/123-add-guards"

	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Branch != "feature/123-add-guards" {
		t.Errorf("got branch %q", rec.Branch)
	}
	if b := currentBranch(t, rec.Path); b != "feature/123-add-guards" {
		t.Errorf("worktree on branch %q, want feature/123-add-guards", b)
	}
	// Branch must persist in the source repo's metadata.
	if !branchExists(repo, "feature/123-add-guards") {
		t.Error("branch not created in source repo")
	}
}

func TestCreateWorktree_ExistingBranch(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	if out, err := exec.Command("git", "-C", repo, "branch", "existing").CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v: %s", err, out)
	}
	a := newWorktreeApp()
	a.WorktreeBranch = "existing"

	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if b := currentBranch(t, rec.Path); b != "existing" {
		t.Errorf("worktree on branch %q, want existing", b)
	}
}

func TestCreateWorktree_ExtraDirNonPrimary(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	a := newWorktreeApp()
	rec, err := a.createWorktree(repo, "wt-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Primary {
		t.Error("expected non-primary record")
	}
}

// ---------------------------------------------------------------------------
// cleanup + manifest
// ---------------------------------------------------------------------------

func TestCleanupWorktrees_KeepDirtyRemovesClean(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	a := newWorktreeApp() // default cleanup == keep-dirty
	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	a.cleanupWorktrees()
	if rec.Kept {
		t.Error("clean+unchanged worktree should be removed under keep-dirty")
	}
	if _, err := os.Stat(rec.Path); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone, stat err = %v", err)
	}
}

func TestCleanupWorktrees_KeepRetainsClean(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	a := newWorktreeApp()
	a.WorktreeCleanup = "keep"
	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	a.cleanupWorktrees()
	if !rec.Kept {
		t.Error("keep mode should retain even a clean worktree")
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Errorf("worktree dir should remain: %v", err)
	}
}

func TestCleanupWorktrees_KeepsDirty(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	a := newWorktreeApp()
	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	// Dirty the worktree.
	if err := os.WriteFile(filepath.Join(rec.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cleanupWorktrees()
	if !rec.Kept {
		t.Error("dirty worktree should be kept")
	}
	if !rec.Dirty {
		t.Error("record should reflect dirty state")
	}
}

func TestWriteWorktreeManifest(t *testing.T) {
	repo := initGitRepo(t, filepath.Join(t.TempDir(), "Quix.Portal.Frontend"))
	root := filepath.Join(t.TempDir(), "worktrees")
	manifestPath := filepath.Join(t.TempDir(), "nested", "worktrees.json")
	a := newWorktreeApp()
	a.WorktreeRoot = root
	a.WorktreeBranch = "feature/x"
	a.WorktreeManifest = manifestPath
	a.WorktreeCleanup = "keep"

	rec, err := a.createWorktree(repo, "wt-1", true)
	if err != nil {
		t.Fatal(err)
	}
	a.cleanupWorktrees()
	a.writeWorktreeManifest()

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	var m WorktreeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if len(m.Worktrees) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m.Worktrees))
	}
	e := m.Worktrees[0]
	if e.Repo != repo {
		t.Errorf("repo = %q, want %q", e.Repo, repo)
	}
	if e.Path != rec.Path {
		t.Errorf("worktree = %q, want %q", e.Path, rec.Path)
	}
	if e.Branch != "feature/x" {
		t.Errorf("branch = %q", e.Branch)
	}
	if e.StartHead == "" {
		t.Error("startHead empty")
	}
	if !e.Kept || !e.Primary {
		t.Errorf("expected kept+primary, got kept=%v primary=%v", e.Kept, e.Primary)
	}
}

func TestWriteWorktreeManifest_NoopWhenUnset(t *testing.T) {
	a := newWorktreeApp() // no manifest path, no worktrees
	a.writeWorktreeManifest()
	// Setting a path but having no worktrees must still be a no-op.
	a.WorktreeManifest = filepath.Join(t.TempDir(), "wt.json")
	a.writeWorktreeManifest()
	if _, err := os.Stat(a.WorktreeManifest); !os.IsNotExist(err) {
		t.Errorf("manifest should not be written with zero worktrees, stat err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// validation + flag parsing
// ---------------------------------------------------------------------------

func TestValidateWorktreeOptions(t *testing.T) {
	tests := []struct {
		name    string
		app     *App
		wantErr bool
	}{
		{"root without worktree", &App{WorktreeRoot: "/x"}, true},
		{"branch without worktree", &App{WorktreeBranch: "b"}, true},
		{"manifest without worktree", &App{WorktreeManifest: "/x.json"}, true},
		{"cleanup without worktree", &App{WorktreeCleanup: "keep"}, true},
		{"bad cleanup mode", &App{Worktree: true, WorktreeCleanup: "nuke"}, true},
		{"clean no longer valid", &App{Worktree: true, WorktreeCleanup: "clean"}, true},
		{"valid keep", &App{Worktree: true, WorktreeCleanup: "keep"}, false},
		{"valid keep-dirty", &App{Worktree: true, WorktreeCleanup: "keep-dirty"}, false},
		{"valid empty cleanup", &App{Worktree: true}, false},
		{"valid full set", &App{Worktree: true, WorktreeRoot: "/x", WorktreeBranch: "b", WorktreeManifest: "/m.json"}, false},
		{"no worktree no options", &App{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.app.validateWorktreeOptions()
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestParseFlags_WorktreeOrchestration(t *testing.T) {
	a := &App{}
	args := []string{
		"--worktree-root", "/tmp/wt",
		"--worktree-branch", "feature/123",
		"--worktree-manifest", "/tmp/wt.json",
		"--worktree-cleanup", "keep",
	}
	if err := a.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	if a.WorktreeRoot != "/tmp/wt" {
		t.Errorf("WorktreeRoot = %q", a.WorktreeRoot)
	}
	if a.WorktreeBranch != "feature/123" {
		t.Errorf("WorktreeBranch = %q", a.WorktreeBranch)
	}
	if a.WorktreeManifest != "/tmp/wt.json" {
		t.Errorf("WorktreeManifest = %q", a.WorktreeManifest)
	}
	if a.WorktreeCleanup != "keep" {
		t.Errorf("WorktreeCleanup = %q", a.WorktreeCleanup)
	}
	if len(a.ClaudeArgs) != 0 {
		t.Errorf("unexpected forwarded args: %v", a.ClaudeArgs)
	}
}
