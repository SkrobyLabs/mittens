package main

import (
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

func TestLaunchSummaryRender_Default(t *testing.T) {
	s := LaunchSummary{
		Provider:         "Codex",
		Workspace:        SummaryMount{Path: "/repo/app", Access: "rw"},
		Credentials:      []string{"Codex OAuth"},
		Network:          "docker bridge, firewall allowlist (+2 dynamic domains)",
		Extensions:       []string{"firewall", "go"},
		HostIntegrations: []string{"OAuth callbacks", "URL opening", "notifications"},
		Execution:        []string{"yolo", "no Docker access"},
		History:          "enabled",
	}

	out := s.Render()
	for _, want := range []string{
		"Boundary",
		"Provider: Codex",
		"Workspace: /repo/app rw",
		"Extra dirs: none",
		"Credentials: Codex OAuth",
		"Network: docker bridge, firewall allowlist (+2 dynamic domains)",
		"Extensions: firewall, go",
		"Host: OAuth callbacks, URL opening, notifications",
		"Execution: yolo, no Docker access",
		"History: enabled",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestLaunchSummaryRender_SortsExtraDirs(t *testing.T) {
	s := LaunchSummary{
		Provider:         "Claude",
		Workspace:        SummaryMount{Path: "/repo/app", Access: "rw"},
		ExtraDirs:        []SummaryMount{{Path: "/z", Access: "rw"}, {Path: "/a", Access: "ro"}},
		Credentials:      []string{"none staged"},
		Network:          "docker bridge, firewall disabled",
		Extensions:       []string{"none"},
		HostIntegrations: []string{"none"},
		Execution:        []string{"permission prompts", "no Docker access"},
		History:          "disabled",
	}

	out := s.Render()
	if !strings.Contains(out, "Extra dirs: /a ro, /z rw") {
		t.Fatalf("extra dirs not sorted/rendered:\n%s", out)
	}
}

func TestBuildLaunchSummary_RuntimeState(t *testing.T) {
	app := &App{
		Provider:          CodexProvider(),
		Profile:           "planner",
		WorkspaceMountSrc: "/repo/app",
		NetworkHost:       true,
		Yolo:              true,
		HostProjectDir:    "repo-app",
		Extensions: []*registry.Extension{
			{Name: "firewall", Enabled: true},
			{Name: "go", Enabled: true},
			{Name: "aws", Enabled: false},
		},
		launchSummary: LaunchSummary{
			ExtraDirs: []SummaryMount{{Path: "/repo/shared", Access: "ro"}},
		},
	}
	cfg := &initcfg.ContainerConfig{}
	cfg.Flags.Firewall = false
	cfg.Flags.DinD = true

	s := app.buildLaunchSummary(cfg, []string{"api.openai.com"})

	if s.Provider != "Codex" {
		t.Fatalf("provider = %q", s.Provider)
	}
	if s.Profile != "planner" {
		t.Fatalf("profile = %q", s.Profile)
	}
	if s.Workspace.Path != "/repo/app" || s.Workspace.Access != "rw" {
		t.Fatalf("workspace = %+v", s.Workspace)
	}
	if got := renderMounts(s.ExtraDirs); got != "/repo/shared ro" {
		t.Fatalf("extra dirs = %q", got)
	}
	if s.Network != "host network, firewall disabled" {
		t.Fatalf("network = %q", s.Network)
	}
	if got := strings.Join(s.Extensions, ", "); got != "firewall, go" {
		t.Fatalf("extensions = %q", got)
	}
	if got := strings.Join(s.Execution, ", "); !strings.Contains(got, "Docker-in-Docker") {
		t.Fatalf("execution missing dind: %q", got)
	}
}

func TestBuildLaunchSummary_FirewallDomainCountDeduped(t *testing.T) {
	app := &App{
		Provider:          ClaudeProvider(),
		WorkspaceMountSrc: "/repo/app",
		Yolo:              false,
	}
	cfg := &initcfg.ContainerConfig{}
	cfg.Flags.Firewall = true

	s := app.buildLaunchSummary(cfg, []string{"api.github.com", "api.github.com", "registry.npmjs.org"})
	if s.Network != "docker bridge, firewall allowlist (+2 dynamic domains)" {
		t.Fatalf("network = %q", s.Network)
	}
}
