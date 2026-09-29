package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// wizardOverviewRows returns the compact, count-oriented policy overview used
// by wizard confirmation screens. It deliberately keeps list values out of the
// overview; renderWizardDetails is the place to inspect every selected item.
func wizardOverviewRows(policy *ProjectPolicy, workspace string) []string {
	summary := launchSummaryFromPolicy(policy, workspace)
	provider := valueOr(summary.Provider, "unknown")
	if summary.Profile != "" {
		provider += " (model preset " + summary.Profile + ")"
	}

	rows := []string{
		"Provider: " + provider,
		"Directories: " + wizardDirectoryCount(summary),
		"Extensions: " + wizardItemCount(summary.Extensions, "enabled"),
		"MCP: " + wizardMCPCount(policy, summary),
		"Network: " + valueOr(summary.Network, "unknown"),
		"Execution: " + strings.Join(wizardExecutionItems(policy), "; "),
	}
	if !onlyNone(summary.Credentials) {
		rows = append(rows, "Credentials: "+wizardItemCount(summary.Credentials, "staged"))
	}
	if !onlyNone(summary.HostIntegrations) {
		rows = append(rows, "Host integrations: "+wizardItemCount(summary.HostIntegrations, "enabled"))
	}
	return rows
}

func wizardDirectoryCount(summary LaunchSummary) string {
	if len(summary.ExtraDirs) == 0 {
		return "workspace only"
	}
	return fmt.Sprintf("workspace + %d extra", len(summary.ExtraDirs))
}

func wizardMCPCount(policy *ProjectPolicy, summary LaunchSummary) string {
	selected := len(summary.MCPServers)
	if selected == 1 && summary.MCPServers[0] == "none" {
		selected = 0
	}
	if policy != nil && policy.MCP.All {
		explicit := selected
		if explicit > 0 && summary.MCPServers[0] == "all configured" {
			explicit--
		}
		if explicit == 0 {
			return "all configured"
		}
		return fmt.Sprintf("all configured + %d explicit", explicit)
	}
	if selected == 0 {
		return "none"
	}
	return fmt.Sprintf("%d selected", selected)
}

func wizardItemCount(items []string, suffix string) string {
	if onlyNone(items) {
		return "none"
	}
	return fmt.Sprintf("%d %s", len(items), suffix)
}

func onlyNone(items []string) bool {
	return len(items) == 0 || len(items) == 1 && (items[0] == "none" || items[0] == "none staged")
}

// renderWizardDetails returns the complete policy boundary with each repeated
// value on its own line. It is intentionally separate from LaunchSummary.Render
// so startup output remains compact and unchanged.
func renderWizardDetails(policy *ProjectPolicy, workspace string) string {
	width := 0
	if term.IsTerminal(int(os.Stderr.Fd())) {
		if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil {
			width = w
		}
	}
	return renderWizardDetailsWithWidth(policy, workspace, width)
}

