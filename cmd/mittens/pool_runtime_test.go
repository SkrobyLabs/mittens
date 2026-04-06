package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestSpawnWorkerContainerBuildsKitchenWorkerRuntime(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "test-api-key")

	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", filepath.Join(tmp, ".mittens"))

	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	dockerDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	psArgsPath := filepath.Join(tmp, "docker-ps-args.txt")
	runArgsPath := filepath.Join(tmp, "docker-run-args.txt")
	dockerPath := filepath.Join(dockerDir, "docker")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  ps)
    printf '%%s\n' "$@" > '%s'
    exit 0
    ;;
  run)
    printf '%%s\n' "$@" > '%s'
    printf 'container-abc123\n'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, psArgsPath, runArgsPath)
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := &App{
		Provider:          ClaudeProvider(),
		Workspace:         workspace,
		WorkspaceMountSrc: workspace,
		ImageName:         "mittens-test",
		ImageTag:          "latest",
		NoBuild:           true,
		brokerPort:        43123,
		brokerToken:       "broker-token",
		poolSession:       "kitchen-workspace-123",
		poolStateDir:      filepath.Join(tmp, "pool-state"),
	}
	if err := os.MkdirAll(app.poolStateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	spec := pool.WorkerSpec{
		ID:            "w-1",
		Role:          "implementer",
		Provider:      "claude",
		Adapter:       "codex",
		Model:         "gpt-5.4",
		CPUs:          "2.5",
		Memory:        "3g",
		WorkspacePath: workspace,
		Environment: map[string]string{
			"MITTENS_SESSION_ID":   "kitchen-workspace-123",
			"MITTENS_KITCHEN_ADDR": "http://127.0.0.1:3900",
			"MITTENS_PLAN_ID":      "plan-123",
		},
	}

	containerName, containerID, err := app.spawnWorkerContainer(spec)
	if err != nil {
		t.Fatal(err)
	}
	if containerName != "mittens-kitchen-workspace-123-w-1" {
		t.Fatalf("containerName = %q, want %q", containerName, "mittens-kitchen-workspace-123-w-1")
	}
	if containerID != "container-abc123" {
		t.Fatalf("containerID = %q, want %q", containerID, "container-abc123")
	}

	psArgs, err := os.ReadFile(psArgsPath)
	if err != nil {
		t.Fatalf("read docker ps args: %v", err)
	}
	if got := string(psArgs); !strings.Contains(got, "name=^/mittens-kitchen-workspace-123-w-1$") {
		t.Fatalf("docker ps args = %q, want exact-name filter", got)
	}

	runArgsData, err := os.ReadFile(runArgsPath)
	if err != nil {
		t.Fatalf("read docker run args: %v", err)
	}
	runArgs := strings.Split(strings.TrimSpace(string(runArgsData)), "\n")

	if !argPairExists(runArgs, "--name", "mittens-kitchen-workspace-123-w-1") {
		t.Fatalf("docker run args missing expected container name: %v", runArgs)
	}
	if !argPairExists(runArgs, "-v", workspace+":"+workspace) {
		t.Fatalf("docker run args missing workspace mount: %v", runArgs)
	}
	if got := poolEnvValue(runArgs, "MITTENS_WORKER_ID"); got != "w-1" {
		t.Fatalf("MITTENS_WORKER_ID = %q, want %q", got, "w-1")
	}
	if got := poolEnvValue(runArgs, "MITTENS_KITCHEN_ADDR"); got != "http://127.0.0.1:3900" {
		t.Fatalf("MITTENS_KITCHEN_ADDR = %q, want %q", got, "http://127.0.0.1:3900")
	}
	if got := poolEnvValue(runArgs, "MITTENS_SESSION_ID"); got != "kitchen-workspace-123" {
		t.Fatalf("MITTENS_SESSION_ID = %q, want %q", got, "kitchen-workspace-123")
	}
	if got := poolEnvValue(runArgs, "MITTENS_PROVIDER"); got != "claude" {
		t.Fatalf("MITTENS_PROVIDER = %q, want %q", got, "claude")
	}
	if got := poolEnvValue(runArgs, "MITTENS_SKIP_PERMS_FLAG"); got != "--dangerously-skip-permissions" {
		t.Fatalf("MITTENS_SKIP_PERMS_FLAG = %q, want %q", got, "--dangerously-skip-permissions")
	}
	if got := poolEnvValue(runArgs, "MITTENS_ADAPTER"); got != "codex" {
		t.Fatalf("MITTENS_ADAPTER = %q, want %q", got, "codex")
	}
	if got := poolEnvValue(runArgs, "MITTENS_MODEL"); got != "gpt-5.4" {
		t.Fatalf("MITTENS_MODEL = %q, want %q", got, "gpt-5.4")
	}
	if got := poolEnvValue(runArgs, "MITTENS_PLAN_ID"); got != "plan-123" {
		t.Fatalf("MITTENS_PLAN_ID = %q, want %q", got, "plan-123")
	}
	if got := poolEnvValue(runArgs, "ANTHROPIC_API_KEY"); got != "test-api-key" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want %q", got, "test-api-key")
	}
	if got := poolCPULimit(runArgs); got != "2.5" {
		t.Fatalf("cpu limit = %q, want %q", got, "2.5")
	}
	if got := poolMemoryLimit(runArgs); got != "3g" {
		t.Fatalf("memory limit = %q, want %q", got, "3g")
	}

	wantLabels := []string{
		"mittens.pool=kitchen-workspace-123",
		"mittens.role=worker",
		"mittens.worker_id=w-1",
		"mittens.workspace=" + workspace,
	}
	gotLabels := poolLabels(runArgs)
	for _, want := range wantLabels {
		if !strings.Contains(strings.Join(gotLabels, "\n"), want) {
			t.Fatalf("docker run labels = %v, want %q", gotLabels, want)
		}
	}

	cfg := extractInitConfig(t, runArgs)
	if cfg.Broker.Port != 43123 {
		t.Fatalf("init config broker port = %d, want %d", cfg.Broker.Port, 43123)
	}
	if cfg.Broker.Token != "broker-token" {
		t.Fatalf("init config broker token = %q, want %q", cfg.Broker.Token, "broker-token")
	}
	if cfg.HostWorkspace != workspace {
		t.Fatalf("init config host workspace = %q, want %q", cfg.HostWorkspace, workspace)
	}
	if cfg.ContainerName != "mittens-kitchen-workspace-123-w-1" {
		t.Fatalf("init config containerName = %q, want %q", cfg.ContainerName, "mittens-kitchen-workspace-123-w-1")
	}
	if !cfg.Flags.PrintMode || !cfg.Flags.Yolo || !cfg.Flags.NoNotify {
		t.Fatalf("init config flags = %+v, want print/yolo/noNotify enabled", cfg.Flags)
	}
}

