package main

import (
	"reflect"
	"testing"
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
		"--provider gemini",
	})

	if !selected["codex"] || !selected["gemini"] {
		t.Fatalf("expected selected codex and gemini, got %v", selected)
	}
	if def != "gemini" {
		t.Fatalf("expected default provider gemini, got %q", def)
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
		"network.extra_domain *.apps.example.test": "Allowed domain: *.apps.example.test",
		"option.yolo enabled":                      "YOLO mode: enabled",
		"option.worktree disabled":                 "Parallel isolation: disabled",
		"--go 1.23":                                "Go: 1.23",
		"--docker host":                            "Docker: host",
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
