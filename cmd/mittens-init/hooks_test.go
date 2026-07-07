package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupNotificationHooksPreservesExistingHooks(t *testing.T) {
	dir := t.TempDir()
	settingsFile := filepath.Join(dir, "settings.json")

	// A plugin-registered PreToolUse hook that must survive re-init, plus a
	// stale notify command that must be refreshed.
	seed := `{
		"hooks": {
			"PreToolUse": [{"matcher":"Bash","hooks":[{"type":"command","command":"~/.claude/hooks/git-commit-validator.sh"}]}],
			"Notification": [{"hooks":[{"type":"command","command":"OLD notify command"}]}]
		}
	}`
	if err := os.WriteFile(settingsFile, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		AIDir:            dir,
		AISettingsFile:   "settings.json",
		AISettingsFormat: "json",
		AIStopHookEvent:  "Stop",
		BrokerPort:       "9999",
	}

	setupNotificationHooks(cfg)

	data, err := os.ReadFile(settingsFile)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	hooks, ok := obj["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected hooks map, got %T", obj["hooks"])
	}

	// The plugin hook must still be there.
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Errorf("PreToolUse hook was dropped: %s", data)
	}
	// mittens' own hooks must be present and refreshed (no stale command).
	if _, ok := hooks["Notification"]; !ok {
		t.Errorf("Notification hook missing")
	}
	if _, ok := hooks["Stop"]; !ok {
		t.Errorf("Stop hook missing")
	}
	if strings.Contains(string(data), "OLD notify command") {
		t.Errorf("stale notify command was not refreshed: %s", data)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git init unavailable: %v", err)
	}
}

func gitHooksPath(t *testing.T, dir string) string {
	t.Helper()
	out, _ := exec.Command("git", "-C", dir, "config", "--get", "core.hooksPath").Output()
	return string(out)
}

func TestFixGitHooksPath_ReachableLeftAsIs(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)

	hooks := filepath.Join(repo, "myhooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "config", "--local", "core.hooksPath", hooks).Run(); err != nil {
		t.Fatal(err)
	}

	fixGitHooksPath(&config{HostWorkspace: repo})

	if got := gitHooksPath(t, repo); got == "" || got[:len(got)-1] != hooks {
		t.Fatalf("reachable hooksPath should be unchanged, got %q want %q", got, hooks)
	}
}

func TestFixGitHooksPath_RemapsUnderConfigDir(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)

	home := t.TempDir() // container home
	hostHome := "/Users/someone"
	configDir := ".claude"
	aiDir := filepath.Join(home, configDir)

	// The remapped (container) hooks dir exists; the host path does not.
	remappedHooks := filepath.Join(aiDir, "hooks")
	if err := os.MkdirAll(remappedHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hostHooks := hostHome + "/" + configDir + "/hooks"

	// Set hooksPath in the *global* (container) scope so the remap is not
	// shadowed by a repo-local value. Point HOME at the temp home so --global
	// writes there.
	t.Setenv("HOME", home)
	if err := exec.Command("git", "config", "--global", "core.hooksPath", hostHooks).Run(); err != nil {
		t.Fatal(err)
	}

	fixGitHooksPath(&config{
		HostWorkspace: repo,
		HostHome:      hostHome,
		AIConfigDir:   configDir,
		AIDir:         aiDir,
	})

	out, _ := exec.Command("git", "-C", repo, "config", "--get", "core.hooksPath").Output()
	got := string(out)
	if got != remappedHooks+"\n" {
		t.Fatalf("hooksPath = %q, want remapped %q", got, remappedHooks)
	}
}

func TestFixGitHooksPath_LocalUnreachableNotRewritten(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)

	// A repo-local, host-absolute hooksPath that does not exist in-container.
	missing := "/nonexistent/host/checkout/.git/hooks"
	if err := exec.Command("git", "-C", repo, "config", "--local", "core.hooksPath", missing).Run(); err != nil {
		t.Fatal(err)
	}

	fixGitHooksPath(&config{
		HostWorkspace: repo,
		HostHome:      "/Users/someone",
		AIConfigDir:   ".claude",
		AIDir:         filepath.Join(t.TempDir(), ".claude"),
	})

	// The host's mounted .git/config must be left untouched.
	out, _ := exec.Command("git", "-C", repo, "config", "--local", "--get", "core.hooksPath").Output()
	if string(out) != missing+"\n" {
		t.Fatalf("repo-local hooksPath must be unchanged, got %q want %q", string(out), missing)
	}
}