func renderWizardDetailsWithWidth(policy *ProjectPolicy, workspace string, width int) string {
	summary := launchSummaryFromPolicy(policy, workspace)
	var lines []string
	appendValue := func(prefix, value string) {
		lines = append(lines, wrapWizardDetailLine(prefix, value, width)...)
	}
	appendItems := func(heading, itemPrefix string, items []string) {
		lines = append(lines, heading+":")
		if len(items) == 0 {
			appendValue("  ", "none")
			return
		}
		for _, item := range items {
			appendValue("  "+itemPrefix, item)
		}
	}

	lines = append(lines, "Details")
	provider := valueOr(summary.Provider, "unknown")
	if summary.Profile != "" {
		provider += " (model preset " + summary.Profile + ")"
	}
	appendValue("Provider: ", provider)
	if policy != nil {
		for _, item := range []struct{ label, value string }{
			{"Backend", policy.Provider.Backend}, {"Model", policy.Provider.Model},
			{"Effort", policy.Provider.Effort}, {"Endpoint", policy.Provider.Endpoint},
		} {
			if item.value != "" {
				appendValue("  "+item.label+": ", item.value)
			}
		}
	}
	lines = append(lines, "Directories:")
	appendValue("  Workspace: ", formatWizardMount(summary.Workspace))
	if len(summary.ExtraDirs) == 0 {
		appendValue("  ", "no extra directories")
	} else {
		for _, mount := range summary.ExtraDirs {
			appendValue("  Extra: ", formatWizardMount(mount))
		}
	}

	appendItems("MCP", "Server: ", valueOrList(summary.MCPServers, []string{"none"}))
	if len(summary.MCPProxy) > 0 {
		appendItems("MCP proxy", "Server: ", summary.MCPProxy)
	}
	if len(summary.MCPEnv) > 0 {
		appendItems("MCP environment", "Server: ", summary.MCPEnv)
	}
	if len(summary.MCPHelperMounts) > 0 {
		lines = append(lines, "MCP helper mounts:")
		for _, mount := range summary.MCPHelperMounts {
			appendValue("  Mount: ", formatWizardMount(mount))
		}
	}
	appendItems("Credentials", "Credential: ", valueOrList(summary.Credentials, []string{"none staged"}))
	appendValue("Network: ", valueOr(summary.Network, "unknown"))
	appendItems("Extensions", "Extension: ", valueOrList(summary.Extensions, []string{"none"}))
	if policy != nil {
		for _, cap := range policy.Capabilities {
			if cap.All {
				appendValue("  ", cap.Name+": all configured")
			}
			for _, arg := range cap.Args {
				appendValue("  ", cap.Name+": "+arg)
			}
		}
		var clouds []string
		for name := range policy.Credentials.Cloud {
			clouds = append(clouds, name)
		}
		sort.Strings(clouds)
		for _, name := range clouds {
			selector := policy.Credentials.Cloud[name]
			if selector.All {
				appendValue("Credential selection: ", name+": all configured")
			}
			for _, profile := range selector.Profiles {
				appendValue("Credential selection: ", name+": "+profile)
			}
		}
		if policy.Network.CustomConfig != "" {
			appendValue("Firewall file: ", policy.Network.CustomConfig)
		}
		for _, domain := range policy.Network.ExtraDomains {
			appendValue("Allowed domain: ", domain)
		}
		ssh := "allowed"
		if !boolValue(policy.Network.SSHEgress, true) {
			ssh = "blocked"
		}
		appendValue("SSH egress: ", ssh)
	}
	appendItems("Host integrations", "Integration: ", valueOrList(summary.HostIntegrations, []string{"none"}))
	appendItems("Execution", "Mode: ", wizardExecutionItems(policy))
	appendValue("History: ", valueOr(summary.History, "unknown"))
	if policy != nil {
		appendValue("Open host browser: ", valueOr(policy.Host.OpenURLs, "allow"))
		for _, item := range []struct {
			label   string
			enabled bool
		}{
			{"Provider sign-in", policy.Credentials.ProviderOAuth},
			{"Clipboard images", boolValue(policy.Host.ClipboardImages, true)},
			{"Notifications", boolValue(policy.Host.Notifications, true)},
			{"Path translation", boolValue(policy.Host.PathTranslation, true)},
			{"Completion notification", boolValue(policy.Execution.Notify, true)},
		} {
			value := "disabled"
			if item.enabled {
				value = "enabled"
			}
			appendValue(item.label+": ", value)
		}
		for _, server := range policy.MCP.Servers {
			if server.CommandPin != "" {
				appendValue("Approved MCP command: ", server.Name+" ("+server.CommandPin+")")
			}
		}
		var options []string
		for name := range policy.Options {
			options = append(options, name)
		}
		sort.Strings(options)
		for _, name := range options {
			appendValue("Option: ", name+" = "+policy.Options[name])
		}
		for _, arg := range policy.ExtraArgs {
			appendValue("Provider argument: ", arg)
		}
	}

	return strings.Join(lines, "\n") + "\n"
}

func formatWizardMount(mount SummaryMount) string {
	path := valueOr(mount.Path, "unknown")
	return path + " (" + valueOr(mount.Access, "rw") + ")"
}

// wrapWizardDetailLine preserves every character in a value while preventing a
// wide terminal from producing unreadable horizontal scrolling. A width of zero
// leaves the line intact, which is also suitable for redirected output.
func wrapWizardDetailLine(prefix, value string, width int) []string {
	line := prefix + value
	if width <= 0 || lipgloss.Width(line) <= width {
		return []string{line}
	}

	continuation := "  "
	available := width
	var lines []string
	var current strings.Builder
	for _, r := range line {
		runeWidth := lipgloss.Width(string(r))
		if current.Len() > 0 && lipgloss.Width(current.String())+runeWidth > available {
			lines = append(lines, current.String())
			current.Reset()
			current.WriteString(continuation)
			available = width
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func wizardExecutionItems(policy *ProjectPolicy) []string {
	if policy == nil {
		policy = defaultProjectPolicy()
	}
	items := executionFromPolicy(policy)
	for i, item := range items {
		if item == "yolo" {
			items[i] = "automatic approvals"
		}
		if item == "permission prompts" {
			items[i] = "approval prompts"
		}
	}
	return items
}
