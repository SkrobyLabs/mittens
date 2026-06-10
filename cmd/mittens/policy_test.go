package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestSaveLoadProjectPolicy(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/repo/app"
	yolo := false
	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Provider.Profile = "planner"
	policy.Workspace.Mounts = []PolicyMount{{Path: "../shared", Access: "ro"}}
	policy.Network.Mode = "host"
	policy.Network.Firewall = "dev"
	policy.Execution.Yolo = &yolo
	policy.Capabilities = []CapabilityPolicy{{Name: "dotnet", Args: []string{"9"}, RawFlag: "--dotnet"}}

	if err := SaveProjectPolicy(workspace, policy); err != nil {
		t.Fatal(err)
	}

	loaded, source, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q", source)
	}
	if loaded.Provider.Name != "codex" || loaded.Provider.Profile != "planner" {
		t.Fatalf("provider = %+v", loaded.Provider)
	}
	if got := loaded.ToLegacyFlags(); !reflect.DeepEqual(got, []string{"--provider", "codex", "--profile", "planner", "--dir-ro", "../shared", "--firewall-dev", "--network-host", "--no-yolo", "--dotnet", "9"}) {
		t.Fatalf("legacy flags = %#v", got)
	}
}

func TestLoadProjectConfig_PrefersProjectPolicy(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/repo/policy-first"
	if err := SaveProjectConfig(workspace, []string{"--provider claude", "--no-firewall"}); err != nil {
		t.Fatal(err)
	}
	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Network.Firewall = "dev"
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		t.Fatal(err)
	}

	flags, err := LoadProjectConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--provider", "codex", "--firewall-dev"}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("flags = %#v, want %#v", flags, want)
	}

	raw, err := loadProjectConfigRaw(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, []string{"--provider codex", "--firewall-dev"}) {
		t.Fatalf("raw = %#v", raw)
	}
}

func TestLoadProjectPolicy_FallsBackToLegacyConfig(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/repo/legacy"
	if err := SaveProjectConfig(workspace, []string{
		"--provider codex",
		"--profile planner",
		"--dir ../shared",
		"--dir-ro ../readonly",
		"--no-firewall",
		"--network-host",
		"--docker dind",
		"--aws prod,stage",
	}); err != nil {
		t.Fatal(err)
	}

	extensions := []*registry.Extension{
		{Name: "aws", Flags: []registry.ExtensionFlag{{Name: "--aws", Arg: "csv"}}},
	}
	policy, source, err := LoadProjectPolicy(workspace, extensions)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceLegacy {
		t.Fatalf("source = %q", source)
	}
	if policy.Provider.Name != "codex" || policy.Provider.Profile != "planner" {
		t.Fatalf("provider = %+v", policy.Provider)
	}
	if policy.Network.Mode != "host" || policy.Network.Firewall != "disabled" {
		t.Fatalf("network = %+v", policy.Network)
	}
	if policy.Execution.Docker != "dind" {
		t.Fatalf("docker mode = %q", policy.Execution.Docker)
	}
	if len(policy.Workspace.Mounts) != 2 {
		t.Fatalf("mounts = %+v", policy.Workspace.Mounts)
	}
	if got := policy.Capabilities; len(got) != 2 || got[0].Name != "docker" || got[1].Name != "aws" {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestProjectPolicyHostFalseValuesRoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/repo/host-policy"
	policy := defaultProjectPolicy()
	policy.Host.OpenURLs = "deny"
	policy.Host.ClipboardImages = boolPtr(false)
	policy.Host.Notifications = boolPtr(false)
	policy.Host.PathTranslation = boolPtr(false)

	if err := SaveProjectPolicy(workspace, policy); err != nil {
		t.Fatal(err)
	}

	loaded, source, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q", source)
	}
	if loaded.Host.OpenURLs != "deny" {
		t.Fatalf("open_urls = %q", loaded.Host.OpenURLs)
	}
	if boolValue(loaded.Host.ClipboardImages, true) {
		t.Fatal("clipboard_images = true, want false")
	}
	if boolValue(loaded.Host.Notifications, true) {
		t.Fatal("notifications = true, want false")
	}
	if boolValue(loaded.Host.PathTranslation, true) {
		t.Fatal("path_translation = true, want false")
	}
	if got := loaded.ToLegacyFlags(); !reflect.DeepEqual(got, []string{"--no-notify"}) {
		t.Fatalf("legacy flags = %#v, want --no-notify only", got)
	}
}

