package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"gopkg.in/yaml.v3"
)

const ProjectPolicyVersion = 2

type PolicySource string

const (
	PolicySourceNone   PolicySource = "none"
	PolicySourceV2     PolicySource = "v2"
	PolicySourceLegacy PolicySource = "legacy"
)

type ProjectPolicy struct {
	Version      int                   `yaml:"version"`
	Provider     ProviderPolicy        `yaml:"provider,omitempty"`
	Workspace    WorkspacePolicy       `yaml:"workspace,omitempty"`
	Network      NetworkPolicy         `yaml:"network,omitempty"`
	Credentials  CredentialPolicy      `yaml:"credentials,omitempty"`
	Host         HostIntegrationPolicy `yaml:"host,omitempty"`
	Capabilities []CapabilityPolicy    `yaml:"capabilities,omitempty"`
	Execution    ExecutionPolicy       `yaml:"execution,omitempty"`
	Options      map[string]string     `yaml:"options,omitempty"`
	ExtraArgs    []string              `yaml:"extra_args,omitempty"`
}

type ProviderPolicy struct {
	Name     string `yaml:"name,omitempty"`
	Backend  string `yaml:"backend,omitempty"`
	Profile  string `yaml:"profile,omitempty"`
	Endpoint string `yaml:"endpoint,omitempty"`
	Model    string `yaml:"model,omitempty"`
}

type WorkspacePolicy struct {
	Mode   string        `yaml:"mode,omitempty"`
	Mounts []PolicyMount `yaml:"mounts,omitempty"`
}

type PolicyMount struct {
	Path   string `yaml:"path"`
	Access string `yaml:"access"`
}

type NetworkPolicy struct {
	Mode         string   `yaml:"mode,omitempty"`
	Firewall     string   `yaml:"firewall,omitempty"`
	CustomConfig string   `yaml:"custom_config,omitempty"`
	ExtraDomains []string `yaml:"extra_domains,omitempty"`
	// SSHEgress controls whether the firewall permits outbound SSH (port 22)
	// from the unprivileged agent. Unset means allowed (so git-over-SSH works);
	// set to false to close this channel. Only meaningful when the firewall is
	// enabled — with the firewall off, all egress is unrestricted anyway.
	SSHEgress *bool `yaml:"ssh_egress,omitempty"`
}

type CredentialPolicy struct {
	ProviderOAuth bool                          `yaml:"provider_oauth,omitempty"`
	Cloud         map[string]CredentialSelector `yaml:"cloud,omitempty"`
}

type CredentialSelector struct {
	Profiles []string `yaml:"profiles,omitempty"`
	All      bool     `yaml:"all,omitempty"`
}

type HostIntegrationPolicy struct {
	OpenURLs        string `yaml:"open_urls,omitempty"`
	ClipboardImages *bool  `yaml:"clipboard_images,omitempty"`
	Notifications   *bool  `yaml:"notifications,omitempty"`
	PathTranslation *bool  `yaml:"path_translation,omitempty"`
}

type CapabilityPolicy struct {
	Name    string   `yaml:"name"`
	Args    []string `yaml:"args,omitempty"`
	All     bool     `yaml:"all,omitempty"`
	RawFlag string   `yaml:"raw_flag,omitempty"`
}

type ExecutionPolicy struct {
	Yolo        *bool  `yaml:"yolo,omitempty"`
	History     *bool  `yaml:"history,omitempty"`
	Worktree    bool   `yaml:"worktree,omitempty"`
	Shell       bool   `yaml:"shell,omitempty"`
	Docker      string `yaml:"docker,omitempty"`
	NetworkHost bool   `yaml:"network_host,omitempty"`
	Notify      *bool  `yaml:"notify,omitempty"`
}

func projectPolicyPath(workspace string) string {
	return filepath.Join(ConfigHome(), "projects", ProjectDir(workspace), "policy.yaml")
}

