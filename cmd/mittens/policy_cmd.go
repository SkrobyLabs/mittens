package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
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
	if err := setPolicyField(policy, args[0], args[1]); err != nil {
		return err
	}
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Updated %s for %s\n", args[0], ProjectDir(workspace))
	return nil
}

func setPolicyField(policy *ProjectPolicy, field, value string) error {
	switch field {
	case "provider.name":
		policy.Provider.Name = value
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

Examples:
  mittens policy show
  mittens policy show --json
  mittens policy set provider.name codex
  mittens policy set network.extra_domains '*.apps.example.test'
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
	if policy.Credentials.ProviderOAuth {
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

func capabilitiesFromPolicy(policy *ProjectPolicy) []string {
	var out []string
	for _, cap := range policy.Capabilities {
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
