package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
	"gopkg.in/yaml.v3"
)

func runPolicy(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printPolicyHelp()
		return nil
	}
	switch args[0] {
	case "show":
		exts, err := loadExtensions()
		if err != nil {
			return fmt.Errorf("loading extensions: %w", err)
		}
		return runPolicyShow(args[1:], exts)
	case "set":
		exts, err := loadExtensions()
		if err != nil {
			return fmt.Errorf("loading extensions: %w", err)
		}
		return runPolicySet(args[1:], exts)
	case "allow":
		exts, err := loadExtensions()
		if err != nil {
			return fmt.Errorf("loading extensions: %w", err)
		}
		return runPolicyAllow(args[1:], exts)
	default:
		return fmt.Errorf("unknown policy command %q", args[0])
	}
}

func runPolicyShow(args []string, extensions []*registry.Extension) error {
	jsonOut := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			return fmt.Errorf("policy show no longer accepts launch flag overrides; use `mittens policy set` or `mittens init`")
		}
	}

	workspace := detectWorkspace()
	policy, source, err := effectivePolicyForShow(workspace, extensions)
	if err != nil {
		return err
	}

	summary := launchSummaryFromPolicy(policy, workspace)
	if jsonOut {
		out := struct {
			Source  PolicySource   `json:"source"`
			Policy  *ProjectPolicy `json:"policy"`
			Summary LaunchSummary  `json:"summary"`
		}{
			Source:  source,
			Policy:  policy,
			Summary: summary,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Fprint(os.Stdout, summary.Render())
	fmt.Fprintf(os.Stdout, "\nPolicy source: %s\n\n", source)
	payload, err := yaml.Marshal(policy)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, string(payload))
	return nil
}

func effectivePolicyForShow(workspace string, extensions []*registry.Extension) (*ProjectPolicy, PolicySource, error) {
	projectPolicy, source, err := LoadProjectPolicy(workspace, extensions)
	if err != nil {
		return nil, PolicySourceNone, err
	}
	if projectPolicy != nil {
		return projectPolicy, source, nil
	}

	userArgs, _ := LoadUserDefaults()
	if len(userArgs) == 0 {
		return defaultProjectPolicy(), PolicySourceNone, nil
	}
	policy, err := PolicyFromLegacyFlags(userArgs, extensions)
	if err != nil {
		return nil, PolicySourceNone, err
	}
	return policy, PolicySourceNone, nil
}

func runPolicySet(args []string, extensions []*registry.Extension) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: mittens policy set <field> <value>")
	}
	workspace := detectWorkspace()
	policy, _, err := LoadProjectPolicy(workspace, extensions)
	if err != nil {
		return err
	}
	if policy == nil {
		policy = defaultProjectPolicy()
	}
	if strings.HasPrefix(args[0], "mcp.") {
		if err := setMCPPolicyField(policy, workspace, args[0], args[1]); err != nil {
			return err
		}
	} else if err := setPolicyField(policy, args[0], args[1]); err != nil {
		return err
	}
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Updated %s for %s\n", args[0], ProjectDir(workspace))
	return nil
}

// runPolicyAllow appends one or more domains to network.extra_domains. It is the
// command the in-container firewall denial message points operators at, and the
// same path the firewall-learn report uses to persist discovered domains.
func runPolicyAllow(args []string, extensions []*registry.Extension) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mittens policy allow <domain> [domain...]")
	}
	workspace := detectWorkspace()
	added, err := addExtraDomains(workspace, extensions, args)
	if err != nil {
		return err
	}
	if len(added) == 0 {
		fmt.Fprintln(os.Stdout, "All domains already in network.extra_domains; nothing to add")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Added %s to network.extra_domains for %s\n", strings.Join(added, ", "), ProjectDir(workspace))
	return nil
}

// addExtraDomains normalizes, de-duplicates against the existing allowlist, and
// appends the given domains to the project's network.extra_domains, saving the
// policy. It returns the domains that were actually new (empty if all were
// already present, in which case the policy file is left untouched).
func addExtraDomains(workspace string, extensions []*registry.Extension, domains []string) ([]string, error) {
	incoming := normalizeNetworkDomains(domains)
	if len(incoming) == 0 {
		return nil, fmt.Errorf("no valid domains to add")
	}
	policy, _, err := LoadProjectPolicy(workspace, extensions)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		policy = defaultProjectPolicy()
	}
	seen := make(map[string]bool, len(policy.Network.ExtraDomains))
	for _, d := range policy.Network.ExtraDomains {
		seen[d] = true
	}
	var added []string
	for _, d := range incoming {
		if seen[d] {
			continue
		}
		seen[d] = true
		policy.Network.ExtraDomains = append(policy.Network.ExtraDomains, d)
		added = append(added, d)
	}
	if len(added) == 0 {
		return nil, nil
	}
	policy.applyDefaults()
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		return nil, err
	}
	return added, nil
}

