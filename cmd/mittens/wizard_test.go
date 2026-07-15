package main

import (
	"reflect"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestParseExistingConfig_SeparatesProviders(t *testing.T) {
	dirs, providers, exts, firewall, opts := parseExistingConfig([]string{
		"--dir /tmp/a",
		"--dir-ro /tmp/b",
		"--provider codex",
		"--provider claude",
		"--aws dev",
		"--firewall-dev",
		"--worker",
		"--planner",
		"--no-yolo",
	})

	if len(dirs) != 2 || dirs[0] != "--dir /tmp/a" || dirs[1] != "--dir-ro /tmp/b" {
		t.Fatalf("unexpected dirs: %v", dirs)
	}
	if len(providers) != 2 || providers[0] != "--provider codex" || providers[1] != "--provider claude" {
		t.Fatalf("unexpected providers: %v", providers)
	}
	if len(exts) != 1 || exts[0] != "--aws dev" {
		t.Fatalf("unexpected exts: %v", exts)
	}
	if len(firewall) != 1 || firewall[0] != "--firewall-dev" {
		t.Fatalf("unexpected firewall: %v", firewall)
	}
	if len(opts) != 1 || opts[0] != "--no-yolo" {
		t.Fatalf("unexpected opts: %v", opts)
	}
}

func TestParseProviderLines(t *testing.T) {
	selected, def := parseProviderLines([]string{
		"--provider codex",
		"--provider ollama",
		"--provider gemini",
	})

	if !selected["codex"] || !selected["ollama"] || !selected["gemini"] {
		t.Fatalf("expected selected codex, ollama and gemini, got %v", selected)
	}
	if def != "gemini" {
		t.Fatalf("expected default provider gemini, got %q", def)
	}
}

func TestProviderWizardStateFromLines(t *testing.T) {
	state := providerWizardStateFromLines([]string{
		"--provider codex",
		"--provider ollama",
	}, ProviderWizardConfig{
		Endpoint: "http://host.docker.internal:11434",
		Model:    "qwen3-coder:30b",
	})

	if !reflect.DeepEqual(state.Selected, []string{"codex", "ollama"}) {
		t.Fatalf("selected = %#v", state.Selected)
	}
	if state.Default != "ollama" {
		t.Fatalf("default = %q, want ollama", state.Default)
	}
	if state.Config.Endpoint != "http://host.docker.internal:11434" || state.Config.Model != "qwen3-coder:30b" {
		t.Fatalf("config = %#v", state.Config)
	}

	wantLines := []string{"--provider codex", "--provider ollama"}
	if got := state.ProviderLines(); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("ProviderLines = %#v, want %#v", got, wantLines)
	}
}

func TestProviderWizardStateFromPolicy(t *testing.T) {
	state := providerWizardStateFromPolicy(ProviderPolicy{
		Name:     "ollama",
		Endpoint: "http://host.docker.internal:11434",
		Model:    "qwen3-coder:30b",
	})

	if !reflect.DeepEqual(state.Selected, []string{"ollama"}) {
		t.Fatalf("selected = %#v", state.Selected)
	}
	if state.Default != "ollama" {
		t.Fatalf("default = %q, want ollama", state.Default)
	}
	if state.Config.Endpoint != "http://host.docker.internal:11434" || state.Config.Model != "qwen3-coder:30b" {
		t.Fatalf("config = %#v", state.Config)
	}
}

func TestProviderWizardStateFromPolicy_ClaudeOpenAIBackend(t *testing.T) {
	state := providerWizardStateFromPolicy(ProviderPolicy{
		Name:     "claude",
		Backend:  "openai",
		Endpoint: "http://host.docker.internal:9223",
		Model:    "claude-sonnet-4-6",
	})

	if !reflect.DeepEqual(state.Selected, []string{"claude"}) {
		t.Fatalf("selected = %#v", state.Selected)
	}
	if state.Default != "claude" {
		t.Fatalf("default = %q, want claude", state.Default)
	}
	if state.Config.Backend != "openai" || state.Config.Endpoint != "http://host.docker.internal:9223" || state.Config.Model != "claude-sonnet-4-6" {
		t.Fatalf("config = %#v", state.Config)
	}
}