func LoadProjectPolicy(workspace string, extensions []*registry.Extension) (*ProjectPolicy, PolicySource, error) {
	if policy, err := loadProjectPolicyFile(workspace); err == nil && policy != nil {
		return policy, PolicySourceV2, nil
	} else if err != nil {
		return nil, PolicySourceNone, err
	}

	lines, err := readConfigLines(projectConfigPath(workspace))
	if err != nil {
		return nil, PolicySourceNone, err
	}
	legacyArgs := splitConfigFlags(lines)
	if len(legacyArgs) == 0 {
		return nil, PolicySourceNone, nil
	}
	policy, err := PolicyFromLegacyFlags(legacyArgs, extensions)
	if err != nil {
		return nil, PolicySourceNone, err
	}
	return policy, PolicySourceLegacy, nil
}

func loadProjectPolicyFile(workspace string) (*ProjectPolicy, error) {
	path := projectPolicyPath(workspace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading policy %s: %w", path, err)
	}
	var policy ProjectPolicy
	if err := yaml.Unmarshal(data, &policy); err != nil {
		return nil, fmt.Errorf("parsing policy %s: %w", path, err)
	}
	policy.applyDefaults()
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("validating policy %s: %w", path, err)
	}
	return &policy, nil
}

func SaveProjectPolicy(workspace string, policy *ProjectPolicy) error {
	return savePolicyFile(projectPolicyPath(workspace), policy)
}

// savePolicyFile validates and writes a policy to an explicit path, creating
// parent directories as needed. Used by SaveProjectPolicy and by the doctor
// migration sweep, which operates on project directories directly.
func savePolicyFile(path string, policy *ProjectPolicy) error {
	if policy == nil {
		return fmt.Errorf("policy is nil")
	}
	policy.applyDefaults()
	if err := policy.Validate(); err != nil {
		return err
	}
	payload, err := yaml.Marshal(policy)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating policy dir: %w", err)
	}
	return os.WriteFile(path, payload, 0o644)
}

func PolicyFromLegacyFlags(args []string, extensions []*registry.Extension) (*ProjectPolicy, error) {
	policy := defaultProjectPolicy()
	extByFlag := map[string]*registry.Extension{}
	for _, ext := range extensions {
		for _, flag := range ext.Flags {
			extByFlag[flag.Name] = ext
		}
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			policy.ExtraArgs = append(policy.ExtraArgs, args[i+1:]...)
			break
		}

		switch arg {
		case "--provider":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--provider requires an argument")
			}
			policy.Provider.Name = args[i+1]
			i++
		case "--profile":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--profile requires an argument")
			}
			policy.Provider.Profile = args[i+1]
			i++
		case "--dir", "--dir-ro":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires an argument", arg)
			}
			access := "rw"
			if arg == "--dir-ro" {
				access = "ro"
			}
			policy.Workspace.Mounts = append(policy.Workspace.Mounts, PolicyMount{Path: args[i+1], Access: access})
			i++
		case "--no-firewall":
			policy.Network.Firewall = "disabled"
		case "--firewall-dev":
			policy.Network.Firewall = "dev"
		case "--firewall":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--firewall requires an argument")
			}
			policy.Network.Firewall = "custom"
			policy.Network.CustomConfig = args[i+1]
			i++
		case "--network-host":
			policy.Network.Mode = "host"
			policy.Execution.NetworkHost = true
		case "--worktree":
			policy.Workspace.Mode = "worktree"
			policy.Execution.Worktree = true
		case "--shell":
			policy.Execution.Shell = true
		case "--worker", "--planner":
			// Historical no-op role flags. Ignore them during legacy config migration.
		case "--no-yolo":
			v := false
			policy.Execution.Yolo = &v
		case "--no-history":
			v := false
			policy.Execution.History = &v
		case "--no-notify":
			v := false
			policy.Execution.Notify = &v
			policy.Host.Notifications = boolPtr(false)
		case "--resume":
			// Historical runtime-only flag. Do not migrate it into project policy.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case "--docker":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--docker requires an argument")
			}
			policy.Execution.Docker = args[i+1]
			policy.Capabilities = upsertCapability(policy.Capabilities, CapabilityPolicy{Name: "docker", Args: []string{args[i+1]}, RawFlag: "--docker"})
			i++
		case "--image-paste-key":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--image-paste-key requires an argument")
			}
			policy.Options["image_paste_key"] = args[i+1]
			i++
		case "--name":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--name requires an argument")
			}
			policy.Options["name"] = args[i+1]
			i++
		default:
			if ext, ok := extByFlag[arg]; ok {
				capability, consumed, err := capabilityFromExtensionFlag(ext, arg, args[i:])
				if err != nil {
					return nil, err
				}
				if capability.Name != "firewall" {
					policy.Capabilities = upsertCapability(policy.Capabilities, capability)
				}
				i += consumed - 1
				continue
			}
			policy.ExtraArgs = append(policy.ExtraArgs, arg)
		}
	}

	policy.applyDefaults()
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return policy, nil
}