func setPolicyField(policy *ProjectPolicy, field, value string) error {
	switch field {
	case "provider.name":
		policy.Provider.Name = value
	case "provider.backend":
		policy.Provider.Backend = canonicalProviderBackend(value)
	case "provider.profile":
		policy.Provider.Profile = value
	case "provider.endpoint":
		policy.Provider.Endpoint = value
	case "provider.model":
		policy.Provider.Model = value
	case "workspace.mode":
		policy.Workspace.Mode = value
		policy.Execution.Worktree = value == "worktree"
	case "network.mode":
		policy.Network.Mode = value
		policy.Execution.NetworkHost = value == "host"
	case "network.firewall":
		policy.Network.Firewall = value
	case "network.custom_config":
		policy.Network.CustomConfig = value
	case "network.extra_domains":
		policy.Network.ExtraDomains = normalizeNetworkDomains(parsePolicyList(value))
	case "network.ssh_egress":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Network.SSHEgress = &v
	case "host.open_urls":
		policy.Host.OpenURLs = value
	case "host.clipboard_images":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Host.ClipboardImages = &v
	case "host.notifications":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Host.Notifications = &v
		policy.Execution.Notify = &v
	case "host.path_translation":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Host.PathTranslation = &v
	case "execution.yolo":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Execution.Yolo = &v
	case "execution.history":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Execution.History = &v
	case "execution.headless":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Execution.Headless = &v
	case "execution.worktree":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Execution.Worktree = v
		if v {
			policy.Workspace.Mode = "worktree"
		} else {
			policy.Workspace.Mode = "direct"
		}
	case "execution.shell":
		v, err := parsePolicyBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		policy.Execution.Shell = v
	case "execution.docker":
		policy.Execution.Docker = value
	default:
		return fmt.Errorf("unsupported policy field %q", field)
	}
	policy.applyDefaults()
	return policy.Validate()
}

// setMCPPolicyField handles `mcp.<server>.mode <mode>`. Proxy mode resolves the
// server from current host config, refuses workspace-only servers (v1), prints
// the resolved command line, and records a command pin.
func setMCPPolicyField(policy *ProjectPolicy, workspace, field, value string) error {
	parts := strings.Split(field, ".")
	if len(parts) != 3 || parts[0] != "mcp" || parts[2] != "mode" {
		return fmt.Errorf("unsupported mcp policy field %q (expected mcp.<server>.mode)", field)
	}
	name := parts[1]
	mode := strings.TrimSpace(value)
	switch mode {
	case mcpModeDirect, mcpModeMount, mcpModeProxy:
	default:
		return fmt.Errorf("invalid mcp mode %q (expected direct, mount, or proxy)", value)
	}

	provider, err := providerByName(policy.Provider.Name)
	if err != nil {
		return err
	}
	servers := readMCPServers(provider, os.Getenv("HOME"), workspace)
	server, ok := servers[name]
	if !ok {
		return fmt.Errorf("unknown MCP server %q; configured servers: %s", name, strings.Join(mcpconfigNames(servers), ", "))
	}

	entry := MCPServerPolicy{Name: name, Mode: mode}
	if mode == mcpModeProxy {
		if server.Scope == mcpconfig.ScopeWorkspace {
			return fmt.Errorf("MCP server %q is defined only in workspace .mcp.json; proxy mode for workspace-scope servers is not supported in v1 (use the wizard for user-scope servers)", name)
		}
		entry.CommandPin = mcpCommandPin(server)
		fmt.Fprintf(os.Stdout, "Proxying %q on the host: %s\n", name, mcpCommandLine(server))
		fmt.Fprintf(os.Stdout, "Command pin: %s\n", entry.CommandPin)
	}
	policy.MCP.Servers = upsertMCPServer(policy.MCP.Servers, entry)
	policy.applyDefaults()
	return policy.Validate()
}

func mcpconfigNames(servers map[string]mcpconfig.Server) []string {
	return mcpconfig.Names(servers)
}

func mcpCommandLine(s mcpconfig.Server) string {
	if s.URL != "" {
		return s.URL
	}
	return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
}

func parsePolicyBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1", "allow", "enabled":
		return true, nil
	case "false", "no", "off", "0", "deny", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean value, got %q", value)
	}
}

func printPolicyHelp() {
	fmt.Println(`mittens policy - Inspect and edit project policy

Usage:
  mittens policy show [--json]
  mittens policy set <field> <value>
  mittens policy allow <domain> [domain...]

Examples:
  mittens policy show
  mittens policy show --json
  mittens policy set provider.name codex
  mittens policy set provider.backend openai
  mittens policy set provider.endpoint http://host.docker.internal:9223
  mittens policy set network.extra_domains '*.apps.example.test'
  mittens policy allow api.example.com '*.cdn.example.net'
  mittens policy set host.open_urls deny
  mittens policy set host.clipboard_images false`)
}