func TestProviderWizardStateNormalizesDefaultProvider(t *testing.T) {
	state := normalizeProviderWizardState(ProviderWizardState{
		Selected: []string{"codex", "gemini"},
		Default:  "ollama",
	})

	wantSelected := []string{"codex", "gemini", "ollama"}
	if !reflect.DeepEqual(state.Selected, wantSelected) {
		t.Fatalf("selected = %#v, want %#v", state.Selected, wantSelected)
	}
	if state.Default != "ollama" {
		t.Fatalf("default = %q, want ollama", state.Default)
	}

	wantLines := []string{"--provider codex", "--provider gemini", "--provider ollama"}
	if got := state.ProviderLines(); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("ProviderLines = %#v, want %#v", got, wantLines)
	}
}

func TestNormalizeProviderSelectionFallsBackToDefault(t *testing.T) {
	got := normalizeProviderSelection(nil, "codex")
	want := []string{"codex"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %#v, want %#v", got, want)
	}
}

func TestNormalizeProviderSelectionFiltersInvalidAndDuplicates(t *testing.T) {
	got := normalizeProviderSelection([]string{"codex", "unknown", "codex", "gemini"}, "claude")
	want := []string{"codex", "gemini"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %#v, want %#v", got, want)
	}
}

func TestNormalizeProviderSelectionFallsBackToClaudeForInvalidDefault(t *testing.T) {
	got := normalizeProviderSelection([]string{"unknown"}, "unknown")
	want := []string{"claude"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %#v, want %#v", got, want)
	}
}

func TestProviderLinesUseCodexHarness(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{name: "codex", lines: []string{"--provider codex"}, want: true},
		{name: "ollama", lines: []string{"--provider ollama"}, want: true},
		{name: "claude", lines: []string{"--provider claude"}, want: false},
		{name: "gemini", lines: []string{"--provider gemini"}, want: false},
		{name: "mixed", lines: []string{"--provider claude", "--provider codex"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerLinesUseCodexHarness(tc.lines); got != tc.want {
				t.Fatalf("providerLinesUseCodexHarness(%v) = %v, want %v", tc.lines, got, tc.want)
			}
		})
	}
}

func TestDirectoryMountLineConversion(t *testing.T) {
	lines := []string{
		"--dir /repo/extra",
		"--dir-ro /repo/docs",
		"--dir ",
		"--provider codex",
	}

	mounts := mountsFromDirLines(lines)
	wantMounts := []PolicyMount{
		{Path: "/repo/extra", Access: "rw"},
		{Path: "/repo/docs", Access: "ro"},
	}
	if !reflect.DeepEqual(mounts, wantMounts) {
		t.Fatalf("mountsFromDirLines = %#v, want %#v", mounts, wantMounts)
	}

	wantLines := []string{"--dir /repo/extra", "--dir-ro /repo/docs"}
	if got := dirLinesFromMounts(mounts); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("dirLinesFromMounts = %#v, want %#v", got, wantLines)
	}
}

func TestDirectoryMountPreselection(t *testing.T) {
	mounts := []PolicyMount{
		{Path: "/repo/extra", Access: "rw"},
		{Path: "/repo/docs", Access: "ro"},
		{Path: "", Access: "ro"},
	}

	got := mountPreselection(mounts)
	want := map[string]bool{
		"/repo/extra": false,
		"/repo/docs":  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mountPreselection = %#v, want %#v", got, want)
	}
}