func (p *ProjectPolicy) ToLegacyFlags() []string {
	if p == nil {
		return nil
	}
	var args []string
	if p.Provider.Name != "" && p.Provider.Name != "claude" {
		args = append(args, "--provider", p.Provider.Name)
	}
	if p.Provider.Profile != "" {
		args = append(args, "--profile", p.Provider.Profile)
	}
	for _, mount := range p.Workspace.Mounts {
		if mount.Access == "ro" {
			args = append(args, "--dir-ro", mount.Path)
		} else {
			args = append(args, "--dir", mount.Path)
		}
	}
	switch p.Network.Firewall {
	case "disabled":
		args = append(args, "--no-firewall")
	case "dev":
		args = append(args, "--firewall-dev")
	case "custom":
		args = append(args, "--firewall", p.Network.CustomConfig)
	}
	if p.Network.Mode == "host" || p.Execution.NetworkHost {
		args = append(args, "--network-host")
	}
	if p.Execution.Worktree || p.Workspace.Mode == "worktree" {
		args = append(args, "--worktree")
	}
	if p.Execution.Shell {
		args = append(args, "--shell")
	}
	if p.Execution.Yolo != nil && !*p.Execution.Yolo {
		args = append(args, "--no-yolo")
	}
	if p.Execution.History != nil && !*p.Execution.History {
		args = append(args, "--no-history")
	}
	if p.Execution.Notify != nil && !*p.Execution.Notify {
		args = append(args, "--no-notify")
	} else if p.Host.Notifications != nil && !*p.Host.Notifications {
		args = append(args, "--no-notify")
	}
	if name := p.Options["name"]; name != "" {
		args = append(args, "--name", name)
	}
	if key := p.Options["image_paste_key"]; key != "" {
		args = append(args, "--image-paste-key", key)
	}
	for _, cap := range p.Capabilities {
		if cap.Name == "" || cap.Name == "docker" {
			if cap.Name == "docker" && len(cap.Args) > 0 && p.Execution.Docker == "" {
				args = append(args, "--docker", cap.Args[0])
			}
			continue
		}
		flag := cap.RawFlag
		if flag == "" {
			flag = "--" + cap.Name
			if cap.All {
				flag += "-all"
			}
		}
		args = append(args, flag)
		if !cap.All && len(cap.Args) > 0 {
			args = append(args, strings.Join(cap.Args, ","))
		}
	}
	args = append(args, p.ExtraArgs...)
	return args
}

