package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWizardOverviewRowsSummarizesLargePolicy(t *testing.T) {
	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Provider.Profile = "review"
	policy.Workspace.Mounts = []PolicyMount{
		{Path: "/src/shared-a", Access: "ro"},
		{Path: "/src/shared-b", Access: "rw"},
		{Path: "/src/shared-c", Access: "ro"},
	}
	policy.Capabilities = []CapabilityPolicy{{Name: "aws"}, {Name: "go"}, {Name: "kubectl"}}
	policy.MCP.All = true
	policy.MCP.Servers = []MCPServerPolicy{{Name: "github", Mode: mcpModeDirect}, {Name: "linear", Mode: mcpModeProxy}}

	got := wizardOverviewRows(policy, "/repo/workspace")
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"Provider: Codex (model preset review)",
		"Directories: workspace + 3 extra",
		"Extensions: 3 enabled",
		"MCP: all configured + 2 explicit",
		"Network: docker bridge, firewall allowlist",
		"Execution: automatic approvals; no Docker access",
		"Credentials: 1 staged",
		"Host integrations: 4 enabled",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("overview missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "/src/shared-a") || strings.Contains(joined, "github (direct)") {
		t.Fatalf("overview should use counts instead of item values:\n%s", joined)
	}
}

func TestRenderWizardDetailsListsEveryItemSeparately(t *testing.T) {
	policy := defaultProjectPolicy()
	policy.Provider.Name = "codex"
	policy.Workspace.Mounts = []PolicyMount{
		{Path: "/Users/example/Documents/Source/very-long-shared-project-a", Access: "ro"},
		{Path: "/Users/example/Documents/Source/very-long-shared-project-b", Access: "rw"},
	}
	policy.Capabilities = []CapabilityPolicy{{Name: "aws"}, {Name: "go"}, {Name: "kubectl"}, {Name: "trivy"}}
	policy.MCP.Servers = []MCPServerPolicy{
		{Name: "github-enterprise-production", Mode: mcpModeDirect},
		{Name: "linear-production", Mode: mcpModeProxy},
		{Name: "sentry-production", Mode: mcpModeMount},
	}

	details := renderWizardDetailsWithWidth(policy, "/Users/example/Documents/Source/very-long-workspace", 0)
	for _, want := range []string{
		"  Workspace: /Users/example/Documents/Source/very-long-workspace (rw)",
		"  Extra: /Users/example/Documents/Source/very-long-shared-project-a (ro)",
		"  Extra: /Users/example/Documents/Source/very-long-shared-project-b (rw)",
		"  Server: github-enterprise-production (direct)",
		"  Server: linear-production (proxy)",
		"  Server: sentry-production (mount)",
		"  Extension: aws",
		"  Extension: go",
		"  Extension: kubectl",
		"  Extension: trivy",
	} {
		if !strings.Contains(details, want+"\n") {
			t.Fatalf("details missing standalone item %q:\n%s", want, details)
		}
	}
	if strings.Contains(details, "aws, go") || strings.Contains(details, "github-enterprise-production (direct),") {
		t.Fatalf("details joined separate items into a comma blob:\n%s", details)
	}
}

func TestRenderWizardDetailsWrapsLongValuesWithoutTruncating(t *testing.T) {
	policy := defaultProjectPolicy()
	longPath := "/Users/example/Documents/Source/a-project-with-a-very-long-name-and-unbroken-value"
	policy.Workspace.Mounts = []PolicyMount{{Path: longPath, Access: "ro"}}

	details := renderWizardDetailsWithWidth(policy, longPath, 30)
	for _, line := range strings.Split(strings.TrimSuffix(details, "\n"), "\n") {
		if lipgloss.Width(line) > 30 {
			t.Fatalf("wrapped line exceeds width: %d %q", lipgloss.Width(line), line)
		}
	}
	if !strings.Contains(strings.ReplaceAll(details, "\n  ", ""), longPath) {
		t.Fatalf("wrapped details lost part of the long value:\n%s", details)
	}
}

func TestWizardDetailsIncludesSelectionsAndNondefaultSettings(t *testing.T) {
	p := defaultProjectPolicy()
	p.Provider = ProviderPolicy{Name: "codex", Model: "custom-model", Endpoint: "http://localhost:11434", Effort: "high"}
	p.Options["image_paste_key"] = "ctrl+v"
	p.ExtraArgs = []string{"--custom-argument"}
	p.Host.OpenURLs = "deny"
	p.Host.ClipboardImages = boolPtr(false)
	p.Network.SSHEgress = boolPtr(false)
	p.Network.ExtraDomains = []string{"build.example.test"}
	p.Credentials.Cloud["aws"] = CredentialSelector{Profiles: []string{"production"}}
	p.Capabilities = []CapabilityPolicy{{Name: "kubectl", Args: []string{"saved-context"}}}
	p.MCP.Servers = []MCPServerPolicy{{Name: "approved-server", Mode: mcpModeProxy, CommandPin: "saved-pin"}}
	out := renderWizardDetailsWithWidth(p, "/repo", 0)
	for _, value := range []string{"Model: custom-model", "Endpoint: http://localhost:11434", "Effort: high", "image_paste_key = ctrl+v", "Provider argument: --custom-argument", "Clipboard images: disabled", "Open host browser: deny", "SSH egress: blocked", "Allowed domain: build.example.test", "aws: production", "kubectl: saved-context", "approved-server (saved-pin)"} {
		if !strings.Contains(out, value) {
			t.Fatalf("details missing %q:\n%s", value, out)
		}
	}
}
