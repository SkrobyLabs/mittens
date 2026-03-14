package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
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
		{"--yolo", func(a *App) bool { return a.Yolo }},
		{"--network-host", func(a *App) bool { return a.NetworkHost }},
		{"--worktree", func(a *App) bool { return a.Worktree }},
		{"--shell", func(a *App) bool { return a.Shell }},
		{"--no-notify", func(a *App) bool { return a.NoNotify }},
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

func TestParseFlags_WorkerPlannerRoleFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"worker", []string{"--worker", "--print"}, "worker"},
		{"planner", []string{"--planner"}, "planner"},
		{"planner wins", []string{"--worker", "--planner"}, "planner"},
	}
	for _, tc := range tests {
		a := &App{}
		if err := a.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		if a.Role != tc.want {
			t.Errorf("%s: Role = %q, want %q", tc.name, a.Role, tc.want)
		}
	}
}

func TestMaybeApplyRolePreset_ClaudeDefaultsWithExplicitModel(t *testing.T) {
	a := &App{
		Workspace:  "/tmp/project",
		Provider:   ClaudeProvider(),
		Role:       "worker",
		ClaudeArgs: []string{"--model", "opus"},
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "opus"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyRolePreset_PlannerUsesOverrides(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveRoleConfig(workspace, &RoleConfig{Roles: map[string]map[string]RolePreset{
		"claude": {
			"planner": {Model: "custom-model"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   ClaudeProvider(),
		Role:       "planner",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "custom-model"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyRolePreset_EffortInjected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := ""
	provider := ClaudeProvider()
	provider.RoleDefaults["worker"] = RolePreset{Model: "claude-haiku-4-6", Effort: "high"}

	a := &App{
		Workspace:  workspace,
		Provider:   provider,
		Role:       "worker",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "claude-haiku-4-6", "--effort", "high"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyRolePreset_EffortInjectedFromOverrideConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveRoleConfig(workspace, &RoleConfig{Roles: map[string]map[string]RolePreset{
		"claude": {
			"planner": {Effort: "high"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   ClaudeProvider(),
		Role:       "planner",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "claude-opus-4-6", "--effort", "high"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyRolePreset_CodexEffortInjected(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveRoleConfig(workspace, &RoleConfig{Roles: map[string]map[string]RolePreset{
		"codex": {
			"planner": {Model: "gpt-5.4", Effort: "high"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		Workspace:  workspace,
		Provider:   CodexProvider(),
		Role:       "planner",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyRolePreset(); err != nil {
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

func TestRolePreset_CodexPlannerDefaultsToHigh(t *testing.T) {
	preset, ok := rolePreset("/tmp/project", CodexProvider(), "planner")
	if !ok {
		t.Fatal("expected codex planner preset")
	}
	if preset.Effort != "high" {
		t.Fatalf("got effort %q, want %q", preset.Effort, "high")
	}
}

func TestRolePreset_CodexPlannerPreservesConfiguredEffort(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MITTENS_HOME", tmp)
	workspace := "/tmp/project"

	if err := SaveRoleConfig(workspace, &RoleConfig{Roles: map[string]map[string]RolePreset{
		"codex": {
			"planner": {Effort: "low"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	preset, ok := rolePreset(workspace, CodexProvider(), "planner")
	if !ok {
		t.Fatal("expected codex planner preset")
	}
	if preset.Effort != "low" {
		t.Fatalf("got effort %q, want %q", preset.Effort, "low")
	}
}

func TestMaybeApplyRolePreset_CodexEffortNotInjectedIfProvidedViaTemplateArg(t *testing.T) {
	a := &App{
		Workspace:  "/tmp/project",
		Provider:   CodexProvider(),
		Role:       "planner",
		ClaudeArgs: []string{"-c", "model_reasoning_effort=high"},
	}

	if err := a.maybeApplyRolePreset(); err != nil {
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

func TestMaybeApplyRolePreset_CodexWorkerEffortNotInjected(t *testing.T) {
	a := &App{
		Workspace:  "/tmp/project",
		Provider:   CodexProvider(),
		Role:       "worker",
		ClaudeArgs: nil,
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "gpt-5.3-codex-spark"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyRolePreset_EffortSkippedWhenExplicit(t *testing.T) {
	a := &App{
		Workspace:  "/tmp/project",
		Provider:   ClaudeProvider(),
		Role:       "planner",
		ClaudeArgs: []string{"--effort", "max"},
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "claude-opus-4-6", "--effort", "max"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyRolePreset_CustomRoleNoPreset(t *testing.T) {
	a := &App{
		Workspace:  "/tmp/project",
		Provider:   CodexProvider(),
		Role:       "custom",
		ClaudeArgs: []string{"--model", "cli-model"},
	}

	if err := a.maybeApplyRolePreset(); err != nil {
		t.Fatal(err)
	}

	want := []string{"--model", "cli-model"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — --resume with optional argument
// ---------------------------------------------------------------------------

func TestParseFlags_ResumeLatest(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--resume"}); err != nil {
		t.Fatal(err)
	}
	if a.ResumeSession != "latest" {
		t.Errorf("ResumeSession = %q, want %q", a.ResumeSession, "latest")
	}
}

func TestParseFlags_ResumeWithID(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--resume", "abc123"}); err != nil {
		t.Fatal(err)
	}
	if a.ResumeSession != "abc123" {
		t.Errorf("ResumeSession = %q, want %q", a.ResumeSession, "abc123")
	}
}

func TestParseFlags_ResumeBeforeOtherFlags(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--resume", "--verbose"}); err != nil {
		t.Fatal(err)
	}
	if a.ResumeSession != "latest" {
		t.Errorf("ResumeSession = %q, want %q", a.ResumeSession, "latest")
	}
	if !a.Verbose {
		t.Error("--verbose not set")
	}
}

func TestParseFlags_DefaultNoResume(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--verbose"}); err != nil {
		t.Fatal(err)
	}
	if a.ResumeSession != "" {
		t.Errorf("ResumeSession = %q, want empty", a.ResumeSession)
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

func TestParseFlags_UnknownForwarded(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--print", "--model", "opus"}); err != nil {
		t.Fatal(err)
	}

	want := []string{"--print", "--model", "opus"}
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
	dir := t.TempDir()
	if err := os.WriteFile(sharedClipboardPIDFile(dir), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sharedClipboardHeartbeatFile(dir), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600); err != nil {
		t.Fatal(err)
	}

	if !sharedClipboardSyncHealthy(dir) {
		t.Fatal("expected shared clipboard sync to be healthy")
	}
}

func TestSharedClipboardSyncHealthy_HeartbeatStale(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(sharedClipboardPIDFile(dir), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	heartbeat := sharedClipboardHeartbeatFile(dir)
	if err := os.WriteFile(heartbeat, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(heartbeat, stale, stale); err != nil {
		t.Fatal(err)
	}

	if sharedClipboardSyncHealthy(dir) {
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
	sharedDir := t.TempDir()
	clientDir := t.TempDir()

	files := map[string]string{
		sharedClipboardStateFile(sharedDir):     "image\n",
		sharedClipboardUpdatedAtFile(sharedDir): "123\n",
		sharedClipboardErrorFile(sharedDir):     "",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(sharedClipboardImageFile(sharedDir), []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copySharedClipboardSnapshot(sharedDir, clientDir); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		sharedClipboardStateFile(clientDir),
		sharedClipboardUpdatedAtFile(clientDir),
		sharedClipboardErrorFile(clientDir),
		sharedClipboardImageFile(clientDir),
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
	if !argPairExists(args, "-v", "/tmp/workspace:/workspace") {
		t.Error("missing workspace mount")
	}

	// Environment variables.
	if !argPairExists(args, "-e", "ANTHROPIC_API_KEY=sk-test-key") {
		t.Error("missing ANTHROPIC_API_KEY env")
	}
	if !argPairContains(args, "-e", "TERM=") {
		t.Error("missing TERM env")
	}
	// MITTENS_DIND is only set when DinD is active; no env var in baseline.
	if argPairExists(args, "-e", "MITTENS_DIND=false") {
		t.Error("MITTENS_DIND=false should not be set in baseline (non-DinD) mode")
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

	wantMount := credFile + ":/mnt/claude-config/.credentials.json:ro"
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

	// MITTENS_HOST_WORKSPACE should be set when EffectiveWorkspace != "/workspace".
	if !argPairContains(args, "-e", "MITTENS_HOST_WORKSPACE="+workspace) {
		t.Error("missing MITTENS_HOST_WORKSPACE env")
	}
}

func TestAssembleDockerArgs_CodexSessionPersistenceMountsWholeConfig(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	p := CodexProvider()
	os.MkdirAll(p.HostConfigDir(home), 0o755)

	a := &App{
		Provider:          p,
		NoHistory:         false,
		ContainerName:     "mittens-codex-session",
		WorkspaceMountSrc: "/tmp/ws",
		Workspace:         "/Users/test/project",
		Credentials:       &CredentialManager{},
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

	// MITTENS_DIND should be true (from resolver).
	if !argPairExists(args, "-e", "MITTENS_DIND=true") {
		t.Error("missing MITTENS_DIND=true")
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

	// Firewall domains.
	if !argPairContains(args, "-e", "MITTENS_FIREWALL_EXTRA=ext.example.com,api.ext.com") {
		t.Error("missing MITTENS_FIREWALL_EXTRA with extension domains")
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

	// Resolver docker args appended verbatim.
	if !argPairExists(args, "-v", "/resolver/path:/container/path:ro") {
		t.Error("missing resolver mount")
	}
	if !argPairExists(args, "-e", "RESOLVER_VAR=yes") {
		t.Error("missing resolver env var")
	}

	// Firewall domains aggregated: extension + resolver.
	if !argPairContains(args, "-e", "MITTENS_FIREWALL_EXTRA=") {
		t.Fatal("missing MITTENS_FIREWALL_EXTRA")
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "MITTENS_FIREWALL_EXTRA=") {
			val := strings.TrimPrefix(args[i+1], "MITTENS_FIREWALL_EXTRA=")
			if !strings.Contains(val, "ext-domain.com") {
				t.Error("MITTENS_FIREWALL_EXTRA missing extension domain")
			}
			if !strings.Contains(val, "resolver-domain.com") {
				t.Error("MITTENS_FIREWALL_EXTRA missing resolver domain")
			}
			break
		}
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
	claudeJSON := filepath.Join(home, ".claude.json") + ":/mnt/claude-config/.claude.json:ro"
	if !argPairExists(args, "-v", claudeJSON) {
		t.Error("missing .claude.json mount")
	}
	gitconfig := filepath.Join(home, ".gitconfig") + ":/mnt/claude-config/.gitconfig:ro"
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

	// Broker port env var.
	if !argPairExists(args, "-e", "MITTENS_BROKER_PORT=12345") {
		t.Error("missing MITTENS_BROKER_PORT env var")
	}
	if !argPairExists(args, "-e", "MITTENS_BROKER_TOKEN=broker-secret") {
		t.Error("missing MITTENS_BROKER_TOKEN env var")
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

	// Should NOT have broker env var.
	if argPairContains(args, "-e", "MITTENS_BROKER_PORT") {
		t.Error("MITTENS_BROKER_PORT should not be present without broker")
	}
	if argPairContains(args, "-e", "MITTENS_BROKER_TOKEN") {
		t.Error("MITTENS_BROKER_TOKEN should not be present without broker")
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

	// MITTENS_EXTRA_DIRS env var with colon-separated paths.
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "MITTENS_EXTRA_DIRS=") {
			val := strings.TrimPrefix(args[i+1], "MITTENS_EXTRA_DIRS=")
			parts := strings.Split(val, ":")
			if len(parts) != 2 {
				t.Errorf("MITTENS_EXTRA_DIRS should have 2 paths, got %d: %q", len(parts), val)
			}
			if parts[0] != dir1 || parts[1] != dir2 {
				t.Errorf("MITTENS_EXTRA_DIRS = %q, want %s:%s", val, dir1, dir2)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("missing MITTENS_EXTRA_DIRS env var")
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
	// Expect exactly 1 mount (the primary workspace mount at /workspace), not 2.
	if mountCount != 1 {
		t.Errorf("expected 1 mount for workspace dir, got %d", mountCount)
	}

	// MITTENS_EXTRA_DIRS should not be set (no extra dirs after dedup).
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "MITTENS_EXTRA_DIRS=") {
			t.Error("MITTENS_EXTRA_DIRS should not be set when extra dir matches workspace")
		}
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
	if !argPairExists(args, "-e", "MITTENS_INSTANCE_NAME=planner-1") {
		t.Error("missing MITTENS_INSTANCE_NAME env var")
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

	if argPairContains(args, "-e", "MITTENS_INSTANCE_NAME") {
		t.Error("MITTENS_INSTANCE_NAME should not be set without --name")
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

	if !argPairExists(args, "-e", "MITTENS_CONTAINER_NAME=mittens-42") {
		t.Error("missing MITTENS_CONTAINER_NAME env var")
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

	if !argPairExists(args, "-e", "MITTENS_NO_NOTIFY=true") {
		t.Error("missing MITTENS_NO_NOTIFY env var")
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

	if argPairContains(args, "-e", "MITTENS_NO_NOTIFY") {
		t.Error("MITTENS_NO_NOTIFY should not be set when notifications enabled")
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

	envVars := []string{
		"MITTENS_AI_BINARY=claude",
		"MITTENS_AI_CONFIG_DIR=.claude",
		"MITTENS_AI_CRED_FILE=.credentials.json",
		"MITTENS_AI_PREFS_FILE=.claude.json",
		"MITTENS_AI_SETTINGS_FILE=settings.json",
		"MITTENS_AI_PROJECT_FILE=CLAUDE.md",
		"MITTENS_AI_TRUSTED_DIRS_KEY=trustedDirectories",
		"MITTENS_AI_YOLO_KEY=skipDangerousModePermissionPrompt",
		"MITTENS_AI_MCP_SERVERS_KEY=mcpServers",
		"MITTENS_AI_SETTINGS_FORMAT=json",
		"MITTENS_AI_CONFIG_SUBDIRS=skills,hooks,agents,output-styles",
		"MITTENS_AI_PLUGIN_DIR=plugins",
		"MITTENS_AI_PLUGIN_FILES=installed_plugins.json,known_marketplaces.json,config.json",
	}
	for _, env := range envVars {
		if !argPairExists(args, "-e", env) {
			t.Errorf("missing env var %s", env)
		}
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

	// Codex-specific provider env vars.
	codexEnvVars := []string{
		"MITTENS_AI_BINARY=codex",
		"MITTENS_AI_CONFIG_DIR=.codex",
		"MITTENS_AI_CRED_FILE=auth.json",
		"MITTENS_AI_PREFS_FILE=",
		"MITTENS_AI_SETTINGS_FILE=config.toml",
		"MITTENS_AI_PROJECT_FILE=AGENTS.md",
		"MITTENS_AI_SETTINGS_FORMAT=toml",
		"MITTENS_AI_CONFIG_SUBDIRS=skills,hooks,agents,output-styles",
		"MITTENS_AI_PLUGIN_DIR=",
		"MITTENS_AI_PLUGIN_FILES=",
	}
	for _, env := range codexEnvVars {
		if !argPairExists(args, "-e", env) {
			t.Errorf("missing env var %s", env)
		}
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

	// Gemini-specific provider env vars.
	geminiEnvVars := []string{
		"MITTENS_AI_BINARY=gemini",
		"MITTENS_AI_CONFIG_DIR=.gemini",
		"MITTENS_AI_CRED_FILE=oauth_creds.json",
		"MITTENS_AI_SETTINGS_FILE=settings.json",
		"MITTENS_AI_PROJECT_FILE=GEMINI.md",
		"MITTENS_AI_SETTINGS_FORMAT=json",
		"MITTENS_AI_CONFIG_SUBDIRS=skills,hooks,agents,output-styles",
		"MITTENS_AI_TRUSTED_DIRS_FILE=trustedFolders.json",
	}
	for _, env := range geminiEnvVars {
		if !argPairExists(args, "-e", env) {
			t.Errorf("missing env var %s", env)
		}
	}

	// ContainerHostname for Gemini.
	if !argPairExists(args, "--hostname", "gemini-cli") {
		t.Error("missing --hostname gemini-cli for Gemini provider")
	}

	// PersistFiles mount/env check (logic exists in assembly but no direct env var for full list).
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

	if !argPairExists(args, "-e", "MITTENS_AI_SETTINGS_FORMAT=json") {
		t.Error("missing MITTENS_AI_SETTINGS_FORMAT=json for Claude provider")
	}
}

func TestMaybeApplyResumeArgs_CodexLatest(t *testing.T) {
	a := &App{
		Provider:      CodexProvider(),
		ResumeSession: "latest",
		ClaudeArgs:    []string{"--model", "gpt-5"},
	}

	a.maybeApplyResumeArgs()

	want := []string{"--resume", "latest", "--model", "gpt-5"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i := range want {
		if a.ClaudeArgs[i] != want[i] {
			t.Fatalf("ClaudeArgs[%d] = %q, want %q", i, a.ClaudeArgs[i], want[i])
		}
	}
}

func TestMaybeApplyResumeArgs_DoesNotDuplicate(t *testing.T) {
	a := &App{
		Provider:      CodexProvider(),
		ResumeSession: "latest",
		ClaudeArgs:    []string{"--resume", "abc123"},
	}

	a.maybeApplyResumeArgs()

	if len(a.ClaudeArgs) != 2 {
		t.Fatalf("ClaudeArgs = %v, want existing args unchanged", a.ClaudeArgs)
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

	// Provider firewall domains should appear in MITTENS_FIREWALL_EXTRA.
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "MITTENS_FIREWALL_EXTRA=") {
			val := strings.TrimPrefix(args[i+1], "MITTENS_FIREWALL_EXTRA=")
			for _, domain := range p.FirewallDomains {
				if !strings.Contains(val, domain) {
					t.Errorf("MITTENS_FIREWALL_EXTRA missing provider domain %s", domain)
				}
			}
			return
		}
	}
	t.Error("missing MITTENS_FIREWALL_EXTRA env var")
}
