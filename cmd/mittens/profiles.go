package main

// Complete, project-local launch profiles.  policy.yaml deliberately remains
// the default profile for compatibility; profiles.yaml holds only non-default
// snapshots.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const namedProfileVersion = 1

type NamedProfileConfig struct {
	Version  int                       `yaml:"version"`
	Profiles map[string]*ProjectPolicy `yaml:"profiles"`
}

var profileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func profilesPolicyPath(workspace string) string {
	return filepath.Join(ConfigHome(), "projects", ProjectDir(workspace), "profiles.yaml")
}

func validateProfileName(name string, allowDefault bool) error {
	if name == "default" && allowDefault {
		return nil
	}
	if name == "default" {
		return fmt.Errorf("profile name %q is reserved", name)
	}
	if !profileNameRE.MatchString(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	return nil
}

func LoadNamedProfiles(workspace string) (*NamedProfileConfig, error) {
	return loadNamedProfilesPath(profilesPolicyPath(workspace))
}

func loadNamedProfilesPath(path string) (*NamedProfileConfig, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &NamedProfileConfig{Version: namedProfileVersion, Profiles: map[string]*ProjectPolicy{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading profiles %s: %w", path, err)
	}
	var nodes []*yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var n yaml.Node
		err := d.Decode(&n)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("parsing profiles %s: %w", path, err)
		}
		nodes = append(nodes, &n)
	}
	if len(nodes) != 1 {
		return nil, fmt.Errorf("profiles %s must contain exactly one YAML document", path)
	}
	if err := rejectDuplicateYAMLKeys(nodes[0]); err != nil {
		return nil, fmt.Errorf("parsing profiles %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var cfg NamedProfileConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing profiles %s: %w", path, err)
	}
	if cfg.Version != namedProfileVersion {
		return nil, fmt.Errorf("unsupported profiles version %d", cfg.Version)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*ProjectPolicy{}
	}
	for name, policy := range cfg.Profiles {
		if err := validateProfileName(name, false); err != nil {
			return nil, err
		}
		if policy == nil {
			return nil, fmt.Errorf("profile %q is nil", name)
		}
		policy.applyDefaults()
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
	}
	return &cfg, nil
}

func rejectDuplicateYAMLKeys(n *yaml.Node) error {
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			if err := rejectDuplicateYAMLKeys(c); err != nil {
				return err
			}
		}
		return nil
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return fmt.Errorf("duplicate key %q", k)
			}
			seen[k] = true
			if err := rejectDuplicateYAMLKeys(n.Content[i+1]); err != nil {
				return err
			}
		}
	}
	for _, c := range n.Content {
		if err := rejectDuplicateYAMLKeys(c); err != nil {
			return err
		}
	}
	return nil
}

func SaveNamedProfiles(workspace string, cfg *NamedProfileConfig) error {
	return saveNamedProfilesPath(profilesPolicyPath(workspace), cfg)
}

func saveNamedProfilesPath(path string, cfg *NamedProfileConfig) error {
	if cfg == nil {
		return fmt.Errorf("profiles are nil")
	}
	cfg.Version = namedProfileVersion
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*ProjectPolicy{}
	}
	for n, p := range cfg.Profiles {
		if err := validateProfileName(n, false); err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("profile %q is nil", n)
		}
		p.applyDefaults()
		if err := p.Validate(); err != nil {
			return fmt.Errorf("profile %q: %w", n, err)
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profiles-*.yaml.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func LoadNamedProfile(workspace, name string) (*ProjectPolicy, error) {
	if err := validateProfileName(name, true); err != nil {
		return nil, err
	}
	if name == "default" {
		p, err := loadProjectPolicyFile(workspace)
		return p, err
	}
	cfg, err := LoadNamedProfiles(workspace)
	if err != nil {
		return nil, err
	}
	p := cfg.Profiles[name]
	if p == nil {
		return nil, fmt.Errorf("profile %q not found; create it with `mittens init --profile %s`", name, name)
	}
	return clonePolicy(p)
}

func SaveNamedProfile(workspace, name string, p *ProjectPolicy) error {
	if err := validateProfileName(name, true); err != nil {
		return err
	}
	if name == "default" {
		return SaveProjectPolicy(workspace, p)
	}
	cfg, err := LoadNamedProfiles(workspace)
	if err != nil {
		return err
	}
	cfg.Profiles[name], err = clonePolicy(p)
	if err != nil {
		return err
	}
	return SaveNamedProfiles(workspace, cfg)
}

func DeleteNamedProfile(workspace, name string) error {
	if name == "default" {
		return fmt.Errorf("the reserved default profile cannot be deleted")
	}
	if err := validateProfileName(name, false); err != nil {
		return err
	}
	cfg, err := LoadNamedProfiles(workspace)
	if err != nil {
		return err
	}
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	delete(cfg.Profiles, name)
	return SaveNamedProfiles(workspace, cfg)
}

// runProfile handles read-only commands for complete project profiles.
func runProfile(args []string) error {
	if len(args) != 1 || args[0] != "list" {
		return fmt.Errorf("usage: mittens profile list")
	}
	return runProfileList(detectWorkspace())
}

// runProfileList prints the default profile followed by complete named
// snapshots in deterministic order. It intentionally performs no migration or
// write, so malformed profile collections are surfaced unchanged.
func runProfileList(workspace string) error {
	defaultPolicy, err := loadProjectPolicyFile(workspace)
	if err != nil {
		return err
	}
	if defaultPolicy == nil {
		defaultPolicy = defaultProjectPolicy()
	}
	profiles, err := LoadNamedProfiles(workspace)
	if err != nil {
		return err
	}

	printProfileSummary("default", defaultPolicy)
	names := make([]string, 0, len(profiles.Profiles))
	for name := range profiles.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		printProfileSummary(name, profiles.Profiles[name])
	}
	return nil
}

