package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
)

func TestWizardDraftCancelAndSectionBackNeverPersist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MITTENS_HOME", home)
	base := defaultProjectPolicy()
	base.Provider.Name = "codex"
	base.MCP.Servers = []MCPServerPolicy{{Name: "private", Mode: mcpModeProxy, CommandPin: "saved-pin"}}
	base.Options["unexposed"] = "keep"
	original, err := clonePolicy(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, cancel := range []bool{false, true} {
		actions := []string{"network", "provider", "save"}
		if cancel {
			actions[2] = "cancel"
		}
		index := 0
		result, err := runWizardEditorWithUI(base, wizardEditorConfig{}, wizardEditorUI{
			Choose: func(d wizardDraft, _ wizardEditorConfig) (string, error) {
				action := actions[index]
				index++
				return action, nil
			},
			Edit: func(section string, d *wizardDraft, _ wizardEditorConfig) error {
				if section == "network" {
					d.Policy.Network.Firewall = "disabled"
					return nil
				}
				d.Policy.Provider.Name = "claude"
				d.Policy.Options["unexposed"] = "corrupted"
				d.TrustCodex = true
				return huh.ErrUserAborted
			},
			Review: func(_ *ProjectPolicy, d wizardDraft, _ wizardEditorConfig, _ bool) (bool, error) {
				if d.Policy.Provider.Name != "codex" || d.Policy.Options["unexposed"] != "keep" || d.TrustCodex {
					t.Fatal("aborted section leaked into draft")
				}
				if !wizardValuesEqual(d.Policy.MCP, base.MCP) {
					t.Fatal("unvisited MCP settings changed")
				}
				return true, nil
			},
		})
		if cancel {
			if !errors.Is(err, huh.ErrUserAborted) || result.Policy != nil {
				t.Fatalf("cancel = %+v, %v", result, err)
			}
		} else if err != nil || result.Policy.Network.Firewall != "disabled" {
			t.Fatalf("draft = %+v, %v", result, err)
		}
		if !wizardValuesEqual(base, original) {
			t.Fatal("original policy mutated")
		}
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 0 {
			t.Fatalf("editor persisted files before caller saved: %v, %v", entries, err)
		}
	}
}

func TestWizardReviewBackRetainsDraft(t *testing.T) {
	actions := []string{"network", "launch", "save"}
	index, reviews := 0, 0
	result, err := runWizardEditorWithUI(defaultProjectPolicy(), wizardEditorConfig{}, wizardEditorUI{
		Choose: func(_ wizardDraft, _ wizardEditorConfig) (string, error) { a := actions[index]; index++; return a, nil },
		Edit: func(_ string, d *wizardDraft, _ wizardEditorConfig) error {
			d.Policy.Network.Firewall = "disabled"
			return nil
		},
		Review: func(_ *ProjectPolicy, d wizardDraft, _ wizardEditorConfig, _ bool) (bool, error) {
			reviews++
			if d.Policy.Network.Firewall != "disabled" {
				t.Fatal("draft lost when returning from review")
			}
			return reviews > 1, nil
		},
	})
	if err != nil || reviews != 2 || result.Launch {
		t.Fatalf("result = %+v, reviews = %d, err = %v", result, reviews, err)
	}
}

