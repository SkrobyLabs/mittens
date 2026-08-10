package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedProfilesRejectStrictMalformedCollection(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/strict-profiles"
	path := profilesPolicyPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"version: 1\nprofiles:\n  planner:\n    provider:\n      name: codex\n      nam: claude\n",
		"version: 1\nprofiles:\n  planner:\n    provider:\n      name: codex\n      name: claude\n",
		"version: 1\nprofiles:\n  default:\n    provider:\n      name: codex\n",
		"version: 1\nprofiles: {}\n---\nversion: 1\nprofiles: {}\n",
	} {
		if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadNamedProfiles(workspace); err == nil {
			t.Fatalf("LoadNamedProfiles accepted malformed collection:\n%s", input)
		}
	}
}

func TestNamedProfilesRoundTripAsStandaloneSnapshots(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/profile-snapshots"
	defaultPolicy := defaultProjectPolicy()
	defaultPolicy.Provider.Name = "claude"
	if err := SaveProjectPolicy(workspace, defaultPolicy); err != nil {
		t.Fatal(err)
	}
	named, err := clonePolicy(defaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	named.Provider.Name = "codex"
	named.Provider.Model = "gpt-5"
	named.Network.Firewall = "disabled"
	if err := SaveNamedProfile(workspace, "planner", named); err != nil {
		t.Fatal(err)
	}
	defaultPolicy.Provider.Name = "gemini"
	if err := SaveProjectPolicy(workspace, defaultPolicy); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadNamedProfile(workspace, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider.Name != "codex" || loaded.Provider.Model != "gpt-5" || loaded.Network.Firewall != "disabled" {
		t.Fatalf("named snapshot was merged or changed: %#v", loaded)
	}
	if err := SaveNamedProfile(workspace, "default", named); err != nil {
		t.Fatal(err)
	}
	if err := DeleteNamedProfile(workspace, "default"); err == nil {
		t.Fatal("expected reserved default deletion to fail")
	}
}

func TestRunProfileListIsSortedAndReadOnly(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/profile-list"
	defaultPolicy := defaultProjectPolicy()
	defaultPolicy.Provider.Name = "claude"
	if err := SaveProjectPolicy(workspace, defaultPolicy); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zebra", "alpha"} {
		policy, err := clonePolicy(defaultPolicy)
		if err != nil {
			t.Fatal(err)
		}
		policy.Provider.Name = "codex"
		policy.Provider.Model = name + "-model"
		policy.Provider.Effort = "high"
		if err := SaveNamedProfile(workspace, name, policy); err != nil {
			t.Fatal(err)
		}
	}
	path := profilesPolicyPath(workspace)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdoutFor(t, func() error { return runProfileList(workspace) })
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("profile list rewrote profiles.yaml")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "default\tprovider=claude") || !strings.HasPrefix(lines[1], "alpha\tprovider=codex\tmodel=alpha-model\teffort=high") || !strings.HasPrefix(lines[2], "zebra\tprovider=codex") {
		t.Fatalf("unexpected profile list output: %q", out)
	}
}

func TestResolveLegacyProviderProfileDistinguishesPersistentSource(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/legacy-reference"
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"codex": {"fast": {Model: "gpt-5", Effort: "high"}},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, persist := range []bool{false, true} {
		policy := defaultProjectPolicy()
		policy.Provider.Name = "codex"
		policy.Provider.Profile = "fast"
		changed, err := resolveLegacyProviderProfile(workspace, policy, persist)
		if err != nil {
			t.Fatal(err)
		}
		if changed != persist || policy.Provider.Profile != "" || policy.Provider.Model != "gpt-5" || policy.Provider.Effort != "high" {
			t.Fatalf("persist=%t resolution = changed:%t policy:%+v", persist, changed, policy.Provider)
		}
	}
	missing := defaultProjectPolicy()
	missing.Provider.Name, missing.Provider.Profile = "codex", "missing"
	if _, err := resolveLegacyProviderProfile(workspace, missing, false); err == nil {
		t.Fatal("expected missing legacy preset to fail closed")
	}
}

