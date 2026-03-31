package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

// ---------------------------------------------------------------------------
// ParseFlags — core boolean flags
// ---------------------------------------------------------------------------

func TestParseFlags_CoreBooleans(t *testing.T) {
	tests := []struct {
		flag  string
		check func(*App) bool
	}{
		{"--verbose", func(a *App) bool { return a.Verbose }},
		{"-v", func(a *App) bool { return a.Verbose }},
		{"--no-config", func(a *App) bool { return a.NoConfig }},
		{"--no-history", func(a *App) bool { return a.NoHistory }},
		{"--no-build", func(a *App) bool { return a.NoBuild }},
		{"--no-yolo", func(a *App) bool { return !a.Yolo }},
		{"--network-host", func(a *App) bool { return a.NetworkHost }},
		{"--worktree", func(a *App) bool { return a.Worktree }},
		{"--shell", func(a *App) bool { return a.Shell }},
		{"--no-notify", func(a *App) bool { return a.NoNotify }},
		{"--rebuild", func(a *App) bool { return a.Rebuild }},
	}
	for _, tc := range tests {
		t.Run(tc.flag, func(t *testing.T) {
			a := &App{}
			if err := a.ParseFlags([]string{tc.flag}); err != nil {
				t.Fatal(err)
			}
			if !tc.check(a) {
				t.Errorf("flag %s not set", tc.flag)
			}
		})
	}
}

func TestParseFlags_LegacyWorkerPlannerIgnored(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--worker", "--planner"}); err != nil {
		t.Fatal(err)
	}
	if a.Profile != "" {
		t.Errorf("Profile = %q, want empty (legacy flags should be ignored)", a.Profile)
	}
}

func TestParseFlags_ProfileFlag(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--profile", "fast"}); err != nil {
		t.Fatal(err)
	}
	if a.Profile != "fast" {
		t.Errorf("Profile = %q, want %q", a.Profile, "fast")
	}
}

