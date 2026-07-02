package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
	"github.com/charmbracelet/huh"
)

// loadWizardMCP seeds the MCP wizard step from the saved v2 policy (not
// round-tripped legacy flag lines).
func loadWizardMCP(workspace string, extensions []*registry.Extension) ([]MCPServerPolicy, bool) {
	policy, source, err := LoadProjectPolicy(workspace, extensions)
	if err != nil || policy == nil || source != PolicySourceV2 {
		return nil, false
	}
	return append([]MCPServerPolicy(nil), policy.MCP.Servers...), policy.MCP.All
}

// mcpServersToLines renders the wizard's MCP selection as legacy flag lines for
// the "Equivalent" display and session config (names only; modes/pins live in
// policy.MCP).
func mcpServersToLines(servers []MCPServerPolicy, all bool) []string {
	if all {
		return []string{"--mcp-all"}
	}
	names := mcpServerNames(servers)
	if len(names) == 0 {
		return nil
	}
	return []string{"--mcp " + strings.Join(names, ",")}
}

// providerNameFromLines extracts the chosen provider from wizard provider lines.
func providerNameFromLines(lines []string) string {
	for i, l := range lines {
		if l == "--provider" && i+1 < len(lines) {
			return lines[i+1]
		}
		if strings.HasPrefix(l, "--provider ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "--provider "))
		}
	}
	return "claude"
}

// wizardMCP presents per-server MCP mode selection. It returns the structured
// selections and whether "all configured" was chosen.
func wizardMCP(editMode bool, existing []MCPServerPolicy, existingAll bool, providerName, workspace string) ([]MCPServerPolicy, bool, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 4: MCP servers (experimental)"))

	if editMode {
		displayCurrentMCP(existing, existingAll)
		var action string
		if err := huh.NewSelect[string]().
			Title("MCP servers (experimental)").
			Options(huh.NewOption("Keep", "keep"), huh.NewOption("Edit", "edit")).
			Value(&action).
			Run(); err != nil {
			return nil, false, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return existing, existingAll, nil
		}
	}

	provider, _ := providerByName(providerName)
	hostServers := readMCPServers(provider, os.Getenv("HOME"), workspace)
	available := discoverMCPNames(hostServers, existing)

	if len(available) == 0 {
		fmt.Fprintln(os.Stderr, "  No MCP servers discovered.")
		fmt.Fprintln(os.Stderr)
		return nil, false, nil
	}

	// Offer the bulk "all configured (direct)" toggle first. Bulk selection
	// never sets proxy mode (Resolved Q2).
	all := existingAll
	if err := huh.NewConfirm().
		Title("Whitelist all configured MCP servers in direct mode?").
		Description("Enables every configured server (firewall whitelisting only). Choose No to pick servers and modes individually.").
		Value(&all).
		Run(); err != nil {
		return nil, false, err
	}
	if all {
		fmt.Fprintln(os.Stderr)
		return nil, true, nil
	}

	existingMode := map[string]string{}
	for _, s := range existing {
		existingMode[s.Name] = s.Mode
	}
	selected := make([]string, 0)
	for _, s := range existing {
		selected = append(selected, s.Name)
	}
	var opts []huh.Option[string]
	for _, name := range available {
		opts = append(opts, huh.NewOption(name, name).Selected(existingMode[name] != ""))
	}
	if err := huh.NewMultiSelect[string]().
		Title("Select MCP servers").
		Options(opts...).
		Value(&selected).
		Run(); err != nil {
		return nil, false, err
	}
	sort.Strings(selected)

	var out []MCPServerPolicy
	for _, name := range selected {
		srv, known := hostServers[name]
		entry, err := wizardMCPMode(name, srv, known, existingMode[name])
		if err != nil {
			return nil, false, err
		}
		out = append(out, entry)
	}
	fmt.Fprintln(os.Stderr)
	return out, false, nil
}

// wizardMCPMode prompts for a single server's mode, honouring provenance and
// broad-capability constraints, and computes a pin for proxy approvals.
func wizardMCPMode(name string, srv mcpconfig.Server, known bool, existingMode string) (MCPServerPolicy, error) {
	class := classifyMCPServer(srv)
	workspaceScope := known && srv.Scope == mcpconfig.ScopeWorkspace

	opts := []huh.Option[string]{
		huh.NewOption("direct — run in the container as configured", mcpModeDirect),
		huh.NewOption("mount — run in the container, mount helper code read-only", mcpModeMount),
	}
	if known && !workspaceScope {
		opts = append(opts, huh.NewOption("proxy — run on the host via broker: "+mcpCommandLine(srv), mcpModeProxy))
	} else if workspaceScope {
		opts = append(opts, huh.NewOption("proxy — unavailable: workspace-defined server", "proxy-unavailable"))
	}

	mode := existingMode
	if mode == "" || mode == mcpModeProxy {
		mode = class.RecommendedMode // never pre-select proxy
	}
	if mode == mcpModeProxy {
		mode = mcpModeMount
	}

	if err := huh.NewSelect[string]().
		Title("Mode for " + name).
		Description(strings.Join(class.Warnings, "; ")).
		Options(opts...).
		Value(&mode).
		Run(); err != nil {
		return MCPServerPolicy{}, err
	}
	if mode == "proxy-unavailable" {
		mode = mcpModeDirect
	}

	entry := MCPServerPolicy{Name: name, Mode: mode}
	if mode == mcpModeProxy {
		if len(class.Warnings) > 0 {
			confirm := false
			if err := huh.NewConfirm().
				Title("Proxy " + name + " despite broad local capability?").
				Description(strings.Join(class.Warnings, "; ") + "\nProxying grants the sandboxed agent host-level power via this server.").
				Value(&confirm).
				Run(); err != nil {
				return MCPServerPolicy{}, err
			}
			if !confirm {
				entry.Mode = mcpModeMount
				return entry, nil
			}
		}
		entry.CommandPin = mcpCommandPin(srv)
	}
	return entry, nil
}

func discoverMCPNames(hostServers map[string]mcpconfig.Server, existing []MCPServerPolicy) []string {
	seen := map[string]struct{}{}
	var names []string
	add := func(n string) {
		if n == "" {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	if resolver := registry.GetListResolver("mcp"); resolver != nil {
		if items, err := resolver(); err == nil {
			for _, it := range items {
				add(it.Value)
			}
		}
	}
	for name := range hostServers {
		add(name)
	}
	for _, s := range existing {
		add(s.Name)
	}
	sort.Strings(names)
	return names
}

func displayCurrentMCP(servers []MCPServerPolicy, all bool) {
	if all {
		fmt.Fprintln(os.Stderr, "  Current: all configured (direct)")
		return
	}
	if len(servers) == 0 {
		fmt.Fprintln(os.Stderr, "  Current: (no MCP servers)")
		return
	}
	var parts []string
	for _, s := range servers {
		mode := s.Mode
		if mode == "" {
			mode = mcpModeDirect
		}
		parts = append(parts, s.Name+" ("+mode+")")
	}
	fmt.Fprintln(os.Stderr, "  Current: "+strings.Join(parts, ", "))
}