func (p *ProjectPolicy) Validate() error {
	if p == nil {
		return fmt.Errorf("policy is nil")
	}
	if p.Version != ProjectPolicyVersion {
		return fmt.Errorf("unsupported policy version %d", p.Version)
	}
	if p.Workspace.Mode != "" && p.Workspace.Mode != "direct" && p.Workspace.Mode != "worktree" {
		return fmt.Errorf("invalid workspace mode %q", p.Workspace.Mode)
	}
	seenMounts := map[string]struct{}{}
	for _, mount := range p.Workspace.Mounts {
		if strings.TrimSpace(mount.Path) == "" {
			return fmt.Errorf("mount path cannot be empty")
		}
		if mount.Access != "rw" && mount.Access != "ro" {
			return fmt.Errorf("invalid access %q for mount %s", mount.Access, mount.Path)
		}
		key := filepath.Clean(mount.Path)
		if _, ok := seenMounts[key]; ok {
			return fmt.Errorf("duplicate mount %s", mount.Path)
		}
		seenMounts[key] = struct{}{}
	}
	if p.Network.Mode != "" && p.Network.Mode != "bridge" && p.Network.Mode != "host" {
		return fmt.Errorf("invalid network mode %q", p.Network.Mode)
	}
	switch p.Network.Firewall {
	case "", "strict", "dev", "custom", "disabled":
	default:
		return fmt.Errorf("invalid firewall mode %q", p.Network.Firewall)
	}
	if p.Network.Firewall == "custom" && p.Network.CustomConfig == "" {
		return fmt.Errorf("custom firewall requires custom_config")
	}
	seenDomains := map[string]struct{}{}
	for _, domain := range p.Network.ExtraDomains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			return fmt.Errorf("network extra domain cannot be empty")
		}
		if strings.ContainsAny(domain, "/:") {
			return fmt.Errorf("network extra domain %q must be a hostname, not a URL", domain)
		}
		key := strings.ToLower(domain)
		if _, ok := seenDomains[key]; ok {
			return fmt.Errorf("duplicate network extra domain %q", domain)
		}
		seenDomains[key] = struct{}{}
	}
	if p.Execution.Docker != "" && p.Execution.Docker != "dind" && p.Execution.Docker != "host" {
		return fmt.Errorf("invalid docker mode %q", p.Execution.Docker)
	}
	switch p.Provider.Backend {
	case "", "claude", "openai":
	default:
		return fmt.Errorf("invalid provider backend %q", p.Provider.Backend)
	}
	if p.Host.OpenURLs != "" && p.Host.OpenURLs != "allow" && p.Host.OpenURLs != "ask" && p.Host.OpenURLs != "deny" {
		return fmt.Errorf("invalid open_urls mode %q", p.Host.OpenURLs)
	}
	for _, cap := range p.Capabilities {
		if strings.TrimSpace(cap.Name) == "" {
			return fmt.Errorf("capability name cannot be empty")
		}
	}
	return nil
}