func TestSpawnWorkerContainerMountsCommonGitDirForLinkedWorktree(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)

	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", filepath.Join(tmp, ".mittens"))

	repo := filepath.Join(tmp, "repo")
	mustRunGit(t, tmp, "init", repo)
	mustRunGit(t, repo, "config", "user.name", "Test User")
	mustRunGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "add", "README.md")
	mustRunGit(t, repo, "commit", "-m", "init")

	worktree := filepath.Join(tmp, "worktrees", "task-1")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, repo, "worktree", "add", "--detach", worktree, "HEAD")

	dockerDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runArgsPath := filepath.Join(tmp, "docker-run-args.txt")
	dockerPath := filepath.Join(dockerDir, "docker")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  ps)
    exit 0
    ;;
  run)
    printf '%%s\n' "$@" > '%s'
    printf 'container-abc123\n'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, runArgsPath)
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	app := &App{
		Provider:          ClaudeProvider(),
		Workspace:         repo,
		WorkspaceMountSrc: repo,
		ImageName:         "mittens-test",
		ImageTag:          "latest",
		NoBuild:           true,
		brokerPort:        43123,
		brokerToken:       "broker-token",
		poolSession:       "kitchen-workspace-123",
		poolStateDir:      filepath.Join(tmp, "pool-state"),
	}
	if err := os.MkdirAll(app.poolStateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := app.spawnWorkerContainer(pool.WorkerSpec{
		ID:            "w-1",
		Role:          "implementer",
		Provider:      "claude",
		WorkspacePath: worktree,
		Environment: map[string]string{
			"MITTENS_SESSION_ID":   "kitchen-workspace-123",
			"MITTENS_KITCHEN_ADDR": "http://127.0.0.1:3900",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runArgsData, err := os.ReadFile(runArgsPath)
	if err != nil {
		t.Fatalf("read docker run args: %v", err)
	}
	runArgs := strings.Split(strings.TrimSpace(string(runArgsData)), "\n")

	commonGitDir := filepath.Join(repo, ".git")
	if !argPairExists(runArgs, "-v", commonGitDir+":"+commonGitDir) {
		t.Fatalf("docker run args missing linked-worktree common git dir mount %q: %v", commonGitDir, runArgs)
	}
}

func TestSpawnWorkerContainerGeminiHostname(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "test-gemini-key")

	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", filepath.Join(tmp, ".mittens"))

	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	dockerDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runArgsPath := filepath.Join(tmp, "docker-run-args.txt")
	dockerPath := filepath.Join(dockerDir, "docker")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  ps)
    exit 0
    ;;
  run)
    printf '%%s\n' "$@" > '%s'
    printf 'container-gemini123\n'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, runArgsPath)
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := GeminiProvider()
	if err := os.MkdirAll(filepath.Join(home, p.ConfigDir), 0o755); err != nil {
		t.Fatal(err)
	}

	app := &App{
		Provider:          p,
		Workspace:         workspace,
		WorkspaceMountSrc: workspace,
		ImageName:         "mittens-test",
		ImageTag:          "latest",
		NoBuild:           true,
		brokerPort:        43123,
		brokerToken:       "broker-token",
		poolSession:       "kitchen-workspace-gemini",
		poolStateDir:      filepath.Join(tmp, "pool-state"),
	}
	if err := os.MkdirAll(app.poolStateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := app.spawnWorkerContainer(pool.WorkerSpec{
		ID:            "w-g1",
		Role:          "implementer",
		Provider:      "gemini",
		WorkspacePath: workspace,
		Environment: map[string]string{
			"MITTENS_SESSION_ID":   "kitchen-workspace-gemini",
			"MITTENS_KITCHEN_ADDR": "http://127.0.0.1:3900",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	runArgsData, err := os.ReadFile(runArgsPath)
	if err != nil {
		t.Fatalf("read docker run args: %v", err)
	}
	runArgs := strings.Split(strings.TrimSpace(string(runArgsData)), "\n")

	if !argPairExists(runArgs, "--hostname", "gemini-cli") {
		t.Errorf("docker run args missing --hostname gemini-cli: %v", runArgs)
	}
	if got := poolEnvValue(runArgs, "GEMINI_API_KEY"); got != "test-gemini-key" {
		t.Errorf("GEMINI_API_KEY = %q, want %q", got, "test-gemini-key")
	}
	if got := poolEnvValue(runArgs, "MITTENS_PROVIDER"); got != "gemini" {
		t.Errorf("MITTENS_PROVIDER = %q, want %q", got, "gemini")
	}
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