func TestDirectorySelectionsToMounts(t *testing.T) {
	selections := []dirMountSelection{
		{Path: "/repo/extra"},
		{Path: "/repo/docs", ReadOnly: true},
		{Path: " "},
	}

	got := mountsFromDirSelections(selections)
	want := []PolicyMount{
		{Path: "/repo/extra", Access: "rw"},
		{Path: "/repo/docs", Access: "ro"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mountsFromDirSelections = %#v, want %#v", got, want)
	}
}

func TestLoadWizardExistingConfigReportsPolicySource(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	workspace := "/repo/wizard-policy"

	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Network.Firewall = "dev"
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		t.Fatal(err)
	}

	lines, source, err := loadWizardExistingConfig(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q", source)
	}
	if !reflect.DeepEqual(lines, []string{"--provider codex", "--firewall-dev"}) {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestLoadWizardExistingConfigReportsLegacySource(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	workspace := "/repo/wizard-legacy"
	want := []string{"--provider codex", "--firewall-dev"}
	if err := SaveProjectConfig(workspace, want); err != nil {
		t.Fatal(err)
	}

	lines, source, err := loadWizardExistingConfig(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceLegacy {
		t.Fatalf("source = %q", source)
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestFormatCurrentSetupLineHidesLegacyFlags(t *testing.T) {
	cases := map[string]string{
		"--provider codex":   "Provider: codex",
		"--dir /repo/extra":  "Extra directory: /repo/extra (read/write)",
		"--dir-ro /repo/doc": "Extra directory: /repo/doc (read-only)",
		"--firewall-dev":     "Firewall: dev",
		"--no-firewall":      "Firewall: disabled",
		"--firewall /tmp/fw": "Firewall: custom file /tmp/fw",
		"--no-yolo":          "YOLO mode: disabled",
		"--network-host":     "Network: host",
		"--worktree":         "Parallel isolation: git worktree",
		"network.extra_domain *.apps.example.test":            "Allowed domain: *.apps.example.test",
		"provider.endpoint http://host.docker.internal:11434": "Provider endpoint: http://host.docker.internal:11434",
		"provider.model qwen3-coder:30b":                      "Provider model: qwen3-coder:30b",
		"option.yolo enabled":                                 "YOLO mode: enabled",
		"option.worktree disabled":                            "Parallel isolation: disabled",
		"--go 1.23":                                           "Go: 1.23",
		"--docker host":                                       "Docker: host",
	}
	for input, want := range cases {
		if got := formatCurrentSetupLine(input); got != want {
			t.Fatalf("formatCurrentSetupLine(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDisplayOptionSetupLinesShowsEffectiveDefaults(t *testing.T) {
	got := displayOptionSetupLines(nil)
	want := []string{"option.yolo enabled", "option.worktree disabled"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("option setup lines = %#v, want %#v", got, want)
	}

	got = displayOptionSetupLines([]string{"--no-yolo", "--worktree"})
	want = []string{"option.yolo disabled", "option.worktree enabled"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("option setup lines = %#v, want %#v", got, want)
	}
}

func TestAppendNetworkLinesKeepsNetworkHostOutOfOptions(t *testing.T) {
	got := appendNetworkLines([]string{"--firewall-dev"}, []string{"--no-yolo", "--network-host", "--worktree"})
	want := []string{"--network-host", "--firewall-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("network lines = %#v, want %#v", got, want)
	}
}

func TestNetworkWizardStateFromLines(t *testing.T) {
	state := networkWizardStateFromLines(
		[]string{"--firewall /tmp/firewall.conf"},
		[]string{"--network-host", "--worktree"},
		[]string{"api.example.test"},
	)

	if state.Network.Mode != "host" || !state.Execution.NetworkHost {
		t.Fatalf("host state = %#v", state)
	}
	if state.Network.Firewall != "custom" || state.Network.CustomConfig != "/tmp/firewall.conf" {
		t.Fatalf("firewall state = %#v", state.Network)
	}
	if !reflect.DeepEqual(state.ExtraDomains, []string{"api.example.test"}) {
		t.Fatalf("extra domains = %#v", state.ExtraDomains)
	}
}

func TestNetworkLinesFromState(t *testing.T) {
	cases := []struct {
		name  string
		state NetworkWizardState
		want  []string
	}{
		{
			name:  "strict",
			state: NetworkWizardState{Network: NetworkPolicy{Mode: "bridge", Firewall: "strict"}},
			want:  nil,
		},
		{
			name:  "dev",
			state: NetworkWizardState{Network: NetworkPolicy{Mode: "bridge", Firewall: "dev"}},
			want:  []string{"--firewall-dev"},
		},
		{
			name:  "disabled",
			state: NetworkWizardState{Network: NetworkPolicy{Mode: "bridge", Firewall: "disabled"}},
			want:  []string{"--no-firewall"},
		},
		{
			name:  "custom",
			state: NetworkWizardState{Network: NetworkPolicy{Mode: "bridge", Firewall: "custom", CustomConfig: "/tmp/fw.conf"}},
			want:  []string{"--firewall /tmp/fw.conf"},
		},
		{
			name:  "host",
			state: NetworkWizardState{Network: NetworkPolicy{Mode: "host", Firewall: "disabled"}, Execution: ExecutionPolicy{NetworkHost: true}},
			want:  []string{"--network-host", "--no-firewall"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := networkLinesFromState(tc.state)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("networkLinesFromState = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestExistingNetworkLinesFromStateIncludesExtraDomains(t *testing.T) {
	state := NetworkWizardState{
		Network:      NetworkPolicy{Mode: "bridge", Firewall: "dev"},
		ExtraDomains: []string{"api.example.test"},
	}

	got := existingNetworkLinesFromState(state)
	want := []string{"--firewall-dev", "network.extra_domain api.example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("existingNetworkLinesFromState = %#v, want %#v", got, want)
	}
}

func TestOptionWizardStateConversion(t *testing.T) {
	state := optionWizardStateFromLines([]string{"--no-yolo", "--worktree", "--network-host"})
	if boolValue(state.Execution.Yolo, true) {
		t.Fatalf("expected yolo disabled: %#v", state)
	}
	if !state.Execution.Worktree {
		t.Fatalf("expected worktree enabled: %#v", state)
	}

	got := optionLinesFromState(state)
	want := []string{"--no-yolo", "--worktree"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("optionLinesFromState = %#v, want %#v", got, want)
	}

	display := displayOptionSetupLinesFromState(state)
	wantDisplay := []string{"option.yolo disabled", "option.worktree enabled"}
	if !reflect.DeepEqual(display, wantDisplay) {
		t.Fatalf("displayOptionSetupLinesFromState = %#v, want %#v", display, wantDisplay)
	}
}

func TestAssembleWizardPolicy(t *testing.T) {
	extensions := []*registry.Extension{
		{
			Name: "aws",
			Flags: []registry.ExtensionFlag{
				{Name: "--aws", Arg: "csv"},
				{Name: "--aws-all", Arg: "none"},
			},
		},
		{
			Name: "mcp",
			Flags: []registry.ExtensionFlag{
				{Name: "--mcp", Arg: "csv"},
				{Name: "--mcp-all", Arg: "none"},
			},
		},
	}
	input := WizardAssemblyInput{
		ProviderLines:  []string{"--provider codex"},
		ProviderConfig: ProviderWizardConfig{Endpoint: "http://localhost:11434", Model: "qwen3-coder:30b"},
		DirLines:       []string{"--dir /repo/extra", "--dir-ro /repo/docs"},
		ExtensionLines: []string{"--aws dev,prod"},
		MCPLines:       []string{"--mcp shortcut,github"},
		NetworkLines:   []string{"--network-host", "--no-firewall"},
		OptionLines:    []string{"--no-yolo", "--worktree"},
		ExtraDomains:   []string{"api.example.test"},
	}

	policy, lines, err := assembleWizardPolicy(input, extensions)
	if err != nil {
		t.Fatal(err)
	}

	wantLines := []string{
		"--provider codex",
		"--dir /repo/extra",
		"--dir-ro /repo/docs",
		"--aws dev,prod",
		"--mcp shortcut,github",
		"--network-host",
		"--no-firewall",
		"--no-yolo",
		"--worktree",
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("equivalent lines = %#v, want %#v", lines, wantLines)
	}
	if policy.Provider.Name != "codex" || policy.Provider.Endpoint != "http://localhost:11434" || policy.Provider.Model != "qwen3-coder:30b" {
		t.Fatalf("provider = %#v", policy.Provider)
	}
	wantMounts := []PolicyMount{
		{Path: "/repo/extra", Access: "rw"},
		{Path: "/repo/docs", Access: "ro"},
	}
	if !reflect.DeepEqual(policy.Workspace.Mounts, wantMounts) {
		t.Fatalf("mounts = %#v, want %#v", policy.Workspace.Mounts, wantMounts)
	}
	if len(policy.Capabilities) != 1 || policy.Capabilities[0].Name != "aws" || !reflect.DeepEqual(policy.Capabilities[0].Args, []string{"dev", "prod"}) {
		t.Fatalf("capabilities = %#v", policy.Capabilities)
	}
	// The mcp capability is migrated into the first-class mcp section (direct mode).
	if len(policy.MCP.Servers) != 2 ||
		policy.MCP.Servers[0].Name != "shortcut" || policy.MCP.Servers[0].Mode != "direct" ||
		policy.MCP.Servers[1].Name != "github" || policy.MCP.Servers[1].Mode != "direct" {
		t.Fatalf("mcp servers = %#v", policy.MCP.Servers)
	}
	if policy.Network.Mode != "host" || policy.Network.Firewall != "disabled" {
		t.Fatalf("network = %#v", policy.Network)
	}
	if !reflect.DeepEqual(policy.Network.ExtraDomains, []string{"api.example.test"}) {
		t.Fatalf("extra domains = %#v", policy.Network.ExtraDomains)
	}
	if boolValue(policy.Execution.Yolo, true) || !policy.Execution.Worktree || !policy.Execution.NetworkHost {
		t.Fatalf("execution = %#v", policy.Execution)
	}
}

func TestAssembleWizardPolicy_ClaudeOpenAIBackend(t *testing.T) {
	input := WizardAssemblyInput{
		ProviderLines: []string{"--provider claude"},
		ProviderConfig: ProviderWizardConfig{
			Backend:  "openai",
			Endpoint: "http://host.docker.internal:9223",
			Model:    "claude-sonnet-4-6",
		},
	}

	policy, _, err := assembleWizardPolicy(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Name != "claude" || policy.Provider.Backend != "openai" || policy.Provider.Endpoint != "http://host.docker.internal:9223" || policy.Provider.Model != "claude-sonnet-4-6" {
		t.Fatalf("provider = %#v", policy.Provider)
	}
}

func TestWizardEquivalentLinesOmitsExtraDomains(t *testing.T) {
	input := WizardAssemblyInput{
		ProviderLines: []string{"--provider claude"},
		NetworkLines:  []string{"--firewall-dev"},
		ExtraDomains:  []string{"api.example.test"},
	}

	got := wizardEquivalentLines(input)
	want := []string{"--provider claude", "--firewall-dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wizardEquivalentLines = %#v, want %#v", got, want)
	}
}

func TestLoadWizardExtraDomainsOnlyFromPolicy(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	workspace := "/repo/wizard-extra-domains"

	policy := defaultProjectPolicy()
	policy.Network.ExtraDomains = []string{"*.apps.example.test"}
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		t.Fatal(err)
	}

	got := loadWizardExtraDomains(workspace, nil)
	want := []string{".apps.example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extra domains = %#v, want %#v", got, want)
	}
}

func TestExtensionLineHelpers(t *testing.T) {
	ext := &registry.Extension{
		Name: "aws",
		Flags: []registry.ExtensionFlag{
			{Name: "--aws", Arg: "csv"},
			{Name: "--aws-all", Arg: "none"},
		},
	}
	lines := []string{
		"--provider codex",
		"--aws dev",
		"--aws-all",
		"--dotnet 8",
	}

	got := extensionLinesFor(ext, lines)
	want := []string{"--aws dev", "--aws-all"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extensionLinesFor = %#v, want %#v", got, want)
	}

	got = removeExtensionLines(ext, lines)
	want = []string{"--provider codex", "--dotnet 8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("removeExtensionLines = %#v, want %#v", got, want)
	}

	got = upsertExtensionLines(ext, lines, []string{"--aws prod,shared"})
	want = []string{"--provider codex", "--dotnet 8", "--aws prod,shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("upsertExtensionLines = %#v, want %#v", got, want)
	}
}

func TestSplitMCPLines(t *testing.T) {
	lines := []string{
		"--aws dev",
		"--mcp shortcut,github",
		"--mcp-all",
		"--dotnet 8",
	}

	rest, mcp := splitMCPLines(lines)
	if want := []string{"--aws dev", "--dotnet 8"}; !reflect.DeepEqual(rest, want) {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
	if want := []string{"--mcp shortcut,github", "--mcp-all"}; !reflect.DeepEqual(mcp, want) {
		t.Fatalf("mcp = %#v, want %#v", mcp, want)
	}
}

func TestExistingDotnetVersions(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{name: "none", lines: nil, want: nil},
		{name: "lts", lines: []string{"--dotnet"}, want: []string{"lts"}},
		{name: "specific", lines: []string{"--dotnet 8,10"}, want: []string{"8", "10"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := existingDotnetVersions(tc.lines)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("existingDotnetVersions = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestExistingCloudConfig(t *testing.T) {
	action, selected := existingCloudConfig([]string{"--aws-all"}, "--aws", "--aws-all")
	if action != "all" || selected != nil {
		t.Fatalf("all config = (%q, %#v), want (all, nil)", action, selected)
	}

	action, selected = existingCloudConfig([]string{"--aws dev,prod"}, "--aws", "--aws-all")
	if action != "select" || !reflect.DeepEqual(selected, []string{"dev", "prod"}) {
		t.Fatalf("selected config = (%q, %#v), want (select, [dev prod])", action, selected)
	}

	action, selected = existingCloudConfig(nil, "--aws", "--aws-all")
	if action != "select" || selected != nil {
		t.Fatalf("empty config = (%q, %#v), want (select, nil)", action, selected)
	}
}

// ---------------------------------------------------------------------------
// wizardSeedFromPolicy — pre-seed state from a base policy
// ---------------------------------------------------------------------------

func TestWizardSeedFromPolicy(t *testing.T) {
	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Workspace.Mounts = []PolicyMount{{Path: "/shared", Access: "ro"}}
	policy.Network.Firewall = "dev"
	policy.Network.ExtraDomains = []string{"api.example.com"}
	policy.Capabilities = []CapabilityPolicy{{Name: "aws"}}
	policy.MCP.Servers = []MCPServerPolicy{{Name: "shortcut", Mode: "mount"}}
	policy.Options = map[string]string{"image_paste_key": "ctrl+v"}

	seed := wizardSeedFromPolicy(policy)

	if seed.providerState.Default != "codex" {
		t.Fatalf("provider default = %q, want codex", seed.providerState.Default)
	}
	if len(seed.dirs) != 1 || seed.dirs[0] != "--dir-ro /shared" {
		t.Fatalf("dirs = %v", seed.dirs)
	}
	if !hasLine(seed.firewall, "--firewall-dev") {
		t.Fatalf("firewall = %v, want --firewall-dev", seed.firewall)
	}
	if len(seed.extraDomains) != 1 || seed.extraDomains[0] != "api.example.com" {
		t.Fatalf("extraDomains = %v", seed.extraDomains)
	}
	if len(seed.mcpServers) != 1 || seed.mcpServers[0].Mode != "mount" {
		t.Fatalf("mcpServers = %v", seed.mcpServers)
	}
	// The image-paste-key option must not leak into the extension bucket.
	for _, line := range seed.exts {
		if configLineFlag(line) == "--image-paste-key" {
			t.Fatalf("image-paste-key leaked into exts: %v", seed.exts)
		}
	}
	if !hasLine(seed.exts, "--aws") {
		t.Fatalf("exts = %v, want --aws", seed.exts)
	}
}

func TestWizardSeedFromPolicyPreservesDockerMode(t *testing.T) {
	for _, mode := range []string{"dind", "host"} {
		t.Run(mode, func(t *testing.T) {
			policy := defaultProjectPolicy()
			policy.Execution.Docker = mode

			seed := wizardSeedFromPolicy(policy)
			want := "--docker " + mode
			if !hasLine(seed.exts, want) {
				t.Fatalf("extension seed = %v, want %q", seed.exts, want)
			}
		})
	}
}

func TestWizardSeedFromPolicy_Nil(t *testing.T) {
	seed := wizardSeedFromPolicy(nil)
	if seed.providerState.Default != "" || len(seed.dirs) != 0 || len(seed.exts) != 0 {
		t.Fatalf("nil policy should yield empty seed, got %+v", seed)
	}
}

func TestFilterNonExtensionLines(t *testing.T) {
	in := []string{"--aws", "--image-paste-key ctrl+v", "--dotnet 8", "--name foo"}
	got := filterNonExtensionLines(in)
	want := []string{"--aws", "--dotnet 8"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