func defaultProjectPolicy() *ProjectPolicy {
	yolo := true
	history := true
	notify := true
	return &ProjectPolicy{
		Version: ProjectPolicyVersion,
		Provider: ProviderPolicy{
			Name: "claude",
		},
		Workspace: WorkspacePolicy{
			Mode: "direct",
		},
		Network: NetworkPolicy{
			Mode:     "bridge",
			Firewall: "strict",
		},
		Credentials: CredentialPolicy{
			ProviderOAuth: true,
			Cloud:         map[string]CredentialSelector{},
		},
		Host: HostIntegrationPolicy{
			OpenURLs:        "allow",
			ClipboardImages: boolPtr(true),
			Notifications:   boolPtr(true),
			PathTranslation: boolPtr(true),
		},
		Execution: ExecutionPolicy{
			Yolo:    &yolo,
			History: &history,
			Notify:  &notify,
		},
		Options: map[string]string{},
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func boolValue(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func (p *ProjectPolicy) applyDefaults() {
	defaults := defaultProjectPolicy()
	if p.Version == 0 {
		p.Version = ProjectPolicyVersion
	}
	if p.Provider.Name == "" {
		p.Provider.Name = defaults.Provider.Name
	}
	if p.Workspace.Mode == "" {
		p.Workspace.Mode = defaults.Workspace.Mode
	}
	if p.Network.Mode == "" {
		p.Network.Mode = defaults.Network.Mode
	}
	if p.Network.Firewall == "" {
		p.Network.Firewall = defaults.Network.Firewall
	}
	p.Network.ExtraDomains = normalizeNetworkDomains(p.Network.ExtraDomains)
	if p.Credentials.Cloud == nil {
		p.Credentials.Cloud = map[string]CredentialSelector{}
	}
	if p.Host.OpenURLs == "" {
		p.Host.OpenURLs = defaults.Host.OpenURLs
	}
	if p.Host.ClipboardImages == nil {
		p.Host.ClipboardImages = defaults.Host.ClipboardImages
	}
	if p.Host.Notifications == nil {
		p.Host.Notifications = defaults.Host.Notifications
	}
	if p.Host.PathTranslation == nil {
		p.Host.PathTranslation = defaults.Host.PathTranslation
	}
	if p.Execution.Yolo == nil {
		p.Execution.Yolo = defaults.Execution.Yolo
	}
	if p.Execution.History == nil {
		p.Execution.History = defaults.Execution.History
	}
	if p.Execution.Notify == nil {
		p.Execution.Notify = defaults.Execution.Notify
	}
	if p.Options == nil {
		p.Options = map[string]string{}
	}
}

func normalizeNetworkDomains(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(strings.ToLower(domain))
		if strings.HasPrefix(domain, "*.") {
			domain = "." + strings.TrimPrefix(domain, "*.")
		}
		if domain != "" {
			out = append(out, domain)
		}
	}
	return out
}

func capabilityFromExtensionFlag(ext *registry.Extension, flagName string, args []string) (CapabilityPolicy, int, error) {
	for _, flag := range ext.Flags {
		if flag.Name != flagName {
			continue
		}
		capability := CapabilityPolicy{Name: ext.Name, RawFlag: flagName}
		switch flag.Arg {
		case "", "none":
			if strings.HasSuffix(flagName, "-all") {
				capability.All = true
			}
			return capability, 1, nil
		case "csv", "path":
			if len(args) < 2 {
				return capability, 0, fmt.Errorf("%s requires an argument", flagName)
			}
			if flag.Arg == "csv" {
				capability.Args = splitCSV(args[1])
			} else {
				capability.Args = []string{args[1]}
			}
			return capability, 2, nil
		case "enum":
			if len(args) >= 2 && extensionEnumAccepts(flag, args[1]) {
				capability.Args = splitEnumArg(flag, args[1])
				return capability, 2, nil
			}
			return capability, 1, nil
		default:
			return capability, 1, nil
		}
	}
	return CapabilityPolicy{}, 0, fmt.Errorf("unknown extension flag %s", flagName)
}

func extensionEnumAccepts(flag registry.ExtensionFlag, value string) bool {
	parts := splitEnumArg(flag, value)
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		found := false
		for _, allowed := range flag.EnumValues {
			if part == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func splitEnumArg(flag registry.ExtensionFlag, value string) []string {
	if flag.Multi {
		return splitCSV(value)
	}
	return []string{value}
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func upsertCapability(caps []CapabilityPolicy, next CapabilityPolicy) []CapabilityPolicy {
	for i, cap := range caps {
		if cap.Name == next.Name && cap.RawFlag == next.RawFlag {
			caps[i] = next
			return caps
		}
	}
	return append(caps, next)
}

func legacyArgsToConfigLines(args []string) []string {
	var lines []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				lines = append(lines, strings.Join(args[i:], " "))
			}
			break
		}
		if strings.HasPrefix(arg, "--") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			lines = append(lines, arg+" "+args[i+1])
			i++
			continue
		}
		lines = append(lines, arg)
	}
	return lines
}