func printProfileSummary(name string, policy *ProjectPolicy) {
	parts := []string{name}
	if policy.Provider.Name != "" {
		parts = append(parts, "provider="+policy.Provider.Name)
	}
	if policy.Provider.Model != "" {
		parts = append(parts, "model="+policy.Provider.Model)
	}
	if policy.Provider.Effort != "" {
		parts = append(parts, "effort="+policy.Provider.Effort)
	}
	fmt.Println(strings.Join(parts, "\t"))
}

func clonePolicy(p *ProjectPolicy) (*ProjectPolicy, error) {
	b, err := yaml.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out ProjectPolicy
	if err = yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type legacyProfileMigration struct {
	Provider string
	Name     string
	Target   string
	Kind     string // bare, qualified, escaped, or existing
}

// MigrateLegacyProfiles converts the old provider-keyed preset maps to complete
// snapshots. Existing YAML entries win; the old JSON remains recoverable.
func MigrateLegacyProfiles(workspace string, seed *ProjectPolicy) ([]string, error) {
	// The writable project default is the sole legacy-reference source that
	// may be rewritten. Resolve it before using it as a conversion snapshot so
	// converted names never keep provider.profile outward references.
	if defaultPolicy, err := loadProjectPolicyFile(workspace); err != nil {
		return nil, err
	} else if defaultPolicy != nil {
		changed, err := resolveLegacyProviderProfile(workspace, defaultPolicy, true)
		if err != nil {
			return nil, err
		}
		if changed {
			if err := SaveProjectPolicy(workspace, defaultPolicy); err != nil {
				return nil, err
			}
		}
		seed = defaultPolicy
	}
	migrations, err := migrateLegacyProfilesInDir(filepath.Join(ConfigHome(), "projects", ProjectDir(workspace)), seed)
	if err != nil {
		return nil, err
	}
	notices := make([]string, 0, len(migrations))
	for _, migration := range migrations {
		notices = append(notices, fmt.Sprintf("%s/%s -> %s", migration.Provider, migration.Name, migration.Target))
	}
	return notices, nil
}

// migrateLegacyProfilesInDir supports doctor --migrate-all without guessing a
// workspace path from its stored directory name.
func migrateLegacyProfilesInDir(dir string, seed *ProjectPolicy) ([]legacyProfileMigration, error) {
	cfg, err := loadNamedProfilesPath(filepath.Join(dir, "profiles.yaml"))
	if err != nil {
		return nil, err
	}
	legacy, err := loadProfileConfigFromDir(dir)
	if err != nil {
		return nil, err
	}
	if seed == nil {
		seed = defaultProjectPolicy()
	} else {
		var err error
		seed, err = clonePolicy(seed)
		if err != nil {
			return nil, err
		}
	}
	// A converted complete profile must not retain an outward legacy-preset
	// reference. Resolve the source snapshot before cloning it, using the same
	// local legacy store migration reads; callers persist the writable default
	// separately when appropriate.
	if _, err := resolveLegacyProviderProfileInDir(dir, seed); err != nil {
		return nil, err
	}
	type entry struct {
		provider, name, target, kind string
		preset                       ProfilePreset
		expected                     *ProjectPolicy
	}
	var entries []entry
	counts := map[string]int{}
	for provider, names := range legacy.Profiles {
		for name, preset := range names {
			entries = append(entries, entry{provider: provider, name: name, preset: preset})
			counts[name]++
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].provider == entries[j].provider {
			return entries[i].name < entries[j].name
		}
		return entries[i].provider < entries[j].provider
	})
	for i := range entries {
		e := &entries[i]
		expected, err := legacyProfileSnapshot(seed, e.provider, e.preset)
		if err != nil {
			return nil, err
		}
		e.expected = expected
	}

	// Compute the total mapping before changing the collection. Existing YAML
	// entries are authoritative. Allocation proceeds in reservation passes so a
	// bare source cannot be displaced by another source's qualified target.
	used := map[string]bool{}
	for name := range cfg.Profiles {
		used[name] = true
	}
	// First recognize one previous canonical target per source. This makes
	// reruns stable even when a target was qualified or escaped by an earlier
	// second-order collision.
	for i := range entries {
		e := &entries[i]
		for _, candidate := range legacyMigrationCandidates(e.provider, e.name) {
			if existing := cfg.Profiles[candidate.target]; legacyMigrationMatches(existing, e.expected) {
				e.target, e.kind = candidate.target, "existing"
				break
			}
		}
	}
	// Pass 1: reserve valid globally-unique bare names.
	for i := range entries {
		e := &entries[i]
		if e.kind != "" || counts[e.name] != 1 || validateProfileName(e.name, false) != nil || used[e.name] {
			continue
		}
		e.target, e.kind = e.name, "bare"
		used[e.target] = true
	}
	// Pass 2: reserve provider-qualified fallbacks after all bare names are
	// known, preventing first- and second-order generated-name collisions.
	for i := range entries {
		e := &entries[i]
		candidate := e.provider + "-" + e.name
		if e.kind != "" || validateProfileName(candidate, false) != nil || used[candidate] {
			continue
		}
		e.target, e.kind = candidate, "qualified"
		used[e.target] = true
	}
	// Pass 3: allocate the injective escaped form for every remaining source.
	for i := range entries {
		e := &entries[i]
		if e.kind != "" {
			continue
		}
		candidate := legacyEscapedProfileName(e.provider, e.name)
		if validateProfileName(candidate, false) != nil || used[candidate] {
			return nil, fmt.Errorf("cannot safely migrate legacy profile %s/%s: no unoccupied injective name", e.provider, e.name)
		}
		e.target, e.kind = candidate, "escaped"
		used[e.target] = true
	}

	for _, e := range entries {
		if e.kind == "existing" {
			continue
		}
		p, err := clonePolicy(e.expected)
		if err != nil {
			return nil, err
		}
		cfg.Profiles[e.target] = p
	}
	changed := false
	for _, e := range entries {
		if e.kind != "existing" {
			changed = true
			break
		}
	}
	if changed {
		if err := saveNamedProfilesPath(filepath.Join(dir, "profiles.yaml"), cfg); err != nil {
			return nil, err
		}
	}
	migrations := make([]legacyProfileMigration, 0, len(entries))
	for _, e := range entries {
		migrations = append(migrations, legacyProfileMigration{Provider: e.provider, Name: e.name, Target: e.target, Kind: e.kind})
	}
	return migrations, nil
}

type legacyMigrationCandidate struct{ target string }

func legacyMigrationCandidates(provider, name string) []legacyMigrationCandidate {
	return []legacyMigrationCandidate{
		{target: name},
		{target: provider + "-" + name},
		{target: legacyEscapedProfileName(provider, name)},
	}
}

func legacyEscapedProfileName(provider, name string) string {
	return "legacy-v1--" + base64.RawURLEncoding.EncodeToString([]byte(provider)) + "--" + base64.RawURLEncoding.EncodeToString([]byte(name))
}

func legacyProfileSnapshot(seed *ProjectPolicy, provider string, preset ProfilePreset) (*ProjectPolicy, error) {
	p, err := clonePolicy(seed)
	if err != nil {
		return nil, err
	}
	if p.Provider.Name != provider {
		p.Provider.Backend = ""
		p.Provider.Endpoint = ""
	}
	p.Provider.Name = provider
	p.Provider.Model = preset.Model
	p.Provider.Effort = preset.Effort
	p.Provider.Profile = ""
	p.applyDefaults()
	return p, nil
}

func legacyMigrationMatches(policy, expected *ProjectPolicy) bool {
	return policy != nil && expected != nil && reflect.DeepEqual(policy, expected)
}