func TestWizardResetDropsOldBoundaryAndPendingSideEffects(t *testing.T) {
	base := defaultProjectPolicy()
	base.Host.OpenURLs = "deny"
	base.Credentials.ProviderOAuth = false
	base.Workspace.Mounts = []PolicyMount{{Path: "/old", Access: "rw"}}
	actions := []string{"network", "reset", "save"}
	index := 0
	result, err := runWizardEditorWithUI(base, wizardEditorConfig{}, wizardEditorUI{
		Choose: func(_ wizardDraft, _ wizardEditorConfig) (string, error) { a := actions[index]; index++; return a, nil },
		Edit: func(_ string, d *wizardDraft, _ wizardEditorConfig) error {
			d.Learn, d.TrustCodex = true, true
			return nil
		},
		Reset:  func(wizardEditorConfig) (*ProjectPolicy, error) { return defaultProjectPolicy(), nil },
		Review: func(_ *ProjectPolicy, _ wizardDraft, _ wizardEditorConfig, _ bool) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Learn || result.TrustCodex || !wizardValuesEqual(result.Policy, defaultProjectPolicy()) {
		t.Fatalf("reset retained old state: %+v", result)
	}
}

func TestWizardInitialPolicyUsesDefaultsWithoutWriting(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := t.TempDir()
	policy, err := wizardInitialPolicy(workspace, "default", nil)
	if err != nil || !wizardValuesEqual(policy, defaultProjectPolicy()) {
		t.Fatalf("recommended seed: %+v, %v", policy, err)
	}
	if UserDefaultsExist() {
		t.Fatal("first run unexpectedly created user defaults")
	}
	defaults := defaultProjectPolicy()
	defaults.Provider.Name = "codex"
	defaults.Host.OpenURLs = "deny"
	defaults.Network.SSHEgress = boolPtr(false)
	if err := SaveUserDefaultsPolicy(defaults); err != nil {
		t.Fatal(err)
	}
	policy, err = wizardInitialPolicy(workspace, "default", nil)
	if err != nil || !wizardValuesEqual(policy, defaults) {
		t.Fatalf("user seed: %+v, %v", policy, err)
	}
	if _, err := os.Stat(projectPolicyPath(workspace)); !os.IsNotExist(err) {
		t.Fatalf("seed persisted project: %v", err)
	}
}

func TestSaveWizardNamedProjectKeepsDefaultAndUserDefaults(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := t.TempDir()
	base := defaultProjectPolicy()
	if err := SaveProjectPolicy(workspace, base); err != nil {
		t.Fatal(err)
	}
	changed, err := clonePolicy(base)
	if err != nil {
		t.Fatal(err)
	}
	changed.Provider.Name = "codex"
	if err := saveWizardProject(workspace, "review", wizardDraft{Policy: changed}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadNamedProfile(workspace, "review")
	if err != nil || got.Provider.Name != "codex" {
		t.Fatalf("named result: %+v, %v", got, err)
	}
	got, err = LoadNamedProfile(workspace, "default")
	if err != nil || got.Provider.Name != base.Provider.Name || UserDefaultsExist() {
		t.Fatalf("unselected save targets changed: %+v, %v", got, err)
	}
}

func TestWizardPolicyChangesShowsDirectoryAccessChange(t *testing.T) {
	before, after := defaultProjectPolicy(), defaultProjectPolicy()
	before.Workspace.Mounts = []PolicyMount{{Path: "/shared", Access: "ro"}}
	after.Workspace.Mounts = []PolicyMount{{Path: "/shared", Access: "rw"}}
	text := strings.Join(wizardPolicyChanges(before, after), "\n")
	if !strings.Contains(text, "Remove directory: /shared (ro)") || !strings.Contains(text, "Add directory: /shared (rw)") {
		t.Fatal(text)
	}
}

func TestWizardLaunchUnchangedConfigurationDoesNotRequireSave(t *testing.T) {
	result, err := runWizardEditorWithUI(defaultProjectPolicy(), wizardEditorConfig{Mode: "project"}, wizardEditorUI{
		Choose: func(_ wizardDraft, cfg wizardEditorConfig) (string, error) {
			if !cfg.Unchanged {
				t.Fatal("unchanged draft marked changed")
			}
			return "launch-existing", nil
		},
	})
	if err != nil || !result.SkipSave || !result.Launch {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestWizardPolicyChangesDescribesNetworkAndSelections(t *testing.T) {
	before, after := defaultProjectPolicy(), defaultProjectPolicy()
	after.Network.Firewall = "disabled"
	after.Capabilities = []CapabilityPolicy{{Name: "kubectl", Args: []string{"staging"}}}
	after.MCP.Servers = []MCPServerPolicy{{Name: "files", Mode: mcpModeMount}}
	out := strings.Join(wizardPolicyChanges(before, after), "\n")
	for _, want := range []string{"firewall allowlist → docker bridge, firewall disabled", "Add extension: kubectl: staging", "Add MCP server: files (mount)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
}
