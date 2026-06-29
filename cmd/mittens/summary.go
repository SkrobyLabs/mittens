package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

// SummaryMount describes one user-visible filesystem mount in the launch summary.
type SummaryMount struct {
	Path   string
	Access string
}

// LaunchSummary is the human-facing boundary that will be applied to a run.
type LaunchSummary struct {
	Provider         string
	Profile          string
	Workspace        SummaryMount
	ExtraDirs        []SummaryMount
	Credentials      []string
	Network          string
	Extensions       []string
	HostIntegrations []string
	Execution        []string
	History          string
}

func (a *App) buildLaunchSummary(cfg *initcfg.ContainerConfig, firewallDomains []string) LaunchSummary {
	extraDirs := a.launchSummary.ExtraDirs
	s := LaunchSummary{
		Provider:  a.Provider.DisplayName,
		Profile:   a.Profile,
		Workspace: SummaryMount{Path: a.WorkspaceMountSrc, Access: "rw"},
		ExtraDirs: extraDirs,
		History:   a.historySummary(),
	}

	s.Credentials = a.credentialSummary()
	s.Network = a.networkSummary(cfg, firewallDomains)
	s.Extensions = a.enabledExtensionNames()
	s.HostIntegrations = a.hostIntegrationSummary(cfg)
	s.Execution = a.executionSummary(cfg)

	return s
}

func (a *App) credentialSummary() []string {
	var out []string
	if a.Provider.APIKeyEnv != "" && envOrDefault(a.Provider.APIKeyEnv, "") != "" {
		out = append(out, a.Provider.DisplayName+" API key env")
	}
	if a.Credentials != nil && a.Credentials.TmpFile() != "" {
		out = append(out, a.Provider.DisplayName+" OAuth")
	}
	for _, entry := range a.credStagingDirs {
		_, target, ok := strings.Cut(entry, ":")
		if !ok || target == "" {
			out = append(out, "extension-staged credentials")
			continue
		}
		out = append(out, strings.TrimPrefix(target, ".")+" credentials")
	}
	if len(out) == 0 {
		return []string{"none staged"}
	}
	return uniqueSorted(out)
}

func (a *App) networkSummary(cfg *initcfg.ContainerConfig, firewallDomains []string) string {
	mode := "docker bridge"
	if a.NetworkHost {
		mode = "host network"
	}
	if cfg.Flags.Firewall {
		ssh := "SSH egress allowed"
		if cfg.Flags.NoSSHEgress {
			ssh = "SSH egress blocked"
		}
		if cfg.Flags.FirewallLearn {
			return fmt.Sprintf("%s, firewall LEARN mode (allowlist not enforced; observing domains), %s", mode, ssh)
		}
		count := len(uniqueSorted(firewallDomains))
		if count > 0 {
			return fmt.Sprintf("%s, firewall allowlist (+%d dynamic domains), %s", mode, count, ssh)
		}
		return fmt.Sprintf("%s, firewall allowlist, %s", mode, ssh)
	}
	return mode + ", firewall disabled"
}

func (a *App) enabledExtensionNames() []string {
	var names []string
	for _, ext := range a.Extensions {
		if ext.Enabled {
			names = append(names, ext.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"none"}
	}
	return names
}

func (a *App) hostIntegrationSummary(cfg *initcfg.ContainerConfig) []string {
	var out []string
	if a.broker != nil && a.broker.Host.OpenURLs {
		out = append(out, "OAuth callbacks", "URL opening")
	}
	if a.broker != nil && a.broker.Host.Notifications {
		out = append(out, "notifications")
	}
	if a.broker != nil && a.broker.Host.ClipboardImages && a.broker.OnClipboardRead != nil {
		out = append(out, "clipboard images")
	}
	if cfg.Flags.EnableX11Clipboard {
		out = append(out, "X11 clipboard bridge")
	}
	if cfg.Flags.WSLClipboard {
		out = append(out, "WSL clipboard")
	}
	if a.dropDir != "" {
		out = append(out, "drag/drop path translation")
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return uniqueSorted(out)
}

func (a *App) executionSummary(cfg *initcfg.ContainerConfig) []string {
	var out []string
	if a.Yolo {
		out = append(out, "yolo")
	} else {
		out = append(out, "permission prompts")
	}
	if a.Shell {
		out = append(out, "shell")
	}
	if a.Headless {
		out = append(out, "headless")
	}
	if a.Worktree {
		out = append(out, "worktree")
	}
	if cfg.Flags.DinD {
		out = append(out, "Docker-in-Docker")
	}
	if cfg.Flags.DockerHost {
		out = append(out, "host Docker socket")
	}
	if !cfg.Flags.DinD && !cfg.Flags.DockerHost {
		out = append(out, "no Docker access")
	}
	return out
}

func (a *App) historySummary() string {
	if a.NoHistory {
		return "disabled"
	}
	providerPlan := a.Provider.RuntimePlan()
	if providerPlan.HistoryMountsWholeConfig {
		return "enabled (provider config)"
	}
	if providerPlan.HistoryMountsProjectDirs && a.HostProjectDir != "" {
		return "enabled (project " + a.HostProjectDir + ")"
	}
	return "enabled"
}

// Render formats a compact user-facing boundary summary.
func (s LaunchSummary) Render() string {
	var b strings.Builder
	b.WriteString(colorCyan + "[mittens]" + colorReset + " Boundary\n")
	b.WriteString(fmt.Sprintf("  Provider: %s", valueOr(s.Provider, "unknown")))
	if s.Profile != "" {
		b.WriteString(" (profile " + s.Profile + ")")
	}
	b.WriteByte('\n')
	if s.Workspace.Path != "" {
		b.WriteString(fmt.Sprintf("  Workspace: %s %s\n", s.Workspace.Path, valueOr(s.Workspace.Access, "rw")))
	}
	b.WriteString("  Extra dirs: " + renderMounts(s.ExtraDirs) + "\n")
	b.WriteString("  Credentials: " + strings.Join(s.Credentials, ", ") + "\n")
	b.WriteString("  Network: " + valueOr(s.Network, "unknown") + "\n")
	b.WriteString("  Extensions: " + strings.Join(s.Extensions, ", ") + "\n")
	b.WriteString("  Host: " + strings.Join(s.HostIntegrations, ", ") + "\n")
	b.WriteString("  Execution: " + strings.Join(s.Execution, ", ") + "\n")
	b.WriteString("  History: " + valueOr(s.History, "unknown") + "\n")
	return b.String()
}

func renderMounts(mounts []SummaryMount) string {
	if len(mounts) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts = append(parts, strings.TrimSpace(m.Path+" "+valueOr(m.Access, "rw")))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