func TestLoadProjectPolicy_None(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	policy, source, err := LoadProjectPolicy("/repo/none", nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy != nil {
		t.Fatalf("policy = %+v", policy)
	}
	if source != PolicySourceNone {
		t.Fatalf("source = %q", source)
	}
}

func TestApplyProjectPolicyConfiguresHostRuntime(t *testing.T) {
	policy := defaultProjectPolicy()
	policy.Host.OpenURLs = "deny"
	policy.Host.ClipboardImages = boolPtr(false)
	policy.Host.Notifications = boolPtr(false)
	policy.Host.PathTranslation = boolPtr(false)

	app := &App{}
	app.applyProjectPolicy(policy)

	if app.HostBridge.OpenURLs {
		t.Fatal("HostBridge.OpenURLs = true, want false")
	}
	if app.HostBridge.ClipboardImages {
		t.Fatal("HostBridge.ClipboardImages = true, want false")
	}
	if app.HostBridge.Notifications {
		t.Fatal("HostBridge.Notifications = true, want false")
	}
	if app.PathTranslate {
		t.Fatal("PathTranslate = true, want false")
	}
}

func TestApplyProjectPolicyConfiguresRuntimeAndCapabilities(t *testing.T) {
	yolo := false
	history := false
	notify := false
	policy := defaultProjectPolicy()
	policy.Provider.Profile = "fast"
	policy.Workspace.Mode = "worktree"
	policy.Workspace.Mounts = []PolicyMount{{Path: "../shared", Access: "ro"}}
	policy.Network.Mode = "host"
	policy.Network.Firewall = "custom"
	policy.Network.CustomConfig = "/tmp/firewall.conf"
	policy.Network.ExtraDomains = []string{".apps.example.test"}
	policy.Execution.Yolo = &yolo
	policy.Execution.History = &history
	policy.Execution.Notify = &notify
	policy.Execution.Shell = true
	policy.Execution.Docker = "dind"
	policy.Options["image_paste_key"] = "ctrl+v"
	policy.Options["name"] = "worker"
	policy.Capabilities = []CapabilityPolicy{{Name: "aws", Args: []string{"prod", "stage"}}}
	policy.ExtraArgs = []string{"--model", "opus"}

	app := &App{
		Yolo: true,
		Extensions: []*registry.Extension{
			{Name: "firewall", DefaultOn: true, Enabled: true},
			{Name: "docker"},
			{Name: "aws"},
		},
	}
	app.applyProjectPolicy(policy)

	if app.Profile != "fast" || !app.Worktree || !app.NetworkHost || !app.Shell {
		t.Fatalf("runtime fields not applied: %+v", app)
	}
	if app.Yolo || !app.NoHistory || !app.NoNotify {
		t.Fatalf("execution booleans not applied: yolo=%v noHistory=%v noNotify=%v", app.Yolo, app.NoHistory, app.NoNotify)
	}
	if app.ImagePasteKey != "ctrl+v" || app.InstanceName != "worker" {
		t.Fatalf("runtime values not applied: paste=%q name=%q", app.ImagePasteKey, app.InstanceName)
	}
	if !reflect.DeepEqual(app.ExtraDirs, []string{"ro:../shared"}) {
		t.Fatalf("extra dirs = %#v", app.ExtraDirs)
	}
	if !reflect.DeepEqual(app.FirewallExtra, []string{".apps.example.test"}) {
		t.Fatalf("firewall extra = %#v", app.FirewallExtra)
	}
	if !reflect.DeepEqual(app.ClaudeArgs, []string{"--model", "opus"}) {
		t.Fatalf("claude args = %#v", app.ClaudeArgs)
	}

	exts := map[string]*registry.Extension{}
	for _, ext := range app.Extensions {
		exts[ext.Name] = ext
	}
	if got := exts["firewall"]; !got.Enabled || got.RawArg != "/tmp/firewall.conf" {
		t.Fatalf("firewall extension = %+v", got)
	}
	if got := exts["docker"]; !got.Enabled || got.RawArg != "dind" || !reflect.DeepEqual(got.Args, []string{"dind"}) {
		t.Fatalf("docker extension = %+v", got)
	}
	if got := exts["aws"]; !got.Enabled || got.RawArg != "prod,stage" || !reflect.DeepEqual(got.Args, []string{"prod", "stage"}) {
		t.Fatalf("aws extension = %+v", got)
	}
}

func TestRunPolicySetUpdatesHostField(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	workspace := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	if err := runPolicySet([]string{"host.open_urls", "deny"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := runPolicySet([]string{"host.clipboard_images", "false"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, source, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q", source)
	}
	if policy.Host.OpenURLs != "deny" {
		t.Fatalf("open_urls = %q", policy.Host.OpenURLs)
	}
	if boolValue(policy.Host.ClipboardImages, true) {
		t.Fatal("clipboard_images = true, want false")
	}
}

func TestRunPolicySetUpdatesNetworkExtraDomains(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	workspace := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	if err := runPolicySet([]string{"network.extra_domains", "*.apps.example.test,api.example.com"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, source, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q", source)
	}
	want := []string{".apps.example.test", "api.example.com"}
	if !reflect.DeepEqual(policy.Network.ExtraDomains, want) {
		t.Fatalf("extra domains = %#v, want %#v", policy.Network.ExtraDomains, want)
	}
}

func TestSetPolicyFieldSSHEgress(t *testing.T) {
	policy := defaultProjectPolicy()
	if policy.Network.SSHEgress != nil {
		t.Fatalf("ssh_egress should be unset by default, got %v", *policy.Network.SSHEgress)
	}
	if err := setPolicyField(policy, "network.ssh_egress", "false"); err != nil {
		t.Fatal(err)
	}
	if policy.Network.SSHEgress == nil || *policy.Network.SSHEgress {
		t.Fatalf("ssh_egress = %v, want false", policy.Network.SSHEgress)
	}
	if err := setPolicyField(policy, "network.ssh_egress", "maybe"); err == nil {
		t.Fatal("expected invalid ssh_egress value to fail")
	}
}

func TestSetPolicyFieldRejectsInvalidValues(t *testing.T) {
	policy := defaultProjectPolicy()
	if err := setPolicyField(policy, "host.open_urls", "maybe"); err == nil {
		t.Fatal("expected invalid open_urls value to fail validation")
	}
	if err := setPolicyField(policy, "host.clipboard_images", "maybe"); err == nil {
		t.Fatal("expected invalid boolean to fail")
	}
	if err := setPolicyField(policy, "unknown.field", "x"); err == nil {
		t.Fatal("expected unknown field to fail")
	}
}

func TestProjectPolicyValidateFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProjectPolicy)
	}{
		{
			name: "network extra domain URL",
			mutate: func(p *ProjectPolicy) {
				p.Network.ExtraDomains = []string{"https://example.com"}
			},
		},
		{
			name: "bad version",
			mutate: func(p *ProjectPolicy) {
				p.Version = 99
			},
		},
		{
			name: "bad mount access",
			mutate: func(p *ProjectPolicy) {
				p.Workspace.Mounts = []PolicyMount{{Path: "x", Access: "write"}}
			},
		},
		{
			name: "duplicate mount",
			mutate: func(p *ProjectPolicy) {
				p.Workspace.Mounts = []PolicyMount{{Path: "x", Access: "rw"}, {Path: "./x", Access: "ro"}}
			},
		},
		{
			name: "custom firewall missing path",
			mutate: func(p *ProjectPolicy) {
				p.Network.Firewall = "custom"
			},
		},
		{
			name: "bad docker mode",
			mutate: func(p *ProjectPolicy) {
				p.Execution.Docker = "remote"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := defaultProjectPolicy()
			tc.mutate(policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPolicyFromLegacyFlags_ProviderArgsAfterSeparator(t *testing.T) {
	policy, err := PolicyFromLegacyFlags([]string{"--provider", "gemini", "--", "--model", "flash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Name != "gemini" {
		t.Fatalf("provider = %q", policy.Provider.Name)
	}
	if !reflect.DeepEqual(policy.ExtraArgs, []string{"--model", "flash"}) {
		t.Fatalf("extra args = %#v", policy.ExtraArgs)
	}
}

func TestPolicyFromLegacyFlags_IgnoresOldRoleFlags(t *testing.T) {
	policy, err := PolicyFromLegacyFlags([]string{"--worker", "--planner", "--provider", "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Name != "codex" {
		t.Fatalf("provider = %q", policy.Provider.Name)
	}
	if len(policy.ExtraArgs) != 0 {
		t.Fatalf("extra args = %#v", policy.ExtraArgs)
	}
}

func TestPolicyFromLegacyFlags_IgnoresRuntimeResume(t *testing.T) {
	policy, err := PolicyFromLegacyFlags([]string{"--resume", "abc123", "--provider", "codex"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Name != "codex" {
		t.Fatalf("provider = %q", policy.Provider.Name)
	}
	for _, flag := range policy.ToLegacyFlags() {
		if flag == "--resume" || flag == "abc123" {
			t.Fatalf("legacy flags retained runtime resume: %#v", policy.ToLegacyFlags())
		}
	}
}

func TestPolicyFromLegacyFlags_EnumExtension(t *testing.T) {
	extensions := []*registry.Extension{
		{
			Name: "dotnet",
			Flags: []registry.ExtensionFlag{
				{Name: "--dotnet", Arg: "enum", EnumValues: []string{"8", "9"}},
			},
		},
	}
	policy, err := PolicyFromLegacyFlags([]string{"--dotnet", "9"}, extensions)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Capabilities) != 1 {
		t.Fatalf("capabilities = %+v", policy.Capabilities)
	}
	if got := policy.Capabilities[0]; got.Name != "dotnet" || !reflect.DeepEqual(got.Args, []string{"9"}) {
		t.Fatalf("capability = %+v", got)
	}
}

func TestRunPolicyShowRejectsOverrides(t *testing.T) {
	err := runPolicyShow([]string{"--provider", "codex"}, nil)
	if err == nil {
		t.Fatal("expected policy show override error")
	}
	if !strings.Contains(err.Error(), "policy show no longer accepts") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEffectivePolicyForShowPrefersProjectPolicy(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	workspace := "/repo/show-policy"
	if err := SaveUserDefaults([]string{"--provider codex", "--no-firewall"}); err != nil {
		t.Fatal(err)
	}
	policy := defaultProjectPolicy()
	policy.Provider.Name = "gemini"
	policy.Network.Firewall = "dev"
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		t.Fatal(err)
	}

	got, source, err := effectivePolicyForShow(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q", source)
	}
	if got.Provider.Name != "gemini" || got.Network.Firewall != "dev" {
		t.Fatalf("policy = %+v", got)
	}
}

func TestEffectivePolicyForShowUsesUserDefaultsWithoutProjectPolicy(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	if err := SaveUserDefaults([]string{"--provider codex", "--no-firewall"}); err != nil {
		t.Fatal(err)
	}

	got, source, err := effectivePolicyForShow("/repo/no-project-policy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceNone {
		t.Fatalf("source = %q", source)
	}
	if got.Provider.Name != "codex" || got.Network.Firewall != "disabled" {
		t.Fatalf("policy = %+v", got)
	}
}

func TestLaunchSummaryFromPolicy(t *testing.T) {
	yolo := false
	history := false
	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Provider.Profile = "fast"
	policy.Workspace.Mode = "worktree"
	policy.Workspace.Mounts = []PolicyMount{{Path: "../shared", Access: "ro"}}
	policy.Network.Mode = "host"
	policy.Network.Firewall = "disabled"
	policy.Execution.Yolo = &yolo
	policy.Execution.History = &history
	policy.Execution.Docker = "host"
	policy.Capabilities = []CapabilityPolicy{{Name: "aws"}}

	summary := launchSummaryFromPolicy(policy, "/repo/app")
	if summary.Provider != "Codex" || summary.Profile != "fast" {
		t.Fatalf("provider/profile = %q/%q", summary.Provider, summary.Profile)
	}
	if summary.Workspace.Access != "rw worktree" {
		t.Fatalf("workspace access = %q", summary.Workspace.Access)
	}
	if got := renderMounts(summary.ExtraDirs); got != "../shared ro" {
		t.Fatalf("extra dirs = %q", got)
	}
	if summary.Network != "host network, firewall disabled" {
		t.Fatalf("network = %q", summary.Network)
	}
	if got := strings.Join(summary.Execution, ", "); !strings.Contains(got, "permission prompts") || !strings.Contains(got, "host Docker socket") {
		t.Fatalf("execution = %q", got)
	}
	if summary.History != "disabled" {
		t.Fatalf("history = %q", summary.History)
	}
}

func TestProjectPolicyPath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	got := projectPolicyPath("/repo/app")
	want := filepath.Join(tmpHome, "projects", ProjectDir("/repo/app"), "policy.yaml")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("policy path should not exist yet: %v", err)
	}
}