func parsePolicyList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func launchSummaryFromPolicy(policy *ProjectPolicy, workspace string) LaunchSummary {
	if policy == nil {
		policy = defaultProjectPolicy()
	}
	provider, err := providerByName(policy.Provider.Name)
	providerName := policy.Provider.Name
	if err == nil {
		provider.ApplyPolicy(policy.Provider)
		providerName = provider.DisplayName
	}
	workspaceMode := "rw"
	workspacePath := workspace
	if policy.Workspace.Mode == "worktree" {
		workspaceMode = "rw worktree"
	}

	var extraDirs []SummaryMount
	for _, mount := range policy.Workspace.Mounts {
		extraDirs = append(extraDirs, SummaryMount{Path: mount.Path, Access: mount.Access})
	}

	network := policy.Network.Mode
	if network == "" {
		network = "bridge"
	}
	switch network {
	case "host":
		network = "host network"
	default:
		network = "docker bridge"
	}
	switch policy.Network.Firewall {
	case "disabled":
		network += ", firewall disabled"
	case "dev":
		network += ", developer firewall allowlist"
	case "custom":
		network += ", custom firewall allowlist"
	default:
		network += ", firewall allowlist"
	}

	return LaunchSummary{
		Provider:         providerName,
		Profile:          policy.Provider.Profile,
		Workspace:        SummaryMount{Path: workspacePath, Access: workspaceMode},
		ExtraDirs:        extraDirs,
		MCPServers:       mcpServersFromPolicy(policy),
		Credentials:      credentialsFromPolicy(policy),
		Network:          network,
		Extensions:       capabilitiesFromPolicy(policy),
		HostIntegrations: hostIntegrationsFromPolicy(policy),
		Execution:        executionFromPolicy(policy),
		History:          historyFromPolicy(policy),
	}
}

func credentialsFromPolicy(policy *ProjectPolicy) []string {
	var out []string
	if policy.Credentials.ProviderOAuth && !(policy.Provider.Name == "claude" && canonicalProviderBackend(policy.Provider.Backend) == "openai") {
		out = append(out, "provider OAuth")
	}
	for name, selector := range policy.Credentials.Cloud {
		if selector.All {
			out = append(out, name+" all")
			continue
		}
		if len(selector.Profiles) > 0 {
			out = append(out, name+" selected")
		}
	}
	if len(out) == 0 {
		return []string{"none staged"}
	}
	return uniqueSorted(out)
}

func mcpServersFromPolicy(policy *ProjectPolicy) []string {
	var out []string
	if policy.MCP.All {
		out = append(out, "all configured")
	}
	for _, server := range policy.MCP.Servers {
		mode := server.Mode
		if mode == "" {
			mode = mcpModeDirect
		}
		out = append(out, server.Name+" ("+mode+")")
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

func capabilitiesFromPolicy(policy *ProjectPolicy) []string {
	var out []string
	for _, cap := range policy.Capabilities {
		if cap.Name == "mcp" {
			continue
		}
		out = append(out, cap.Name)
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return uniqueSorted(out)
}

func hostIntegrationsFromPolicy(policy *ProjectPolicy) []string {
	var out []string
	if policy.Host.OpenURLs != "deny" {
		out = append(out, "URL opening")
	}
	if boolValue(policy.Host.ClipboardImages, true) {
		out = append(out, "clipboard images")
	}
	if boolValue(policy.Host.Notifications, true) {
		out = append(out, "notifications")
	}
	if boolValue(policy.Host.PathTranslation, true) {
		out = append(out, "drag/drop path translation")
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return uniqueSorted(out)
}

func executionFromPolicy(policy *ProjectPolicy) []string {
	var out []string
	if policy.Execution.Yolo == nil || *policy.Execution.Yolo {
		out = append(out, "yolo")
	} else {
		out = append(out, "permission prompts")
	}
	if policy.Execution.Shell {
		out = append(out, "shell")
	}
	if policy.Execution.Headless != nil && *policy.Execution.Headless {
		out = append(out, "headless")
	}
	if policy.Execution.Worktree || policy.Workspace.Mode == "worktree" {
		out = append(out, "worktree")
	}
	switch policy.Execution.Docker {
	case "dind":
		out = append(out, "Docker-in-Docker")
	case "host":
		out = append(out, "host Docker socket")
	default:
		out = append(out, "no Docker access")
	}
	return out
}

func historyFromPolicy(policy *ProjectPolicy) string {
	if policy.Execution.History != nil && !*policy.Execution.History {
		return "disabled"
	}
	return "enabled"
}