func TestMigrateLegacyProfilesClearsDefaultReferenceBeforeSnapshot(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/legacy-default-reference"
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"codex": {"fast": {Model: "gpt-5", Effort: "high"}},
	}}); err != nil {
		t.Fatal(err)
	}
	defaultPolicy := defaultProjectPolicy()
	defaultPolicy.Provider.Name = "codex"
	defaultPolicy.Provider.Profile = "fast"
	if err := SaveProjectPolicy(workspace, defaultPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyProfiles(workspace, defaultPolicy); err != nil {
		t.Fatal(err)
	}
	loadedDefault, err := LoadNamedProfile(workspace, "default")
	if err != nil {
		t.Fatal(err)
	}
	if loadedDefault.Provider.Profile != "" || loadedDefault.Provider.Model != "gpt-5" {
		t.Fatalf("default was not persistently resolved: %+v", loadedDefault.Provider)
	}
	converted, err := LoadNamedProfile(workspace, "fast")
	if err != nil {
		t.Fatal(err)
	}
	if converted.Provider.Profile != "" {
		t.Fatalf("converted profile retained legacy reference: %+v", converted.Provider)
	}
}

func TestMigrateLegacyProfilesCollisionIsIdempotent(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/legacy-profiles"
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {"fast": {Model: "sonnet", Effort: "high"}},
		"codex":  {"fast": {Model: "gpt", Effort: "medium"}},
	}}); err != nil {
		t.Fatal(err)
	}
	notices, err := MigrateLegacyProfiles(workspace, defaultProjectPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(notices, ","), "claude/fast -> claude-fast,codex/fast -> codex-fast"; got != want {
		t.Fatalf("migration notices = %q, want %q", got, want)
	}
	before, err := os.ReadFile(profilesPolicyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyProfiles(workspace, defaultProjectPolicy()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(profilesPolicyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("repeat migration changed profiles.yaml:\n%s", after)
	}
}

func TestMigrateLegacyProfilesFailsBeforePartialWriteWhenEscapedTargetOccupied(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/legacy-occupied"
	escaped := "legacy-v1--Y29kZXg--YmFkL25hbWU" // codex / bad/name
	if err := SaveNamedProfiles(workspace, &NamedProfileConfig{Profiles: map[string]*ProjectPolicy{
		escaped: defaultProjectPolicy(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"codex": {"bad/name": {Model: "gpt-5"}, "safe": {Model: "gpt-4"}},
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(profilesPolicyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyProfiles(workspace, defaultProjectPolicy()); err == nil {
		t.Fatal("expected occupied escaped target to fail")
	}
	after, err := os.ReadFile(profilesPolicyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed migration partially changed profiles.yaml")
	}
}

func TestMigrateLegacyProfilesReservesBareNamesBeforeQualifiedTargets(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/legacy-second-order"
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {"fast": {Model: "sonnet"}},
		"codex":  {"fast": {Model: "gpt-5"}},
		"other":  {"codex-fast": {Model: "other-model"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyProfiles(workspace, defaultProjectPolicy()); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadNamedProfiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := profiles.Profiles["codex-fast"]; got == nil || got.Provider.Name != "other" {
		t.Fatalf("bare target was not reserved: %#v", got)
	}
	escaped := legacyEscapedProfileName("codex", "fast")
	if got := profiles.Profiles[escaped]; got == nil || got.Provider.Name != "codex" {
		t.Fatalf("second-order collision did not use escaped target %q: %#v", escaped, got)
	}
	before, err := os.ReadFile(profilesPolicyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyProfiles(workspace, defaultProjectPolicy()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(profilesPolicyPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rerun did not recognize canonical second-order targets")
	}
}

func TestMigrateLegacyProfilesDoesNotRecognizeBoundaryDifferentSnapshot(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := "/repo/legacy-boundary-mismatch"
	authoritative := defaultProjectPolicy()
	authoritative.Provider.Name = "codex"
	authoritative.Provider.Model = "gpt-5"
	authoritative.Network.Firewall = "disabled" // differs from the seed
	if err := SaveNamedProfiles(workspace, &NamedProfileConfig{Profiles: map[string]*ProjectPolicy{
		"fast": authoritative,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"codex": {"fast": {Model: "gpt-5"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacyProfiles(workspace, defaultProjectPolicy()); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadNamedProfiles(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := profiles.Profiles["fast"]; got == nil || got.Network.Firewall != "disabled" {
		t.Fatalf("authoritative differing snapshot was replaced: %#v", got)
	}
	if got := profiles.Profiles["codex-fast"]; got == nil || got.Network.Firewall != "strict" {
		t.Fatalf("boundary-different preset was not migrated to an unoccupied target: %#v", got)
	}
}
