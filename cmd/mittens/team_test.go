package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/pool"
	"gopkg.in/yaml.v3"
)

func installDockerStub(t *testing.T, script string) string {
	t.Helper()
	tmp := t.TempDir()
	dockerPath := filepath.Join(tmp, "docker")
	if err := os.WriteFile(dockerPath, []byte(script), 0755); err != nil {
		t.Fatalf("write docker stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return tmp
}

func TestRunTeamSessionRejectsNoYolo(t *testing.T) {
	// Use a temp dir for config home to avoid polluting real config.
	configHome := t.TempDir()
	t.Setenv("MITTENS_HOME", configHome)
	configPath := teamConfigPath("strike-team-a")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir team config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(teamConfigFileHeader+"max_workers: 4\n"), 0644); err != nil {
		t.Fatalf("write team config: %v", err)
	}

	err := runTeamSession([]string{"--name", "strike-team-a", "--no-yolo"})
	if err == nil {
		t.Fatal("expected error for --no-yolo with team mode")
	}
	if !strings.Contains(err.Error(), "--no-yolo is incompatible with team mode") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunTeamSessionRejectsInvalidSessionName(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())

	err := runTeamSession([]string{"--name", "../escape"})
	if err == nil {
		t.Fatal("expected error for invalid team session name")
	}
	if !strings.Contains(err.Error(), "invalid team name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunTeamSessionRequiresNamedTeam(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())

	err := runTeamSession(nil)
	if err == nil {
		t.Fatal("expected error when team name is missing")
	}
	if !strings.Contains(err.Error(), "team name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunTeamSessionRejectsMissingNamedTeamConfig(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())

	err := runTeamSession([]string{"--name", "strike-team-a"})
	if err == nil {
		t.Fatal("expected error for missing named team config")
	}
	if !strings.Contains(err.Error(), `team "strike-team-a" is not configured`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleTeamDispatch(t *testing.T) {
	// Verify that handleTeam dispatches subcommands correctly.
	// We can't fully test the run path without Docker, but we can test
	// that help doesn't error.
	if err := handleTeam([]string{"help"}); err != nil {
		t.Fatalf("handleTeam help: %v", err)
	}
	if err := handleTeam([]string{"--help"}); err != nil {
		t.Fatalf("handleTeam --help: %v", err)
	}
}

func TestListTeamConfigs(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MITTENS_HOME", configHome)

	alphaPath := teamConfigPath("alpha")
	if err := os.MkdirAll(filepath.Dir(alphaPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alphaPath, []byte(teamConfigFileHeader+"max_workers: 4\n"), 0644); err != nil {
		t.Fatal(err)
	}
	betaPath := teamConfigPath("beta")
	if err := os.MkdirAll(filepath.Dir(betaPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaPath, []byte(teamConfigFileHeader+"max_workers: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(ConfigHome(), "projects", "proj", "pools", "alpha-abc123")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "events.jsonl"), []byte(`{"ts":"2026-04-01T00:00:00Z"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "session.json"), []byte(`{"workspace":"w","teamName":"alpha","sessionId":"alpha-abc123","startedAt":"2026-04-01T00:00:00Z"}`), 0644); err != nil {
		t.Fatal(err)
	}

	teams, err := listTeamConfigs(ConfigHome())
	if err != nil {
		t.Fatalf("listTeamConfigs: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("len(teams) = %d, want 2", len(teams))
	}
	if teams[0].Name != "alpha" || teams[0].SessionCount != 1 {
		t.Fatalf("teams[0] = %+v, want alpha with one session", teams[0])
	}
	if teams[1].Name != "beta" || teams[1].SessionCount != 0 {
		t.Fatalf("teams[1] = %+v, want beta with zero sessions", teams[1])
	}
}

func TestListTeamConfigsRespectsConfigHomeArgument(t *testing.T) {
	configHome := t.TempDir()
	otherHome := t.TempDir()
	t.Setenv("MITTENS_HOME", otherHome)

	configPath := filepath.Join(configHome, "teams", "alpha", "team.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(teamConfigFileHeader+"max_workers: 4\n"), 0644); err != nil {
		t.Fatal(err)
	}

	teams, err := listTeamConfigs(configHome)
	if err != nil {
		t.Fatalf("listTeamConfigs: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("len(teams) = %d, want 1", len(teams))
	}
	if teams[0].Name != "alpha" {
		t.Fatalf("teams[0] = %+v, want alpha", teams[0])
	}
}

func TestHandleTeamListJSON(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MITTENS_HOME", configHome)
	configPath := teamConfigPath("strike-team-a")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(teamConfigFileHeader+"max_workers: 4\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := handleTeamList([]string{"--json"}); err != nil {
		t.Fatalf("handleTeamList: %v", err)
	}
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	var teams []teamConfigMeta
	if err := json.Unmarshal(buf.Bytes(), &teams); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(teams) != 1 || teams[0].Name != "strike-team-a" {
		t.Fatalf("teams = %+v, want strike-team-a", teams)
	}
}

func TestTeamYAMLConfigMarshal(t *testing.T) {
	cfg := teamYAMLConfig{
		MaxWorkers: 6,
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var parsed teamYAMLConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MaxWorkers != 6 {
		t.Errorf("MaxWorkers = %d, want 6", parsed.MaxWorkers)
	}
}

func TestTeamRoleSpecIncludesDefaultFallback(t *testing.T) {
	got := teamRoleSpec(" Default ")
	if got.Key != "default" {
		t.Fatalf("teamRoleSpec(default).Key = %q, want default", got.Key)
	}
	if got.Label != "Default" {
		t.Fatalf("teamRoleSpec(default).Label = %q, want Default", got.Label)
	}
	if !strings.Contains(strings.ToLower(got.Summary), "fallback") {
		t.Fatalf("teamRoleSpec(default).Summary = %q, want fallback summary", got.Summary)
	}
}

func TestTeamRolePromptDefaults(t *testing.T) {
	got := teamRolePromptDefaults(" openai ")
	if got.Provider != "codex" {
		t.Fatalf("teamRolePromptDefaults.Provider = %q, want codex", got.Provider)
	}
	if got.Model != "gpt-5.4" {
		t.Fatalf("teamRolePromptDefaults.Model = %q, want gpt-5.4", got.Model)
	}
}

func TestTeamConfigEditorSetModelNormalizesAndDropsEmpty(t *testing.T) {
	editor := newTeamConfigEditor()
	editor.SetModel(" Planner ", teamConfigRoleInput{
		Provider: " openai ",
		Model:    " gpt-5.4 ",
	})

	got := editor.Model("planner")
	if got.Provider != "codex" {
		t.Fatalf("editor.Model(planner).Provider = %q, want codex", got.Provider)
	}
	if got.Model != "gpt-5.4" {
		t.Fatalf("editor.Model(planner).Model = %q, want gpt-5.4", got.Model)
	}

	editor.SetModel("planner", teamConfigRoleInput{})
	if got := editor.Model("planner"); got != (teamConfigRoleInput{}) {
		t.Fatalf("editor.Model(planner) after empty set = %#v, want zero value", got)
	}
}

func TestLoadTeamConfigEditorNormalizesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "team.yaml")
	content := `max_workers: 0
models:
  Default:
    provider: " openai "
    model: " gpt-5.4 "
  Planner:
    provider: " openai "
    model: " gpt-5.4 "
    adapter: " openai-codex "
  Implementer:
    model: " claude-sonnet-4-6 "
    adapter: " claude-code "
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	editor, err := loadTeamConfigEditor(path)
	if err != nil {
		t.Fatalf("loadTeamConfigEditor: %v", err)
	}
	if editor.MaxWorkers != teamConfigDefaultMaxWorkers {
		t.Fatalf("editor.MaxWorkers = %d, want %d", editor.MaxWorkers, teamConfigDefaultMaxWorkers)
	}
	if got := editor.Model("planner").Provider; got != "codex" {
		t.Fatalf("editor.Model(planner).Provider = %q, want codex", got)
	}
	if got := editor.Model("default").Model; got != "gpt-5.4" {
		t.Fatalf("editor.Model(default).Model = %q, want gpt-5.4", got)
	}
	if got := editor.Model("implementer").Provider; got != "claude" {
		t.Fatalf("editor.Model(implementer).Provider = %q, want claude", got)
	}
}

func TestTeamRoleSummaryUsesInheritance(t *testing.T) {
	editor := newTeamConfigEditor()
	editor.SetModel("default", teamConfigRoleInput{Provider: "codex", Model: "gpt-5.4"})
	if got := teamRoleSummary(editor, "planner", "claude"); got != "inherits default" {
		t.Fatalf("teamRoleSummary(planner) = %q, want inherits default", got)
	}
	if got := teamRoleSummary(editor, "default", "claude"); got != "codex / gpt-5.4" {
		t.Fatalf("teamRoleSummary(default) = %q, want codex / gpt-5.4", got)
	}
}

func TestTeamProcessSummary(t *testing.T) {
	if got := teamProcessSummary(pool.SessionReuseConfig{}); got != "fresh session each task" {
		t.Fatalf("teamProcessSummary(disabled) = %q, want fresh session each task", got)
	}
	if got := teamProcessSummary(pool.SessionReuseConfig{
		Enabled:      true,
		TTLSeconds:   300,
		MaxTasks:     3,
		MaxTokens:    100000,
		SameRoleOnly: true,
	}); got != "reuse 3 tasks / 300s / same role" {
		t.Fatalf("teamProcessSummary(enabled) = %q", got)
	}
}

func TestTeamConfigEditorSaveWritesHeaderAndModels(t *testing.T) {
	editor := newTeamConfigEditor()
	editor.SetMaxWorkers("7")
	editor.SetModel("default", teamConfigRoleInput{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-6",
	})
	editor.SessionReuse = pool.SessionReuseConfig{
		Enabled:      true,
		TTLSeconds:   600,
		MaxTasks:     5,
		MaxTokens:    150000,
		SameRoleOnly: false,
	}

	path := filepath.Join(t.TempDir(), "nested", "team.yaml")
	if err := editor.Save(path); err != nil {
		t.Fatalf("editor.Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(data), teamConfigFileHeader) {
		t.Fatalf("saved file missing expected header: %q", string(data))
	}
	if strings.Contains(string(data), "adapter:") {
		t.Fatalf("saved file unexpectedly preserved adapter field: %q", string(data))
	}

	cfg, err := pool.LoadTeamConfig(path)
	if err != nil {
		t.Fatalf("LoadTeamConfig(saved): %v", err)
	}
	if cfg.MaxWorkers != 7 {
		t.Fatalf("saved MaxWorkers = %d, want 7", cfg.MaxWorkers)
	}
	if got := cfg.Models["default"].Provider; got != "claude" {
		t.Fatalf("saved default provider = %q, want claude", got)
	}
	if got := cfg.Models["default"].Adapter; got != "" {
		t.Fatalf("saved default adapter = %q, want empty", got)
	}
	if !cfg.SessionReuse.Enabled {
		t.Fatal("saved session reuse should be enabled")
	}
	if cfg.SessionReuse.TTLSeconds != 600 {
		t.Fatalf("saved session reuse ttl = %d, want 600", cfg.SessionReuse.TTLSeconds)
	}
	if cfg.SessionReuse.SameRoleOnly {
		t.Fatal("saved session reuse same_role_only = true, want false")
	}
}

func TestNamedTeamConfigPathConstruction(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("MITTENS_HOME", configHome)

	expected := filepath.Join(configHome, "teams", "strike-team-a", "team.yaml")
	got := teamConfigPath("strike-team-a")
	if got != expected {
		t.Errorf("config path = %q, want %q", got, expected)
	}
}

func TestGenerateTeamSessionIDUsesTeamPrefix(t *testing.T) {
	got := generateTeamSessionID("strike-team-a")
	if !strings.HasPrefix(got, "strike-team-a-") {
		t.Fatalf("generateTeamSessionID = %q, want team prefix", got)
	}
}

func TestCleanupTeamSession(t *testing.T) {
	// Verify cleanup preserves state dir but removes workers/ dir.
	stateDir := t.TempDir()
	subDir := filepath.Join(stateDir, "test-session")
	workersDir := filepath.Join(subDir, "workers", "w-1")
	os.MkdirAll(workersDir, 0755)
	os.WriteFile(filepath.Join(subDir, "events.jsonl"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(workersDir, "result.txt"), []byte("output"), 0644)

	cleanupTeamSession(subDir, "test-session")

	// State dir and WAL preserved for resume.
	if _, err := os.Stat(subDir); os.IsNotExist(err) {
		t.Error("expected state dir to be preserved")
	}
	if _, err := os.Stat(filepath.Join(subDir, "events.jsonl")); os.IsNotExist(err) {
		t.Error("expected WAL to be preserved")
	}
	// Workers dir removed (ephemeral).
	if _, err := os.Stat(filepath.Join(subDir, "workers")); !os.IsNotExist(err) {
		t.Error("expected workers dir to be removed")
	}
}

func TestCleanupTeamSessionRemovesWorkersInAnyState(t *testing.T) {
	stateDir := t.TempDir()
	subDir := filepath.Join(stateDir, "test-session")
	workersDir := filepath.Join(subDir, "workers", "w-1")
	if err := os.MkdirAll(workersDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "docker.log")
	installDockerStub(t, "#!/bin/sh\n"+
		"printf '%s\\n' \"$@\" >> '"+logPath+"'\n"+
		"printf '--\\n' >> '"+logPath+"'\n"+
		"case \"$1\" in\n"+
		"  ps)\n"+
		"    printf 'cid-running\\tw-1\\trunning\\tUp 5 minutes\\ncid-exited\\tw-2\\texited\\tExited (0) 1 minute ago\\n'\n"+
		"    ;;\n"+
		"esac\n")

	cleanupTeamSession(subDir, "test-session")

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "ps\n-a\n") {
		t.Fatalf("docker log = %q, want ps -a", log)
	}
	if !strings.Contains(log, "label=mittens.pool=test-session") {
		t.Fatalf("docker log = %q, want pool label filter", log)
	}
	if !strings.Contains(log, "label=mittens.role=worker") {
		t.Fatalf("docker log = %q, want worker label filter", log)
	}
	if !strings.Contains(log, "stop\n-t\n10\ncid-running\n") {
		t.Fatalf("docker log = %q, want stop for running worker", log)
	}
	if strings.Contains(log, "stop\n-t\n10\ncid-exited\n") {
		t.Fatalf("docker log = %q, did not expect stop for exited worker", log)
	}
	if !strings.Contains(log, "rm\n-f\ncid-running\n") || !strings.Contains(log, "rm\n-f\ncid-exited\n") {
		t.Fatalf("docker log = %q, want rm -f for both workers", log)
	}
	if _, err := os.Stat(filepath.Join(subDir, "workers")); !os.IsNotExist(err) {
		t.Fatal("expected workers dir to be removed")
	}
}

func TestBuildWorkerInitConfig(t *testing.T) {
	app := &App{
		Provider: DefaultProvider(),
		Verbose:  true,
		Yolo:     false,
		Extensions: []*registry.Extension{
			{Name: "firewall", Enabled: true},
			{Name: "aws", Enabled: false},
		},
		brokerPort:  12345,
		brokerToken: "test-token",
	}
	app.EffectiveWorkspace = "/test/workspace"

	cfg := app.buildWorkerInitConfig(DefaultProvider(), "mittens-team-1-w-1")

	// Worker-specific flags.
	if !cfg.Flags.PrintMode {
		t.Error("expected PrintMode = true")
	}
	if !cfg.Flags.Yolo {
		t.Error("expected Yolo = true")
	}
	if cfg.Flags.TeamMCP {
		t.Error("expected TeamMCP = false")
	}
	if !cfg.Flags.Firewall {
		t.Error("expected Firewall = true (extension enabled)")
	}
	if !cfg.Flags.NoNotify {
		t.Error("expected NoNotify = true for workers")
	}
	// Verbose should NOT be copied blindly — workers respect the leader's verbose.
	if !cfg.Flags.Verbose {
		t.Error("expected Verbose = true (inherited from leader)")
	}

	// AI section should match provider.
	if cfg.AI.SkipPermsFlag != "--dangerously-skip-permissions" {
		t.Errorf("AI.SkipPermsFlag = %q, want --dangerously-skip-permissions", cfg.AI.SkipPermsFlag)
	}
	if cfg.AI.Binary != "claude" {
		t.Errorf("AI.Binary = %q, want %q", cfg.AI.Binary, "claude")
	}
	if cfg.AI.ConfigDir != ".claude" {
		t.Errorf("AI.ConfigDir = %q, want %q", cfg.AI.ConfigDir, ".claude")
	}
	if cfg.AI.CredFile != ".credentials.json" {
		t.Errorf("AI.CredFile = %q, want %q", cfg.AI.CredFile, ".credentials.json")
	}

	// Workers must inherit the host broker config so they can sync credentials.
	if cfg.Broker.Port != 12345 {
		t.Errorf("Broker.Port = %d, want 12345", cfg.Broker.Port)
	}
	if cfg.Broker.Token != "test-token" {
		t.Errorf("Broker.Token = %q, want test-token", cfg.Broker.Token)
	}

	// Container name and workspace.
	if cfg.ContainerName != "mittens-team-1-w-1" {
		t.Errorf("ContainerName = %q, want %q", cfg.ContainerName, "mittens-team-1-w-1")
	}
	if cfg.HostWorkspace != "/test/workspace" {
		t.Errorf("HostWorkspace = %q, want %q", cfg.HostWorkspace, "/test/workspace")
	}
}

func TestBuildWorkerInitConfigFirewallDisabled(t *testing.T) {
	app := &App{
		Provider:   DefaultProvider(),
		Extensions: []*registry.Extension{},
	}

	cfg := app.buildWorkerInitConfig(DefaultProvider(), "w-test")
	if cfg.Flags.Firewall {
		t.Error("expected Firewall = false when no firewall extension enabled")
	}
}

func TestSpawnWorkerFirewallMount(t *testing.T) {
	// Verify that when firewallConfPath is set, spawnWorkerContainer includes the mount.
	// We can't actually run docker, but we can test buildWorkerInitConfig + verify
	// the field is respected by checking the App state.
	app := &App{
		Provider:         DefaultProvider(),
		Extensions:       []*registry.Extension{{Name: "firewall", Enabled: true}},
		teamSessionID:    "test",
		firewallConfPath: "/tmp/test-firewall.conf",
	}

	// buildWorkerInitConfig should set Firewall=true.
	cfg := app.buildWorkerInitConfig(DefaultProvider(), "test-container")
	if !cfg.Flags.Firewall {
		t.Error("expected Firewall = true")
	}

	// Verify firewallConfPath is accessible on the App for mount assembly.
	if app.firewallConfPath != "/tmp/test-firewall.conf" {
		t.Errorf("firewallConfPath = %q, want /tmp/test-firewall.conf", app.firewallConfPath)
	}
}

func TestSpawnWorkerNoFirewallMount(t *testing.T) {
	app := &App{
		Provider:      DefaultProvider(),
		Extensions:    []*registry.Extension{},
		teamSessionID: "test",
	}

	if app.firewallConfPath != "" {
		t.Error("expected empty firewallConfPath when firewall not resolved")
	}
}

func TestSpawnWorkerContainerRequiresID(t *testing.T) {
	app := &App{
		Provider:      DefaultProvider(),
		teamSessionID: "test-session",
	}

	_, _, err := app.spawnWorkerContainer(pool.WorkerSpec{})
	if err == nil {
		t.Fatal("expected error for empty spec.ID")
	}
	if err.Error() != "spawn worker: spec.ID is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSpawnWorkerContainerRemovesStaleExactNameContainer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logPath := filepath.Join(t.TempDir(), "docker.log")
	installDockerStub(t, "#!/bin/sh\n"+
		"printf '%s\\n' \"$@\" >> '"+logPath+"'\n"+
		"printf '--\\n' >> '"+logPath+"'\n"+
		"case \"$1\" in\n"+
		"  ps)\n"+
		"    printf 'cid-stale\\tmittens-test-session-w-1\\texited\\tExited (0) 1 minute ago\\n'\n"+
		"    ;;\n"+
		"  port)\n"+
		"    exit 1\n"+
		"    ;;\n"+
		"  run)\n"+
		"    printf 'cid-new\\n'\n"+
		"    ;;\n"+
		"esac\n")

	app := &App{
		Provider:           DefaultProvider(),
		teamSessionID:      "test-session",
		teamStateDir:       t.TempDir(),
		WorkspaceMountSrc:  t.TempDir(),
		EffectiveWorkspace: "/workspace",
		ContainerName:      "mittens-team-test-session",
		ImageName:          "mittens",
		ImageTag:           "latest",
		Yolo:               true,
	}

	containerName, containerID, err := app.spawnWorkerContainer(pool.WorkerSpec{ID: "w-1"})
	if err != nil {
		t.Fatalf("spawnWorkerContainer: %v", err)
	}
	if containerName != "mittens-test-session-w-1" {
		t.Fatalf("containerName = %q, want mittens-test-session-w-1", containerName)
	}
	if containerID != "cid-new" {
		t.Fatalf("containerID = %q, want cid-new", containerID)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "name=^/mittens-test-session-w-1$") {
		t.Fatalf("docker log = %q, want exact-name filter", log)
	}
	rmIdx := strings.Index(log, "rm\n-f\ncid-stale\n")
	runIdx := strings.Index(log, "run\n-d\n")
	if rmIdx == -1 {
		t.Fatalf("docker log = %q, want stale container removal", log)
	}
	if runIdx == -1 {
		t.Fatalf("docker log = %q, want docker run", log)
	}
	if rmIdx > runIdx {
		t.Fatalf("docker log = %q, want stale removal before docker run", log)
	}
}

func TestSpawnWorkerContainerRemovesStaleExactNameContainerWithRegexMetaSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logPath := filepath.Join(t.TempDir(), "docker.log")
	installDockerStub(t, "#!/bin/sh\n"+
		"printf '%s\\n' \"$@\" >> '"+logPath+"'\n"+
		"printf '--\\n' >> '"+logPath+"'\n"+
		"case \"$1\" in\n"+
		"  ps)\n"+
		"    exact=0\n"+
		"    for arg in \"$@\"; do\n"+
		"      if [ \"$arg\" = 'name=^/mittens-release\\.v1-w-1$' ]; then\n"+
		"        exact=1\n"+
		"      fi\n"+
		"    done\n"+
		"    if [ \"$exact\" -eq 1 ]; then\n"+
		"      printf 'cid-stale\\tmittens-release.v1-w-1\\texited\\tExited (0) 1 minute ago\\n'\n"+
		"    else\n"+
		"      printf 'cid-other\\tmittens-releaseXv1-w-1\\trunning\\tUp 5 minutes\\n'\n"+
		"    fi\n"+
		"    ;;\n"+
		"  port)\n"+
		"    exit 1\n"+
		"    ;;\n"+
		"  run)\n"+
		"    printf 'cid-new\\n'\n"+
		"    ;;\n"+
		"esac\n")

	app := &App{
		Provider:           DefaultProvider(),
		teamSessionID:      "release.v1",
		teamStateDir:       t.TempDir(),
		WorkspaceMountSrc:  t.TempDir(),
		EffectiveWorkspace: "/workspace",
		ContainerName:      "mittens-team-release.v1",
		ImageName:          "mittens",
		ImageTag:           "latest",
		Yolo:               true,
	}

	containerName, containerID, err := app.spawnWorkerContainer(pool.WorkerSpec{ID: "w-1"})
	if err != nil {
		t.Fatalf("spawnWorkerContainer: %v", err)
	}
	if containerName != "mittens-release.v1-w-1" {
		t.Fatalf("containerName = %q, want mittens-release.v1-w-1", containerName)
	}
	if containerID != "cid-new" {
		t.Fatalf("containerID = %q, want cid-new", containerID)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	log := string(logData)
	if !strings.Contains(log, "name=^/mittens-release\\.v1-w-1$") {
		t.Fatalf("docker log = %q, want regexp-quoted exact-name filter", log)
	}
	if strings.Contains(log, "rm\n-f\ncid-other\n") {
		t.Fatalf("docker log = %q, did not expect regex-neighbor container removal", log)
	}
	rmIdx := strings.Index(log, "rm\n-f\ncid-stale\n")
	runIdx := strings.Index(log, "run\n-d\n")
	if rmIdx == -1 {
		t.Fatalf("docker log = %q, want stale exact-name container removal", log)
	}
	if runIdx == -1 {
		t.Fatalf("docker log = %q, want docker run", log)
	}
	if rmIdx > runIdx {
		t.Fatalf("docker log = %q, want stale removal before docker run", log)
	}
}

func TestSpawnWorkerContainerFailsWhenExactNameContainerIsActive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logPath := filepath.Join(t.TempDir(), "docker.log")
	installDockerStub(t, "#!/bin/sh\n"+
		"printf '%s\\n' \"$@\" >> '"+logPath+"'\n"+
		"printf '--\\n' >> '"+logPath+"'\n"+
		"case \"$1\" in\n"+
		"  ps)\n"+
		"    printf 'cid-running\\tmittens-test-session-w-1\\trunning\\tUp 5 minutes\\n'\n"+
		"    ;;\n"+
		"esac\n")

	app := &App{
		Provider:           DefaultProvider(),
		teamSessionID:      "test-session",
		teamStateDir:       t.TempDir(),
		WorkspaceMountSrc:  t.TempDir(),
		EffectiveWorkspace: "/workspace",
		ContainerName:      "mittens-team-test-session",
		ImageName:          "mittens",
		ImageTag:           "latest",
		Yolo:               true,
	}

	_, _, err := app.spawnWorkerContainer(pool.WorkerSpec{ID: "w-1"})
	if err == nil {
		t.Fatal("expected spawnWorkerContainer to fail when exact-name container is active")
	}
	if !strings.Contains(err.Error(), `worker container "mittens-test-session-w-1" already exists in "running" state`) {
		t.Fatalf("unexpected error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	log := string(logData)
	if strings.Contains(log, "rm\n-f\n") {
		t.Fatalf("docker log = %q, did not expect stale removal", log)
	}
	if strings.Contains(log, "run\n-d\n") {
		t.Fatalf("docker log = %q, did not expect docker run", log)
	}
}

func TestBuildWorkerInitConfig_CodexProvider(t *testing.T) {
	app := &App{
		Provider:    DefaultProvider(),
		Verbose:     true,
		brokerPort:  12345,
		brokerToken: "test-token",
		Extensions:  []*registry.Extension{},
	}
	app.EffectiveWorkspace = "/test/workspace"

	cfg := app.buildWorkerInitConfig(CodexProvider(), "mittens-team-1-w-1")

	if cfg.AI.Binary != "codex" {
		t.Fatalf("AI.Binary = %q, want codex", cfg.AI.Binary)
	}
	if cfg.AI.ConfigDir != ".codex" {
		t.Fatalf("AI.ConfigDir = %q, want .codex", cfg.AI.ConfigDir)
	}
	if cfg.AI.CredFile != "auth.json" {
		t.Fatalf("AI.CredFile = %q, want auth.json", cfg.AI.CredFile)
	}
	if cfg.AI.SkipPermsFlag != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("AI.SkipPermsFlag = %q", cfg.AI.SkipPermsFlag)
	}
}

func TestBuildWorkerInitConfig_FirewallExtra(t *testing.T) {
	codex := CodexProvider()
	app := &App{
		Provider: DefaultProvider(),
		Extensions: []*registry.Extension{
			{Name: "firewall", Enabled: true},
			{Name: "aws", Enabled: true, Firewall: []string{"s3.amazonaws.com"}},
			{Name: "disabled-ext", Enabled: false, Firewall: []string{"should-not-appear.example.com"}},
		},
	}

	cfg := app.buildWorkerInitConfig(codex, "w-test")

	// Provider domains must be present.
	for _, domain := range codex.FirewallDomains {
		found := false
		for _, d := range cfg.FirewallExtra {
			if d == domain {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("FirewallExtra missing provider domain %q", domain)
		}
	}

	// Enabled extension domain must be present.
	found := false
	for _, d := range cfg.FirewallExtra {
		if d == "s3.amazonaws.com" {
			found = true
			break
		}
	}
	if !found {
		t.Error("FirewallExtra missing enabled extension domain s3.amazonaws.com")
	}

	// Disabled extension domain must not be present.
	for _, d := range cfg.FirewallExtra {
		if d == "should-not-appear.example.com" {
			t.Error("FirewallExtra contains domain from disabled extension")
		}
	}

	// FirewallExtra must be non-empty overall.
	if len(cfg.FirewallExtra) == 0 {
		t.Error("FirewallExtra is empty, expected provider and extension domains")
	}
}

func TestResolveWorkerProviderAliases(t *testing.T) {
	got, err := resolveWorkerProvider("openai", DefaultProvider())
	if err != nil {
		t.Fatalf("resolveWorkerProvider(openai): %v", err)
	}
	if got.Name != "codex" {
		t.Fatalf("resolveWorkerProvider(openai).Name = %q, want codex", got.Name)
	}

	got, err = resolveWorkerProvider("anthropic", CodexProvider())
	if err != nil {
		t.Fatalf("resolveWorkerProvider(anthropic): %v", err)
	}
	if got.Name != "claude" {
		t.Fatalf("resolveWorkerProvider(anthropic).Name = %q, want claude", got.Name)
	}
}

func TestExtraTeamProvidersSkipsPrimaryAndCanonicalizes(t *testing.T) {
	tc := &pool.TeamConfig{
		Models: map[string]pool.ModelConfig{
			"planner":     {Provider: "anthropic"},
			"implementer": {Provider: "openai"},
			"reviewer":    {Provider: "openai"},
		},
	}

	got, err := extraTeamProviders(tc, ClaudeProvider())
	if err != nil {
		t.Fatalf("extraTeamProviders: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(extraTeamProviders) = %d, want 1", len(got))
	}
	if got[0].Name != "codex" {
		t.Fatalf("extraTeamProviders[0].Name = %q, want codex", got[0].Name)
	}
}
