package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestProviderRuntimePlan_DefaultProviderHasBuildArgs(t *testing.T) {
	p := ClaudeProvider()
	plan := p.RuntimePlan()

	if len(plan.ImageTagParts) != 0 {
		t.Fatalf("ImageTagParts = %v, want none for default provider", plan.ImageTagParts)
	}
	if plan.BuildArgs["AI_BINARY"] != "claude" {
		t.Fatalf("AI_BINARY build arg = %q, want claude", plan.BuildArgs["AI_BINARY"])
	}
	if plan.BuildArgs["AI_CONFIG_DIR"] != ".claude" {
		t.Fatalf("AI_CONFIG_DIR build arg = %q, want .claude", plan.BuildArgs["AI_CONFIG_DIR"])
	}
	if !reflect.DeepEqual(plan.FirewallDomains, p.FirewallDomains) {
		t.Fatalf("FirewallDomains = %v, want %v", plan.FirewallDomains, p.FirewallDomains)
	}
	if plan.AI.Binary != "claude" {
		t.Fatalf("AI.Binary = %q, want claude", plan.AI.Binary)
	}
	if plan.AI.InitSettingsJQ != p.InitSettingsJQ {
		t.Fatalf("AI.InitSettingsJQ = %q, want %q", plan.AI.InitSettingsJQ, p.InitSettingsJQ)
	}
	if !reflect.DeepEqual(plan.AI.ConfigSubdirs, p.ConfigSubdirs) {
		t.Fatalf("AI.ConfigSubdirs = %v, want %v", plan.AI.ConfigSubdirs, p.ConfigSubdirs)
	}
}

func TestProviderRuntimePlan_NonDefaultProviderTagsImage(t *testing.T) {
	p := CodexProvider()
	plan := p.RuntimePlan()

	if got, want := plan.ImageTagParts, []string{"codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ImageTagParts = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(plan.LiveMountFiles, p.LiveMountFiles) {
		t.Fatalf("LiveMountFiles = %v, want %v", plan.LiveMountFiles, p.LiveMountFiles)
	}
	if !reflect.DeepEqual(plan.LiveMountDirs, p.LiveMountDirs) {
		t.Fatalf("LiveMountDirs = %v, want %v", plan.LiveMountDirs, p.LiveMountDirs)
	}
}

func TestCapabilityRuntimePlans_OnlyEnabledExtensionsContribute(t *testing.T) {
	home := t.TempDir()
	plans := capabilityRuntimePlans([]*registry.Extension{
		{
			Name:    "go",
			Enabled: true,
			Build: &registry.BuildConfig{
				Script:   "build.sh",
				ImageTag: "go{{.Arg}}",
				Args: map[string]string{
					"GO_VERSION": "{{.Arg}}",
				},
			},
			RawArg:   "1.23",
			Firewall: []string{"proxy.golang.org"},
			Env:      map[string]string{"GOENV": "on"},
			Capabilities: []string{
				"NET_RAW",
			},
			Mounts: []registry.MountConfig{
				{Src: "~/go", Dst: "~/.go", Mode: "rw"},
			},
		},
		{
			Name:    "disabled",
			Enabled: false,
			Build:   &registry.BuildConfig{Script: "build.sh", ImageTag: "disabled"},
		},
	}, home, "/home/agent")

	if len(plans) != 1 {
		t.Fatalf("len(plans) = %d, want 1", len(plans))
	}
	plan := plans[0]
	if !plan.Install {
		t.Fatal("Install = false, want true")
	}
	if plan.ImageTagPart != "go1.23" {
		t.Fatalf("ImageTagPart = %q, want go1.23", plan.ImageTagPart)
	}
	if plan.BuildArgs["GO_VERSION"] != "1.23" {
		t.Fatalf("GO_VERSION build arg = %q, want 1.23", plan.BuildArgs["GO_VERSION"])
	}
	if got, want := plan.FirewallDomains, []string{"proxy.golang.org"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FirewallDomains = %v, want %v", got, want)
	}
	if got, want := plan.Env, map[string]string{"GOENV": "on"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Env = %v, want %v", got, want)
	}
	if got, want := plan.Capabilities, []string{"NET_RAW"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Capabilities = %v, want %v", got, want)
	}
	if len(plan.Mounts) != 1 {
		t.Fatalf("Mounts = %v, want one mount", plan.Mounts)
	}
	if plan.Mounts[0].Src != filepath.Join(home, "go") || plan.Mounts[0].Dst != "/home/agent/.go" {
		t.Fatalf("Mount = %+v", plan.Mounts[0])
	}
}

func TestFlattenCapabilitySetupPlans(t *testing.T) {
	dockerArgs, firewallDomains, credStagingDirs := flattenCapabilitySetupPlans([]CapabilitySetupPlan{
		{
			Name:            "aws",
			DockerArgs:      []string{"-v", "/host/aws:/home/agent/.aws:ro"},
			FirewallDomains: []string{"sts.amazonaws.com"},
			CredStagingDirs: []string{"/tmp/aws:.aws"},
		},
		{
			Name:            "gcp",
			DockerArgs:      []string{"-e", "GOOGLE_APPLICATION_CREDENTIALS=/creds"},
			FirewallDomains: []string{"oauth2.googleapis.com"},
			CredStagingDirs: []string{"/tmp/gcp:.config/gcloud"},
		},
	})

	if got, want := dockerArgs, []string{"-v", "/host/aws:/home/agent/.aws:ro", "-e", "GOOGLE_APPLICATION_CREDENTIALS=/creds"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerArgs = %v, want %v", got, want)
	}
	if got, want := firewallDomains, []string{"sts.amazonaws.com", "oauth2.googleapis.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("firewallDomains = %v, want %v", got, want)
	}
	if got, want := credStagingDirs, []string{"/tmp/aws:.aws", "/tmp/gcp:.config/gcloud"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("credStagingDirs = %v, want %v", got, want)
	}
}
