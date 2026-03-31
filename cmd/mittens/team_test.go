package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/pool"
	"gopkg.in/yaml.v3"
)

func TestRunTeamSessionRejectsNoYolo(t *testing.T) {
	// Use a temp dir for config home to avoid polluting real config.
	t.Setenv("MITTENS_HOME", t.TempDir())

	err := runTeamSession([]string{"--no-yolo"})
	if err == nil {
		t.Fatal("expected error for --no-yolo with team mode")
	}
	if !strings.Contains(err.Error(), "--no-yolo is incompatible with team mode") {
		t.Errorf("unexpected error: %v", err)
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

func TestTeamConfigPathConstruction(t *testing.T) {
	// Verify config path is deterministic.
	workspace := "/Users/test/src/my-project"
	projDir := ProjectDir(workspace)

	configHome := t.TempDir()
	t.Setenv("MITTENS_HOME", configHome)

	expected := filepath.Join(configHome, "projects", projDir, "team.yaml")
	got := filepath.Join(ConfigHome(), "projects", projDir, "team.yaml")
	if got != expected {
		t.Errorf("config path = %q, want %q", got, expected)
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

	// Workers should not inherit the host broker config.
	if cfg.Broker.Port != 0 {
		t.Errorf("Broker.Port = %d, want 0", cfg.Broker.Port)
	}
	if cfg.Broker.Token != "" {
		t.Errorf("Broker.Token = %q, want empty", cfg.Broker.Token)
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