func TestMaybeApplyProfile_AppliesModelAndEffort(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {
			"planner": {Model: "opus", Effort: "high"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   ClaudeProvider(),
		Profile:    "planner",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyProfile(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "opus", "--effort", "high"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyProfile_ExplicitModelNotOverridden(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {
			"fast": {Model: "haiku"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   ClaudeProvider(),
		Profile:    "fast",
		ClaudeArgs: []string{"--model", "sonnet"},
	}

	if err := a.maybeApplyProfile(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "sonnet"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
}

func TestMaybeApplyProfile_NoProfileIsNoop(t *testing.T) {
	a := &App{
		Workspace:  "/tmp/project",
		Provider:   ClaudeProvider(),
		ClaudeArgs: []string{"--model", "opus"},
	}

	if err := a.maybeApplyProfile(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "opus"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
}

func TestMaybeApplyProfile_CodexEffortTemplate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"codex": {
			"deep": {Model: "gpt-5.4", Effort: "high"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   CodexProvider(),
		Profile:    "deep",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyProfile(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "gpt-5.4", "-c", "model_reasoning_effort=high"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyProfile_EffortSkippedWhenExplicit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {
			"planner": {Model: "opus", Effort: "high"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   ClaudeProvider(),
		Profile:    "planner",
		ClaudeArgs: []string{"--effort", "max"},
	}

	if err := a.maybeApplyProfile(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "opus", "--effort", "max"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyProfile_MissingProfileErrorsNonInteractive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)

	a := &App{
		Workspace:  "/tmp/project",
		Provider:   ClaudeProvider(),
		Profile:    "nonexistent",
		ClaudeArgs: nil,
	}

	err := a.maybeApplyProfile()
	if err == nil {
		t.Fatal("expected error for missing profile in non-interactive mode")
	}
}

func TestLoadProfileConfig_LegacyRolesJsonFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	// Write a legacy roles.json
	dir := filepath.Join(tmp, "projects", ProjectDir(workspace))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"roles":{"claude":{"planner":{"model":"opus","effort":"high"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "roles.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	pc, err := LoadProfileConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}

	preset, ok := pc.Profiles["claude"]["planner"]
	if !ok {
		t.Fatal("expected planner profile from legacy roles.json")
	}
	if preset.Model != "opus" {
		t.Fatalf("got model %q, want %q", preset.Model, "opus")
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — --resume is no longer a mittens flag (use -- separator)
// ---------------------------------------------------------------------------

func TestParseFlags_ResumeIsUnknownFlag(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--resume"})
	if err == nil {
		t.Fatal("expected error for --resume, got nil")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error = %q, want 'unknown flag'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — --dir with argument
// ---------------------------------------------------------------------------

func TestParseFlags_Dir(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--dir", "/extra/path"}); err != nil {
		t.Fatal(err)
	}
	if len(a.ExtraDirs) != 1 || a.ExtraDirs[0] != "/extra/path" {
		t.Errorf("ExtraDirs = %v, want [/extra/path]", a.ExtraDirs)
	}
}

func TestParseFlags_DirReadOnly(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--dir-ro", "/readonly/path"}); err != nil {
		t.Fatal(err)
	}
	if len(a.ExtraDirs) != 1 || a.ExtraDirs[0] != "ro:/readonly/path" {
		t.Errorf("ExtraDirs = %v, want [ro:/readonly/path]", a.ExtraDirs)
	}
}

func TestParseFlags_DirMissingArg(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--dir"})
	if err == nil {
		t.Error("expected error for --dir without argument")
	}
}

func TestParseFlags_DirMissingArgNextIsFlag(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--dir", "--verbose"})
	if err == nil {
		t.Error("expected error for --dir followed by another flag")
	}
}

func TestParseFlags_DirReadOnlyMissingArg(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--dir-ro"})
	if err == nil {
		t.Error("expected error for --dir-ro without argument")
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — "--" separator
// ---------------------------------------------------------------------------

func TestParseFlags_Separator(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--verbose", "--", "--resume", "--model", "opus"}); err != nil {
		t.Fatal(err)
	}

	if !a.Verbose {
		t.Error("--verbose before -- should be parsed")
	}

	want := []string{"--resume", "--model", "opus"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i, arg := range a.ClaudeArgs {
		if arg != want[i] {
			t.Errorf("ClaudeArgs[%d] = %q, want %q", i, arg, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — unknown flags forwarded to ClaudeArgs
// ---------------------------------------------------------------------------

func TestParseFlags_UnknownFlagRejectsWithError(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--print", "--model", "opus"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "unknown flag")
	}
}

func TestParseFlags_UnknownFlagAfterSeparatorAllowed(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--verbose", "--", "--print", "--model", "opus"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"--print", "--model", "opus"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
}

func TestParseFlags_PositionalArgRejected(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"do something"})
	if err == nil {
		t.Fatal("expected error for positional arg, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error = %q, want it to contain \"unknown command\"", err)
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — extension delegation
// ---------------------------------------------------------------------------

func TestParseFlags_ExtensionDelegation(t *testing.T) {
	a := &App{
		Extensions: []*registry.Extension{
			{
				Name: "test-ext",
				Flags: []registry.ExtensionFlag{
					{Name: "--test-ext", Arg: "csv"},
				},
			},
		},
	}

	if err := a.ParseFlags([]string{"--test-ext", "a,b,c", "--verbose"}); err != nil {
		t.Fatal(err)
	}

	ext := a.Extensions[0]
	if !ext.Enabled {
		t.Error("extension should be enabled")
	}
	wantArgs := []string{"a", "b", "c"}
	if len(ext.Args) != len(wantArgs) {
		t.Fatalf("ext.Args = %v, want %v", ext.Args, wantArgs)
	}
	for i, a := range ext.Args {
		if a != wantArgs[i] {
			t.Errorf("ext.Args[%d] = %q, want %q", i, a, wantArgs[i])
		}
	}
	if !a.Verbose {
		t.Error("--verbose should still be parsed after extension flag")
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — multiple --dir flags
// ---------------------------------------------------------------------------

func TestParseFlags_MultipleDir(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--dir", "/a", "--dir", "/b"}); err != nil {
		t.Fatal(err)
	}
	if len(a.ExtraDirs) != 2 {
		t.Fatalf("ExtraDirs = %v, want 2 entries", a.ExtraDirs)
	}
	if a.ExtraDirs[0] != "/a" || a.ExtraDirs[1] != "/b" {
		t.Errorf("ExtraDirs = %v, want [/a, /b]", a.ExtraDirs)
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — empty args
// ---------------------------------------------------------------------------

func TestParseFlags_Empty(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	if a.Verbose || a.Shell || len(a.ClaudeArgs) > 0 {
		t.Error("empty args should leave defaults")
	}
}

// ---------------------------------------------------------------------------
// assembleDockerArgs helpers
// ---------------------------------------------------------------------------

// argSliceContains reports whether any element in args contains substr.
func argSliceContains(args []string, substr string) bool {
	for _, a := range args {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
}

// argContainsExact reports whether args contains the given exact value.
func argContainsExact(args []string, val string) bool {
	for _, a := range args {
		if a == val {
			return true
		}
	}
	return false
}

// argPairExists reports whether args contains flag immediately followed by val.
// e.g. argPairExists(args, "-v", "/path") matches [... "-v" "/path" ...].
// extractInitConfig finds the MITTENS_CONFIG env var in docker args,
// reads the JSON config file it points to, and returns the parsed config.
func extractInitConfig(t *testing.T, args []string) *initcfg.ContainerConfig {
	t.Helper()
	// Find the host-side file path from the -v mount that maps to initcfg.ConfigPath.
	var hostPath string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-v" {
			parts := strings.SplitN(args[i+1], ":", 3)
			if len(parts) >= 2 && parts[1] == initcfg.ConfigPath {
				hostPath = parts[0]
				break
			}
		}
	}
	if hostPath == "" {
		t.Fatal("no mittens-init config mount found in docker args")
	}
	cfg, err := initcfg.Load(hostPath)
	if err != nil {
		t.Fatalf("failed to load init config from %s: %v", hostPath, err)
	}
	return cfg
}

func argPairExists(args []string, flag, val string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

// argPairContains reports whether args contains flag immediately followed by
// a value containing substr.
func argPairContains(args []string, flag, substr string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && strings.Contains(args[i+1], substr) {
			return true
		}
	}
	return false
}

// setupTestHome creates a minimal $HOME layout for assembleDockerArgs tests.
// Returns the temp home dir. The caller should use t.Setenv("HOME", home).
func setupTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	return home
}

func TestSharedClipboardSyncHealthy_HeartbeatFresh(t *testing.T) {
	cp := newClipboardPathsAt(t.TempDir())
	if err := os.WriteFile(cp.pidFile(), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cp.heartbeatFile(), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600); err != nil {
		t.Fatal(err)
	}

	if !cp.syncHealthy() {
		t.Fatal("expected shared clipboard sync to be healthy")
	}
}

func TestSharedClipboardSyncHealthy_HeartbeatStale(t *testing.T) {
	cp := newClipboardPathsAt(t.TempDir())
	if err := os.WriteFile(cp.pidFile(), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	heartbeat := cp.heartbeatFile()
	if err := os.WriteFile(heartbeat, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(heartbeat, stale, stale); err != nil {
		t.Fatal(err)
	}

	if cp.syncHealthy() {
		t.Fatal("expected stale heartbeat to be unhealthy")
	}
}

func TestStaleClipboardLock_DeadPID(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "clipboard-sync.lock")
	if err := os.WriteFile(lockPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !staleClipboardLock(lockPath) {
		t.Fatal("expected dead pid lock to be stale")
	}
}

func TestCopySharedClipboardSnapshot(t *testing.T) {
	shared := newClipboardPathsAt(t.TempDir())
	client := newClipboardPathsAt(t.TempDir())

	files := map[string]string{
		shared.stateFile():     "image\n",
		shared.updatedAtFile(): "123\n",
		shared.errorFile():     "",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(shared.imageFile(), []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copySharedClipboardSnapshot(shared.dir, client.dir); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		client.stateFile(),
		client.updatedAtFile(),
		client.errorFile(),
		client.imageFile(),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected snapshot file %s: %v", path, err)
		}
	}
}

// ---------------------------------------------------------------------------
// assembleDockerArgs — Tier 3 orchestration tests
// ---------------------------------------------------------------------------

func TestAssembleDockerArgs_Baseline(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	t.Setenv("TERM", "xterm-256color")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-test",
		WorkspaceMountSrc: "/tmp/workspace",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Core flags always present.
	if !argSliceContains(args, "-it") {
		t.Error("missing -it")
	}
	if !argPairExists(args, "--name", "mittens-test") {
		t.Error("missing --name")
	}

	// Workspace mount.
	if !argPairExists(args, "-v", "/tmp/workspace:/tmp/workspace") {
		t.Error("missing workspace mount")
	}

	// Environment variables.
	if !argPairExists(args, "-e", "ANTHROPIC_API_KEY=sk-test-key") {
		t.Error("missing ANTHROPIC_API_KEY env")
	}
	if !argPairContains(args, "-e", "TERM=") {
		t.Error("missing TERM env")
	}
	// MITTENS_DIND should not be set in baseline (non-DinD) mode.
	cfg := extractInitConfig(t, args)
	if cfg.Flags.DinD {
		t.Error("Flags.DinD should be false in baseline (non-DinD) mode")
	}

	// Security hardening (non-DinD).
	if !argPairExists(args, "--cap-drop", "ALL") {
		t.Error("missing --cap-drop ALL")
	}
	if !argPairExists(args, "--cap-add", "SETUID") {
		t.Error("missing --cap-add SETUID")
	}
	if !argPairExists(args, "--cap-add", "SETGID") {
		t.Error("missing --cap-add SETGID")
	}
	if !argPairExists(args, "--security-opt", "no-new-privileges") {
		t.Error("missing --security-opt no-new-privileges")
	}

	// Should NOT have credential mount or session mounts.
	if argPairContains(args, "-v", ".credentials.json") {
		t.Error("credential mount should not be present without credentials")
	}
	if argPairContains(args, "-v", "/projects/") {
		t.Error("session persistence mounts should not be present with NoHistory=true")
	}
}

func TestAssembleDockerArgs_WithCredentials(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create a real temp credential file.
	credFile := filepath.Join(t.TempDir(), "creds.json")
	os.WriteFile(credFile, []byte(`{"token":"test"}`), 0600)

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-cred",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{tmpFile: credFile},
	}

	args := a.assembleDockerArgs(nil, nil)

	wantMount := credFile + ":/mnt/mittens-staging/.credentials.json:ro"
	if !argPairExists(args, "-v", wantMount) {
		t.Errorf("missing credential mount, want -v %s\nargs: %v", wantMount, args)
	}
}

func TestAssembleDockerArgs_SessionPersistence(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	workspace := "/Users/test/project"
	hostProjectDir := ProjectDir(workspace)

	// Create required dirs.
	os.MkdirAll(filepath.Join(home, ".claude", "projects", hostProjectDir), 0o755)

	a := &App{
		Provider:           DefaultProvider(),
		NoHistory:          false,
		ContainerName:      "mittens-session",
		WorkspaceMountSrc:  "/tmp/ws",
		HostProjectDir:     hostProjectDir,
		Workspace:          workspace,
		EffectiveWorkspace: workspace,
		Credentials:        &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Project dir mount.
	p := DefaultProvider()
	projDir := filepath.Join(p.HostConfigDir(home), "projects", hostProjectDir)
	containerProjDir := filepath.Join(p.ContainerConfigDir(), "projects", hostProjectDir)
	if !argPairExists(args, "-v", projDir+":"+containerProjDir) {
		t.Error("missing project dir mount")
	}

	// Plans and tasks mounts (Worktree=false).
	if !argPairContains(args, "-v", "/.claude/plans:") {
		t.Error("missing plans mount")
	}
	if !argPairContains(args, "-v", "/.claude/tasks:") {
		t.Error("missing tasks mount")
	}

	// HostWorkspace is always set to EffectiveWorkspace.
	cfg := extractInitConfig(t, args)
	if cfg.HostWorkspace != workspace {
		t.Errorf("HostWorkspace = %q, want %q", cfg.HostWorkspace, workspace)
	}
}

func TestAssembleDockerArgs_CodexSessionPersistenceMountsWholeConfig(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	p := CodexProvider()
	os.MkdirAll(p.HostConfigDir(home), 0o755)
	hostProjectFile := filepath.Join(p.HostConfigDir(home), p.ProjectFile)
	if err := os.WriteFile(hostProjectFile, []byte("host agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Provider:           p,
		NoHistory:          false,
		ContainerName:      "mittens-codex-session",
		WorkspaceMountSrc:  "/tmp/ws",
		Workspace:          "/Users/test/project",
		EffectiveWorkspace: "/Users/test/project",
		Credentials:        &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "-v", p.HostConfigDir(home)+":"+p.ContainerConfigDir()) {
		t.Fatalf("missing whole-config history mount for codex")
	}
	if argPairExists(args, "-v", p.HostConfigDir(home)+":"+p.StagingConfigDir()+":ro") {
		t.Fatalf("did not expect staging config mount when whole-config history is enabled")
	}
	if argPairContains(args, "-v", "/projects/") {
		t.Fatalf("did not expect project-only history mount for codex")
	}
	if !argPairContains(args, "-v", ":"+filepath.Join(p.ContainerConfigDir(), p.ProjectFile)) {
		t.Fatalf("missing runtime project file overlay mount")
	}

	cfg := extractInitConfig(t, args)
	if cfg.HostWorkspace != "/Users/test/project" {
		t.Fatalf("HostWorkspace = %q, want %q", cfg.HostWorkspace, "/Users/test/project")
	}

	var overlayPath string
	wantTarget := filepath.Join(p.ContainerConfigDir(), p.ProjectFile)
	for i := 0; i < len(args)-1; i++ {
		if args[i] != "-v" {
			continue
		}
		parts := strings.SplitN(args[i+1], ":", 2)
		if len(parts) == 2 && parts[1] == wantTarget {
			overlayPath = parts[0]
			break
		}
	}
	if overlayPath == "" {
		t.Fatal("overlay mount target not found")
	}
	if overlayPath == hostProjectFile {
		t.Fatal("overlay mount should not point to the host project file directly")
	}
	data, err := os.ReadFile(overlayPath)
	if err != nil {
		t.Fatalf("reading overlay file: %v", err)
	}
	if string(data) != "host agents\n" {
		t.Fatalf("overlay file contents = %q, want %q", string(data), "host agents\n")
	}
}

func TestAssembleDockerArgs_DinD(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-dind",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	// Simulate what the docker resolver contributes for dind mode.
	resolverArgs := []string{
		"--privileged",
		"-v", "mittens-dind-docker:/var/lib/docker",
		"-e", "MITTENS_DIND=true",
	}

	args := a.assembleDockerArgs(resolverArgs, nil)

	// Should have --privileged (from resolver).
	if !argSliceContains(args, "--privileged") {
		t.Error("missing --privileged for DinD")
	}

	// Docker volume mount (from resolver).
	if !argPairExists(args, "-v", "mittens-dind-docker:/var/lib/docker") {
		t.Error("missing docker volume mount for DinD")
	}

	// MITTENS_DIND should be true (from resolver, now in JSON config).
	cfg := extractInitConfig(t, args)
	if !cfg.Flags.DinD {
		t.Error("Flags.DinD should be true for DinD mode")
	}

	// Security hardening should be ABSENT (--privileged in resolverArgs suppresses it).
	if argPairExists(args, "--cap-drop", "ALL") {
		t.Error("--cap-drop ALL should not be present with DinD")
	}
	if argPairExists(args, "--security-opt", "no-new-privileges") {
		t.Error("--security-opt should not be present with DinD")
	}
}

func TestAssembleDockerArgs_NetworkHost(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		NetworkHost:       true,
		ContainerName:     "mittens-net",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "--network", "host") {
		t.Error("missing --network host")
	}
}

func TestAssembleDockerArgs_ExtensionMountsEnvCaps(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create a dir for the conditional mount.
	mountSrc := filepath.Join(home, ".kube")
	os.MkdirAll(mountSrc, 0o755)

	ext := &registry.Extension{
		Name:    "test-ext",
		Enabled: true,
		Mounts: []registry.MountConfig{
			{
				Src:  "~/.kube",
				Dst:  "/home/claude/.kube",
				Mode: "ro",
				When: "dir_exists",
				Env:  map[string]string{"KUBECONFIG": "/home/claude/.kube/config"},
			},
		},
		Env:          map[string]string{"MY_EXT_VAR": "hello"},
		Capabilities: []string{"NET_RAW"},
		Firewall:     []string{"ext.example.com", "api.ext.com"},
	}

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-ext",
		WorkspaceMountSrc: "/tmp/ws",
		Extensions:        []*registry.Extension{ext},
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Extension mount should be present (condition satisfied).
	if !argPairContains(args, "-v", ".kube:/home/claude/.kube:ro") {
		t.Error("missing extension mount")
	}

	// Mount env var.
	if !argPairExists(args, "-e", "KUBECONFIG=/home/claude/.kube/config") {
		t.Error("missing mount env var KUBECONFIG")
	}

	// Extension env var.
	if !argPairExists(args, "-e", "MY_EXT_VAR=hello") {
		t.Error("missing extension env var MY_EXT_VAR")
	}

	// Capability.
	if !argPairExists(args, "--cap-add", "NET_RAW") {
		t.Error("missing --cap-add NET_RAW")
	}

	// Firewall domains in JSON config.
	{
		cfg := extractInitConfig(t, args)
		found := false
		for _, d := range cfg.FirewallExtra {
			if d == "ext.example.com" {
				found = true
				break
			}
		}
		if !found {
			t.Error("FirewallExtra missing ext.example.com")
		}
	}
}

func TestAssembleDockerArgs_ResolverContributions(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Extension with its own firewall domains.
	ext := &registry.Extension{
		Name:     "resolver-ext",
		Enabled:  true,
		Firewall: []string{"ext-domain.com"},
	}

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-resolver",
		WorkspaceMountSrc: "/tmp/ws",
		Extensions:        []*registry.Extension{ext},
		Credentials:       &CredentialManager{},
	}

	resolverArgs := []string{"-v", "/resolver/path:/container/path:ro", "-e", "RESOLVER_VAR=yes"}
	resolverFirewall := []string{"resolver-domain.com"}

	args := a.assembleDockerArgs(resolverArgs, resolverFirewall)

	// Resolver docker args appended verbatim (non-MITTENS env vars stay).
	if !argPairExists(args, "-v", "/resolver/path:/container/path:ro") {
		t.Error("missing resolver mount")
	}
	if !argPairExists(args, "-e", "RESOLVER_VAR=yes") {
		t.Error("missing resolver env var")
	}

	// Firewall domains aggregated in JSON config: extension + resolver.
	cfg := extractInitConfig(t, args)
	hasExt := false
	hasResolver := false
	for _, d := range cfg.FirewallExtra {
		if d == "ext-domain.com" {
			hasExt = true
		}
		if d == "resolver-domain.com" {
			hasResolver = true
		}
	}
	if !hasExt {
		t.Error("FirewallExtra missing extension domain")
	}
	if !hasResolver {
		t.Error("FirewallExtra missing resolver domain")
	}
}

func TestAssembleDockerArgs_OptionalFiles(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create .claude.json and .gitconfig.
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{}`), 0644)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\nname=test"), 0644)

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-opt",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Both files should be mounted read-only.
	claudeJSON := filepath.Join(home, ".claude.json") + ":/mnt/mittens-staging/.claude.json:ro"
	if !argPairExists(args, "-v", claudeJSON) {
		t.Error("missing .claude.json mount")
	}
	gitconfig := filepath.Join(home, ".gitconfig") + ":/mnt/mittens-staging/.gitconfig:ro"
	if !argPairExists(args, "-v", gitconfig) {
		t.Error("missing .gitconfig mount")
	}

	// Now test without the files.
	home2 := setupTestHome(t)
	t.Setenv("HOME", home2)

	a2 := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-opt2",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args2 := a2.assembleDockerArgs(nil, nil)

	if argPairContains(args2, "-v", ".claude.json") {
		t.Error(".claude.json mount should not be present when file doesn't exist")
	}
	if argPairContains(args2, "-v", ".gitconfig") {
		t.Error(".gitconfig mount should not be present when file doesn't exist")
	}
}

func TestAssembleDockerArgs_CredBroker(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-broker",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
		brokerPort:        12345,
		brokerToken:       "broker-secret",
	}

	args := a.assembleDockerArgs(nil, nil)

	// Broker config in JSON.
	cfg := extractInitConfig(t, args)
	if cfg.Broker.Port != 12345 {
		t.Errorf("Broker.Port = %d, want 12345", cfg.Broker.Port)
	}
	if cfg.Broker.Token != "broker-secret" {
		t.Errorf("Broker.Token = %q, want broker-secret", cfg.Broker.Token)
	}

	// host.docker.internal mapping.
	if !argContainsExact(args, "--add-host=host.docker.internal:host-gateway") {
		t.Error("missing --add-host for host.docker.internal")
	}
}

func TestAssembleDockerArgs_NoBroker(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-nobroker",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
		// brokerPort is 0 — no broker
	}

	args := a.assembleDockerArgs(nil, nil)

	// Broker config should have zero values.
	cfg := extractInitConfig(t, args)
	if cfg.Broker.Port != 0 {
		t.Errorf("Broker.Port = %d, want 0 without broker", cfg.Broker.Port)
	}
	if cfg.Broker.Token != "" {
		t.Errorf("Broker.Token = %q, want empty without broker", cfg.Broker.Token)
	}
}

func TestAssembleDockerArgs_ExtraDirs(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create real temp dirs as extra dirs.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-extra",
		WorkspaceMountSrc: "/tmp/ws",
		ExtraDirs:         []string{dir1, "ro:" + dir2},
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Each dir gets a -v mount.
	if !argPairExists(args, "-v", dir1+":"+dir1) {
		t.Errorf("missing mount for extra dir %s", dir1)
	}
	if !argPairExists(args, "-v", dir2+":"+dir2+":ro") {
		t.Errorf("missing read-only mount for extra dir %s", dir2)
	}

	// ExtraDirs in JSON config.
	cfg := extractInitConfig(t, args)
	if len(cfg.ExtraDirs) != 2 {
		t.Fatalf("ExtraDirs has %d entries, want 2", len(cfg.ExtraDirs))
	}
	if cfg.ExtraDirs[0] != dir1 || cfg.ExtraDirs[1] != dir2 {
		t.Errorf("ExtraDirs = %v, want [%s, %s]", cfg.ExtraDirs, dir1, dir2)
	}
}

func TestAssembleDockerArgs_ExtraDirsDedupWorkspace(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Use a real temp dir as both workspace and extra dir.
	wsDir := t.TempDir()

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-dedup",
		WorkspaceMountSrc: wsDir,
		ExtraDirs:         []string{wsDir},
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// The extra dir should be skipped since it matches the workspace mount.
	mountCount := 0
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-v" && strings.Contains(args[i+1], wsDir) {
			mountCount++
		}
	}
	// Expect exactly 1 mount (the primary workspace identity mount), not 2.
	if mountCount != 1 {
		t.Errorf("expected 1 mount for workspace dir, got %d", mountCount)
	}

	// ExtraDirs should be empty after dedup.
	cfg := extractInitConfig(t, args)
	if len(cfg.ExtraDirs) != 0 {
		t.Errorf("ExtraDirs should be empty after dedup, got %v", cfg.ExtraDirs)
	}
}

func TestParseFlags_Name(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--name", "my-instance"}); err != nil {
		t.Fatal(err)
	}
	if a.InstanceName != "my-instance" {
		t.Errorf("InstanceName = %q, want %q", a.InstanceName, "my-instance")
	}
}

func TestParseExtraDirSpec(t *testing.T) {
	got := parseExtraDirSpec("ro:/tmp/a")
	if got.Path != "/tmp/a" || !got.ReadOnly {
		t.Fatalf("parseExtraDirSpec(ro:/tmp/a) = %+v", got)
	}

	got = parseExtraDirSpec("/tmp/b")
	if got.Path != "/tmp/b" || got.ReadOnly {
		t.Fatalf("parseExtraDirSpec(/tmp/b) = %+v", got)
	}
}

func TestAssembleDockerArgs_CustomName(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		InstanceName:      "planner-1",
		ContainerName:     "mittens-planner-1",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "--name", "mittens-planner-1") {
		t.Error("missing --name mittens-planner-1")
	}
	cfg := extractInitConfig(t, args)
	if cfg.InstanceName != "planner-1" {
		t.Errorf("InstanceName = %q, want planner-1", cfg.InstanceName)
	}
}

func TestAssembleDockerArgs_NoCustomName(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-12345",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	cfg := extractInitConfig(t, args)
	if cfg.InstanceName != "" {
		t.Error("InstanceName should be empty without --name")
	}
}

func TestAssembleDockerArgs_ContainerNameEnv(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-42",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	cfg := extractInitConfig(t, args)
	if cfg.ContainerName != "mittens-42" {
		t.Errorf("ContainerName = %q, want mittens-42", cfg.ContainerName)
	}
}

func TestAssembleDockerArgs_NoNotify(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		NoNotify:          true,
		ContainerName:     "mittens-nn",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	cfg := extractInitConfig(t, args)
	if !cfg.Flags.NoNotify {
		t.Error("Flags.NoNotify should be true")
	}
}

func TestAssembleDockerArgs_NotifyEnabled(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-yes",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	cfg := extractInitConfig(t, args)
	if cfg.Flags.NoNotify {
		t.Error("Flags.NoNotify should be false when notifications enabled")
	}
}

func TestIsValidContainerName(t *testing.T) {
	valid := []string{"foo", "my-instance", "app.v2", "test_1", "A123"}
	for _, name := range valid {
		if !isValidContainerName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	invalid := []string{"", "-bad", ".dot", "_under", "has space", "no/slash", "no:colon"}
	for _, name := range invalid {
		if isValidContainerName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestInspectContainerRunning_NonExistent(t *testing.T) {
	// A container that doesn't exist should return (false, false).
	exists, running := InspectContainerRunning("mittens-nonexistent-test-container-xyz")
	if exists {
		t.Error("expected exists=false for non-existent container")
	}
	if running {
		t.Error("expected running=false for non-existent container")
	}
}

// ---------------------------------------------------------------------------
// assembleDockerArgs — provider env vars
// ---------------------------------------------------------------------------

func TestAssembleDockerArgs_ProviderEnvVars(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-prov",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Provider config is now in the JSON config file.
	cfg := extractInitConfig(t, args)
	if cfg.AI.Binary != "claude" {
		t.Errorf("AI.Binary = %q, want claude", cfg.AI.Binary)
	}
	if cfg.AI.ConfigDir != ".claude" {
		t.Errorf("AI.ConfigDir = %q, want .claude", cfg.AI.ConfigDir)
	}
	if cfg.AI.CredFile != ".credentials.json" {
		t.Errorf("AI.CredFile = %q, want .credentials.json", cfg.AI.CredFile)
	}
	if cfg.AI.PrefsFile != ".claude.json" {
		t.Errorf("AI.PrefsFile = %q, want .claude.json", cfg.AI.PrefsFile)
	}
	if cfg.AI.SettingsFile != "settings.json" {
		t.Errorf("AI.SettingsFile = %q, want settings.json", cfg.AI.SettingsFile)
	}
	if cfg.AI.ProjectFile != "CLAUDE.md" {
		t.Errorf("AI.ProjectFile = %q, want CLAUDE.md", cfg.AI.ProjectFile)
	}
	if cfg.AI.TrustedDirsKey != "trustedDirectories" {
		t.Errorf("AI.TrustedDirsKey = %q, want trustedDirectories", cfg.AI.TrustedDirsKey)
	}
	if cfg.AI.YoloKey != "skipDangerousModePermissionPrompt" {
		t.Errorf("AI.YoloKey = %q, want skipDangerousModePermissionPrompt", cfg.AI.YoloKey)
	}
	if cfg.AI.MCPServersKey != "mcpServers" {
		t.Errorf("AI.MCPServersKey = %q, want mcpServers", cfg.AI.MCPServersKey)
	}
	if cfg.AI.SettingsFormat != "json" {
		t.Errorf("AI.SettingsFormat = %q, want json", cfg.AI.SettingsFormat)
	}
	if cfg.AI.PluginDir != "plugins" {
		t.Errorf("AI.PluginDir = %q, want plugins", cfg.AI.PluginDir)
	}
}

func TestAssembleDockerArgs_ProviderPaths(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	p := DefaultProvider()

	a := &App{
		Provider:          p,
		NoHistory:         true,
		ContainerName:     "mittens-paths",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Config staging mount should use provider paths.
	wantConfig := p.HostConfigDir(home) + ":" + p.StagingConfigDir() + ":ro"
	if !argPairExists(args, "-v", wantConfig) {
		t.Errorf("missing config staging mount, want -v %s", wantConfig)
	}
}

// ---------------------------------------------------------------------------
// assembleDockerArgs — provider API key parameterization
// ---------------------------------------------------------------------------

func TestAssembleDockerArgs_ProviderAPIKeyEnv(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")

	p := CodexProvider()
	os.MkdirAll(filepath.Join(home, p.ConfigDir), 0o755)

	a := &App{
		Provider:          p,
		NoHistory:         true,
		ContainerName:     "mittens-apikey",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Should use OPENAI_API_KEY, not ANTHROPIC_API_KEY.
	if !argPairExists(args, "-e", "OPENAI_API_KEY=sk-openai-test") {
		t.Error("missing OPENAI_API_KEY env var")
	}
	if argPairContains(args, "-e", "ANTHROPIC_API_KEY") {
		t.Error("ANTHROPIC_API_KEY should not be present for Codex provider")
	}
}

func TestAssembleDockerArgs_CodexProvider(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")

	p := CodexProvider()
	os.MkdirAll(filepath.Join(home, p.ConfigDir), 0o755)

	a := &App{
		Provider:          p,
		NoHistory:         true,
		ContainerName:     "mittens-codex",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Codex-specific config in JSON.
	cfg := extractInitConfig(t, args)
	if cfg.AI.Binary != "codex" {
		t.Errorf("AI.Binary = %q, want codex", cfg.AI.Binary)
	}
	if cfg.AI.ConfigDir != ".codex" {
		t.Errorf("AI.ConfigDir = %q, want .codex", cfg.AI.ConfigDir)
	}
	if cfg.AI.CredFile != "auth.json" {
		t.Errorf("AI.CredFile = %q, want auth.json", cfg.AI.CredFile)
	}
	if cfg.AI.SettingsFile != "config.toml" {
		t.Errorf("AI.SettingsFile = %q, want config.toml", cfg.AI.SettingsFile)
	}
	if cfg.AI.ProjectFile != "AGENTS.md" {
		t.Errorf("AI.ProjectFile = %q, want AGENTS.md", cfg.AI.ProjectFile)
	}
	if cfg.AI.SettingsFormat != "toml" {
		t.Errorf("AI.SettingsFormat = %q, want toml", cfg.AI.SettingsFormat)
	}

	// UserPrefsFile is empty — should NOT mount a user prefs file.
	if argPairContains(args, "-v", "StagingUserPrefsPath") {
		t.Error("user prefs mount should not be present for Codex (empty UserPrefsFile)")
	}
}

func TestAssembleDockerArgs_GeminiProvider(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_API_KEY", "sk-gemini-test")

	p := GeminiProvider()
	os.MkdirAll(filepath.Join(home, p.ConfigDir), 0o755)

	a := &App{
		Provider:          p,
		NoHistory:         true,
		ContainerName:     "mittens-gemini",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Gemini-specific config should be in the JSON config file.
	cfg := extractInitConfig(t, args)
	if cfg.AI.Binary != "gemini" {
		t.Errorf("AI.Binary = %q, want gemini", cfg.AI.Binary)
	}
	if cfg.AI.ConfigDir != ".gemini" {
		t.Errorf("AI.ConfigDir = %q, want .gemini", cfg.AI.ConfigDir)
	}
	if cfg.AI.CredFile != "oauth_creds.json" {
		t.Errorf("AI.CredFile = %q, want oauth_creds.json", cfg.AI.CredFile)
	}
	if cfg.AI.SettingsFile != "settings.json" {
		t.Errorf("AI.SettingsFile = %q, want settings.json", cfg.AI.SettingsFile)
	}
	if cfg.AI.ProjectFile != "GEMINI.md" {
		t.Errorf("AI.ProjectFile = %q, want GEMINI.md", cfg.AI.ProjectFile)
	}
	if cfg.AI.SettingsFormat != "json" {
		t.Errorf("AI.SettingsFormat = %q, want json", cfg.AI.SettingsFormat)
	}
	if cfg.AI.TrustedDirsFile != "trustedFolders.json" {
		t.Errorf("AI.TrustedDirsFile = %q, want trustedFolders.json", cfg.AI.TrustedDirsFile)
	}

	// ContainerHostname for Gemini.
	if !argPairExists(args, "--hostname", "gemini-cli") {
		t.Error("missing --hostname gemini-cli for Gemini provider")
	}
}

func TestAssembleDockerArgs_SettingsFormatEnv(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-fmt",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	cfg := extractInitConfig(t, args)
	if cfg.AI.SettingsFormat != "json" {
		t.Errorf("AI.SettingsFormat = %q, want json", cfg.AI.SettingsFormat)
	}
}

func TestMaybeApplyProviderRuntimeArgs_CodexDisablesUpdateCheck(t *testing.T) {
	a := &App{
		Provider:   CodexProvider(),
		ClaudeArgs: []string{"--model", "gpt-5"},
	}

	a.maybeApplyProviderRuntimeArgs()

	want := []string{"-c", "check_for_update_on_startup=false", "--model", "gpt-5"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyProviderRuntimeArgs_CodexRespectsExistingOverride(t *testing.T) {
	a := &App{
		Provider:   CodexProvider(),
		ClaudeArgs: []string{"-c", "check_for_update_on_startup=true", "--model", "gpt-5"},
	}

	a.maybeApplyProviderRuntimeArgs()

	want := []string{"-c", "check_for_update_on_startup=true", "--model", "gpt-5"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestSanitizeDockerArgsForLog_RedactsSecrets(t *testing.T) {
	args := []string{
		"-e", "OPENAI_API_KEY=sk-live",
		"-e", "MITTENS_BROKER_TOKEN=secret",
		"-e", "TERM=xterm-256color",
	}

	got := sanitizeDockerArgsForLog(args)

	if got[1] != "OPENAI_API_KEY=REDACTED" {
		t.Fatalf("OPENAI_API_KEY not redacted: %v", got)
	}
	if got[3] != "MITTENS_BROKER_TOKEN=REDACTED" {
		t.Fatalf("MITTENS_BROKER_TOKEN not redacted: %v", got)
	}
	if got[5] != "TERM=xterm-256color" {
		t.Fatalf("non-secret env should remain visible: %v", got)
	}
	if args[1] != "OPENAI_API_KEY=sk-live" {
		t.Fatal("sanitizeDockerArgsForLog should not mutate input slice")
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — --provider flag
// ---------------------------------------------------------------------------

func TestParseFlags_ProviderConsumed(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--provider", "codex", "--verbose"}); err != nil {
		t.Fatal(err)
	}
	// --provider and its value should be consumed (not forwarded to ClaudeArgs).
	if argContainsExact(a.ClaudeArgs, "--provider") {
		t.Error("--provider should not be forwarded to ClaudeArgs")
	}
	if argContainsExact(a.ClaudeArgs, "codex") {
		t.Error("codex should not be forwarded to ClaudeArgs")
	}
	if !a.Verbose {
		t.Error("--verbose should still be parsed after --provider")
	}
}

func TestParseFlags_ProviderMissingArg(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--provider"})
	if err == nil {
		t.Error("expected error for --provider without argument")
	}
}

func TestAssembleDockerArgs_ProviderFirewallDomains(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	p := CodexProvider()
	os.MkdirAll(filepath.Join(home, p.ConfigDir), 0o755)

	a := &App{
		Provider:          p,
		NoHistory:         true,
		ContainerName:     "mittens-fw",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Provider firewall domains should appear in the JSON config.
	cfg := extractInitConfig(t, args)
	for _, domain := range p.FirewallDomains {
		found := false
		for _, d := range cfg.FirewallExtra {
			if d == domain {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("FirewallExtra missing provider domain %s", domain)
		}
	}
}

// ---------------------------------------------------------------------------
// effortEnabled
// ---------------------------------------------------------------------------

func TestEffortEnabled(t *testing.T) {
	tests := []struct {
		name string
		p    *Provider
		want bool
	}{
		{"EffortFlag set", &Provider{EffortFlag: "--effort"}, true},
		{"EffortTemplate set", &Provider{EffortTemplate: "reasoning_effort=%s"}, true},
		{"both set", &Provider{EffortFlag: "--effort", EffortTemplate: "reasoning_effort=%s"}, true},
		{"neither set", &Provider{}, false},
		{"nil provider", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := effortEnabled(tc.p); got != tc.want {
				t.Errorf("effortEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// effortArgExists
// ---------------------------------------------------------------------------

func TestEffortArgExists(t *testing.T) {
	tests := []struct {
		name string
		p    *Provider
		args []string
		want bool
	}{
		{
			"EffortFlag present",
			&Provider{EffortFlag: "--effort"},
			[]string{"--model", "opus", "--effort", "high"},
			true,
		},
		{
			"EffortFlag absent",
			&Provider{EffortFlag: "--effort"},
			[]string{"--model", "opus"},
			false,
		},
		{
			"EffortTemplate with -c prefix",
			&Provider{EffortTemplate: "reasoning_effort=%s"},
			[]string{"-c", "reasoning_effort=high"},
			true,
		},
		{
			"EffortTemplate absent",
			&Provider{EffortTemplate: "reasoning_effort=%s"},
			[]string{"--model", "opus"},
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := effortArgExists(tc.p, tc.args); got != tc.want {
				t.Errorf("effortArgExists() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// effortTemplateKey
// ---------------------------------------------------------------------------

func TestEffortTemplateKey(t *testing.T) {
	tests := []struct {
		name string
		p    *Provider
		want string
	}{
		{"no equals sign", &Provider{EffortTemplate: "--thinking-budget %s"}, ""},
		{"key=value template", &Provider{EffortTemplate: "reasoning_effort=%s"}, "reasoning_effort"},
		{"empty template", &Provider{EffortTemplate: ""}, ""},
		{"nil provider", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := effortTemplateKey(tc.p); got != tc.want {
				t.Errorf("effortTemplateKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// effortArgs
// ---------------------------------------------------------------------------

func TestEffortArgs(t *testing.T) {
	tests := []struct {
		name   string
		p      *Provider
		effort string
		want   []string
	}{
		{
			"EffortFlag",
			&Provider{EffortFlag: "--effort"},
			"high",
			[]string{"--effort", "high"},
		},
		{
			"EffortTemplate",
			&Provider{EffortTemplate: "reasoning_effort=%s"},
			"high",
			[]string{"reasoning_effort=high"},
		},
		{
			"nil provider",
			nil,
			"high",
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := effortArgs(tc.p, tc.effort)
			if tc.want == nil {
				if got != nil {
					t.Errorf("effortArgs() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("effortArgs() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("effortArgs()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// argExists
// ---------------------------------------------------------------------------

func TestArgExists(t *testing.T) {
	tests := []struct {
		name string
		args []string
		val  string
		want bool
	}{
		{"found", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b"}, "d", false},
		{"empty slice", []string{}, "x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := argExists(tc.args, tc.val); got != tc.want {
				t.Errorf("argExists() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// envOrDefault
// ---------------------------------------------------------------------------

func TestEnvOrDefault(t *testing.T) {
	t.Run("env set", func(t *testing.T) {
		t.Setenv("MITTENS_TEST_ENV_OR_DEFAULT", "from-env")
		if got := envOrDefault("MITTENS_TEST_ENV_OR_DEFAULT", "fallback"); got != "from-env" {
			t.Errorf("envOrDefault() = %q, want %q", got, "from-env")
		}
	})
	t.Run("env not set", func(t *testing.T) {
		if got := envOrDefault("MITTENS_TEST_UNSET_12345", "fallback"); got != "fallback" {
			t.Errorf("envOrDefault() = %q, want %q", got, "fallback")
		}
	})
}

// ---------------------------------------------------------------------------
// copyFileAtomic
// ---------------------------------------------------------------------------

func TestCopyFileAtomic(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.txt")
		dst := filepath.Join(dir, "dst.txt")
		content := "hello atomic"
		if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := copyFileAtomic(src, dst); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Errorf("dst content = %q, want %q", string(got), content)
		}
	})
	t.Run("source missing", func(t *testing.T) {
		dir := t.TempDir()
		err := copyFileAtomic(filepath.Join(dir, "nonexistent"), filepath.Join(dir, "dst"))
		if err == nil {
			t.Error("expected error for missing source")
		}
	})
}

// ---------------------------------------------------------------------------
// staleClipboardLock — additional sub-tests
// ---------------------------------------------------------------------------

func TestStaleClipboardLock(t *testing.T) {
	t.Run("LivePID", func(t *testing.T) {
		lockPath := filepath.Join(t.TempDir(), "clipboard-sync.lock")
		if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		if staleClipboardLock(lockPath) {
			t.Fatal("expected live pid lock to not be stale")
		}
	})
	t.Run("MissingFile", func(t *testing.T) {
		if staleClipboardLock(filepath.Join(t.TempDir(), "nonexistent.lock")) {
			t.Fatal("expected missing lock file to not be stale")
		}
	})
}

// ---------------------------------------------------------------------------
// extractMittensEnv
// ---------------------------------------------------------------------------

func TestExtractMittensEnv(t *testing.T) {
	tests := []struct {
		name  string
		kv    string
		want  bool
		check func(*initcfg.ContainerConfig) bool
	}{
		{
			"MITTENS_DIND=true",
			"MITTENS_DIND=true",
			true,
			func(c *initcfg.ContainerConfig) bool { return c.Flags.DinD },
		},
		{
			"MITTENS_MCP=server1",
			"MITTENS_MCP=server1",
			true,
			func(c *initcfg.ContainerConfig) bool { return c.MCP == "server1" },
		},
		{
			"MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS=30",
			"MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS=30",
			true,
			func(c *initcfg.ContainerConfig) bool { return c.X11ClipboardMaxAgeSecs == 30 },
		},
		{
			"unrelated var",
			"OTHER_VAR=foo",
			false,
			nil,
		},
		{
			"unrecognized MITTENS var",
			"MITTENS_UNKNOWN=bar",
			false,
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &initcfg.ContainerConfig{}
			got := extractMittensEnv(cfg, tc.kv)
			if got != tc.want {
				t.Errorf("extractMittensEnv(%q) = %v, want %v", tc.kv, got, tc.want)
			}
			if tc.check != nil && !tc.check(cfg) {
				t.Errorf("config field not set correctly for %q", tc.kv)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// filterMittensEnvArgs
// ---------------------------------------------------------------------------

func TestFilterMittensEnvArgs(t *testing.T) {
	cfg := &initcfg.ContainerConfig{}
	src := []string{"-e", "MITTENS_DIND=true", "-e", "HOME=/root"}
	dst := filterMittensEnvArgs(nil, src, cfg)

	if !cfg.Flags.DinD {
		t.Error("cfg.Flags.DinD should be true after filtering MITTENS_DIND=true")
	}

	// dst should contain HOME=/root but not MITTENS_DIND=true.
	hasHome := false
	hasMittens := false
	for _, a := range dst {
		if a == "HOME=/root" {
			hasHome = true
		}
		if strings.Contains(a, "MITTENS_DIND") {
			hasMittens = true
		}
	}
	if !hasHome {
		t.Errorf("returned args should contain HOME=/root, got %v", dst)
	}
	if hasMittens {
		t.Errorf("returned args should not contain MITTENS_DIND, got %v", dst)
	}
}
