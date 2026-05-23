package main

import (
	"fmt"
	"os"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

// ProviderRuntimePlan is the provider-owned contribution to image build and
// launch behavior. It keeps provider special cases out of generic orchestration.
type ProviderRuntimePlan struct {
	ImageTagParts            []string
	BuildArgs                map[string]string
	FirewallDomains          []string
	AI                       initcfg.AIConfig
	ContainerEnv             map[string]string
	ContainerHostname        string
	DockerArgs               []string
	DefaultArgs              []string
	HistoryMountsWholeConfig bool
	HistoryMountsProjectDirs bool
	LiveMountFiles           []string
	LiveMountDirs            []string
	SkipCredentials          bool
	LocalModelSource         string
	DefaultModel             string
}

// RuntimePlan returns the provider's contribution to the runtime plan.
func (p *Provider) RuntimePlan() ProviderRuntimePlan {
	plan := ProviderRuntimePlan{
		BuildArgs: map[string]string{
			"AI_USERNAME":    p.Username,
			"AI_BINARY":      p.Binary,
			"AI_INSTALL_CMD": p.InstallCmd,
			"AI_CONFIG_DIR":  p.ConfigDir,
		},
		FirewallDomains:          append([]string(nil), p.FirewallDomains...),
		AI:                       p.initConfigPlan(),
		ContainerEnv:             copyStringMap(p.ContainerEnv),
		ContainerHostname:        p.ContainerHostname,
		DockerArgs:               append([]string(nil), p.DockerArgs...),
		DefaultArgs:              append([]string(nil), p.DefaultArgs...),
		HistoryMountsWholeConfig: p.HistoryMountsWholeConfig,
		HistoryMountsProjectDirs: p.HistoryMountsProjectDirs,
		LiveMountFiles:           append([]string(nil), p.LiveMountFiles...),
		LiveMountDirs:            append([]string(nil), p.LiveMountDirs...),
		SkipCredentials:          p.SkipCredentials,
		LocalModelSource:         p.LocalModelSource,
		DefaultModel:             p.DefaultModel,
	}
	if p.Name != "" && p.Name != "claude" {
		plan.ImageTagParts = append(plan.ImageTagParts, p.Name)
	}
	return plan
}

func (p *Provider) initConfigPlan() initcfg.AIConfig {
	return initcfg.AIConfig{
		Binary:          p.Binary,
		ConfigDir:       p.ConfigDir,
		CredFile:        p.CredentialFile,
		PrefsFile:       p.UserPrefsFile,
		SettingsFile:    p.SettingsFile,
		ProjectFile:     p.ProjectFile,
		TrustedDirsKey:  p.TrustedDirsKey,
		YoloKey:         p.YoloKey,
		MCPServersKey:   p.MCPServersKey,
		TrustedDirsFile: p.TrustedDirsFile,
		InitSettingsJQ:  p.InitSettingsJQ,
		StopHookEvent:   p.StopHookEvent,
		PersistFiles:    append([]string(nil), p.PersistFiles...),
		PersistDirs:     append([]string(nil), p.PersistDirs...),
		PersistGlobs:    append([]string(nil), p.PersistGlobs...),
		SettingsFormat:  p.SettingsFormat,
		ConfigSubdirs:   append([]string(nil), p.ConfigSubdirs...),
		PluginDir:       p.PluginDir,
		PluginFiles:     append([]string(nil), p.PluginFiles...),
	}
}

// CapabilityRuntimePlan is the extension-owned contribution to image build and
// launch behavior. Enabled extensions are compiled into these plans before the
// main app consumes them.
type CapabilityRuntimePlan struct {
	Name            string
	Install         bool
	ImageTagPart    string
	BuildArgs       map[string]string
	Mounts          []registry.ResolvedMount
	Env             map[string]string
	Capabilities    []string
	FirewallDomains []string
}

// CapabilitySetupPlan is the resolver-owned contribution produced during host
// setup before docker run args are assembled.
type CapabilitySetupPlan struct {
	Name            string
	DockerArgs      []string
	FirewallDomains []string
	CredStagingDirs []string
}

func capabilityRuntimePlan(ext *registry.Extension, home, containerHome string) CapabilityRuntimePlan {
	plan := CapabilityRuntimePlan{
		Name:            ext.Name,
		ImageTagPart:    ext.ImageTagPart(),
		BuildArgs:       ext.BuildArgs(),
		Mounts:          ext.ExpandedMounts(home, containerHome),
		Env:             copyStringMap(ext.Env),
		Capabilities:    append([]string(nil), ext.Capabilities...),
		FirewallDomains: ext.FirewallDomains(),
	}
	plan.Install = ext.Build != nil && ext.Build.Script != ""
	return plan
}

func capabilityRuntimePlans(exts []*registry.Extension, home, containerHome string) []CapabilityRuntimePlan {
	var plans []CapabilityRuntimePlan
	for _, ext := range exts {
		if ext == nil || !ext.Enabled {
			continue
		}
		plans = append(plans, capabilityRuntimePlan(ext, home, containerHome))
	}
	return plans
}

func (a *App) capabilitySetupPlans(home string) ([]CapabilitySetupPlan, error) {
	var plans []CapabilitySetupPlan
	for _, ext := range a.Extensions {
		if ext == nil || !ext.Enabled {
			continue
		}
		setupFn := registry.GetSetupResolver(ext.Name)
		if setupFn == nil {
			continue
		}
		logVerbose(a.Verbose, "Setting up extension: %s", ext.Name)

		staging, err := os.MkdirTemp("", "mittens-"+ext.Name+"-*")
		if err != nil {
			return nil, fmt.Errorf("creating staging dir for %s: %w", ext.Name, err)
		}
		a.tempDirs = append(a.tempDirs, staging)

		plan := CapabilitySetupPlan{Name: ext.Name}
		ctx := &registry.SetupContext{
			Home:            home,
			ContainerHome:   a.Provider.HomePath(),
			ContainerName:   a.ContainerName,
			Extension:       ext,
			DockerArgs:      &plan.DockerArgs,
			FirewallExtra:   &plan.FirewallDomains,
			TempDirs:        &a.tempDirs,
			StagingDir:      staging,
			CredStagingDirs: &plan.CredStagingDirs,
		}
		if err := setupFn(ctx); err != nil {
			return nil, fmt.Errorf("extension %s setup: %w", ext.Name, err)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func flattenCapabilitySetupPlans(plans []CapabilitySetupPlan) (dockerArgs []string, firewallDomains []string, credStagingDirs []string) {
	for _, plan := range plans {
		dockerArgs = append(dockerArgs, plan.DockerArgs...)
		firewallDomains = append(firewallDomains, plan.FirewallDomains...)
		credStagingDirs = append(credStagingDirs, plan.CredStagingDirs...)
	}
	return dockerArgs, firewallDomains, credStagingDirs
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
