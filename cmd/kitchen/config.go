package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Complexity string

const (
	ComplexityTrivial  Complexity = "trivial"
	ComplexityLow      Complexity = "low"
	ComplexityMedium   Complexity = "medium"
	ComplexityHigh     Complexity = "high"
	ComplexityCritical Complexity = "critical"
)

var allComplexities = []Complexity{
	ComplexityTrivial,
	ComplexityLow,
	ComplexityMedium,
	ComplexityHigh,
	ComplexityCritical,
}

type PoolKey struct {
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
	Adapter  string `json:"adapter,omitempty" yaml:"adapter,omitempty"`
}

type RoutingRule struct {
	Prefer   []PoolKey `json:"prefer,omitempty" yaml:"prefer,omitempty"`
	Fallback []PoolKey `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

const defaultRoutingRole = "default"

type ConcurrencyConfig struct {
	MaxActiveLineages    int `json:"maxActiveLineages" yaml:"maxActiveLineages"`
	MaxPlanningWorkers   int `json:"maxPlanningWorkers" yaml:"maxPlanningWorkers"`
	MaxWorkersTotal      int `json:"maxWorkersTotal" yaml:"maxWorkersTotal"`
	MaxWorkersPerPool    int `json:"maxWorkersPerPool" yaml:"maxWorkersPerPool"`
	MaxWorkersPerLineage int `json:"maxWorkersPerLineage" yaml:"maxWorkersPerLineage"`
	MaxIdlePerPool       int `json:"maxIdlePerPool" yaml:"maxIdlePerPool"`
	CouncilSeatIdleTTLSeconds int `json:"councilSeatIdleTTLSeconds" yaml:"councilSeatIdleTTLSeconds"`
}

type FailurePolicyRule struct {
	Action   string `json:"action" yaml:"action"`
	Max      int    `json:"max,omitempty" yaml:"max,omitempty"`
	Cooldown string `json:"cooldown,omitempty" yaml:"cooldown,omitempty"`
}

type SnapshotConfig struct {
	PlanHistoryLimit int `json:"planHistoryLimit" yaml:"planHistoryLimit"`
}

type KitchenConfig struct {
	Routing       map[Complexity]RoutingRule            `json:"routing" yaml:"routing"`
	RoleDefaults  map[string]RoutingRule                `json:"roleDefaults,omitempty" yaml:"roleDefaults,omitempty"`
	RoleRouting   map[string]map[Complexity]RoutingRule `json:"roleRouting,omitempty" yaml:"roleRouting,omitempty"`
	Concurrency   ConcurrencyConfig                     `json:"concurrency" yaml:"concurrency"`
	FailurePolicy map[string]FailurePolicyRule          `json:"failure_policy" yaml:"failure_policy"`
	Snapshots     SnapshotConfig                        `json:"snapshots" yaml:"snapshots"`
}

type KitchenPaths struct {
	HomeDir      string
	ConfigPath   string
	StateDir     string
	ProjectsDir  string
	WorktreesDir string
}

type ProjectPaths struct {
	Key         string
	RootDir     string
	PlansDir    string
	LineagesDir string
	PoolsDir    string
}

func DefaultKitchenConfig() KitchenConfig {
	return KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityTrivial: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "haiku"}},
			},
			ComplexityLow: {
				Prefer:   []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
				Fallback: []PoolKey{{Provider: "gemini", Model: "gemini-2.0-flash"}},
			},
			ComplexityMedium: {
				Prefer:   []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
				Fallback: []PoolKey{{Provider: "gemini", Model: "gemini-2.0-flash"}, {Provider: "anthropic", Model: "opus"}},
			},
			ComplexityHigh: {
				Prefer:   []PoolKey{{Provider: "anthropic", Model: "opus"}},
				Fallback: []PoolKey{{Provider: "gemini", Model: "gemini-2.5-pro"}},
			},
			ComplexityCritical: {
				Prefer:   []PoolKey{{Provider: "anthropic", Model: "opus"}},
				Fallback: []PoolKey{{Provider: "gemini", Model: "gemini-2.5-pro"}},
			},
		},
		RoleDefaults: map[string]RoutingRule{},
		RoleRouting:  map[string]map[Complexity]RoutingRule{},
		Concurrency: ConcurrencyConfig{
			MaxActiveLineages:    5,
			MaxPlanningWorkers:   2,
			MaxWorkersTotal:      12,
			MaxWorkersPerPool:    6,
			MaxWorkersPerLineage: 4,
			MaxIdlePerPool:       2,
			CouncilSeatIdleTTLSeconds: 270,
		},
		FailurePolicy: map[string]FailurePolicyRule{
			"capability":     {Action: "escalate_complexity", Max: 2},
			"plan":           {Action: "replan", Max: 1},
			"environment":    {Action: "retry", Max: 2},
			"conflict":       {Action: "retry_merge", Max: 1},
			"auth":           {Action: "try_next_provider", Cooldown: "60s"},
			"infrastructure": {Action: "respawn", Max: 1},
		},
		Snapshots: SnapshotConfig{
			PlanHistoryLimit: defaultPlanProgressHistoryLimit,
		},
	}
}

func configurableKitchenRoles() []string {
	return []string{
		defaultRoutingRole,
		plannerTaskRole,
		"implementer",
		"reviewer",
		lineageFixMergeRole,
	}
}

func effectiveRoutingForRole(cfg KitchenConfig, role string) map[Complexity]RoutingRule {
	role = normalizeRoutingRole(role)
	routing := cloneRoutingMap(cfg.Routing)
	if role == defaultRoutingRole {
		return routing
	}
	if rule, ok := roleDefaultRule(cfg, role); ok {
		for _, complexity := range allComplexities {
			routing[complexity] = cloneRoutingRule(rule)
		}
	}
	roleRules := cfg.RoleRouting[role]
	for complexity, rule := range roleRules {
		if len(rule.Prefer) > 0 {
			routing[complexity] = cloneRoutingRule(rule)
		}
	}
	return routing
}

func normalizeRoutingRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return defaultRoutingRole
	}
	return role
}

func cloneRoutingMap(src map[Complexity]RoutingRule) map[Complexity]RoutingRule {
	out := make(map[Complexity]RoutingRule, len(src))
	for complexity, rule := range src {
		out[complexity] = cloneRoutingRule(rule)
	}
	return out
}

func cloneRoutingRule(rule RoutingRule) RoutingRule {
	return RoutingRule{
		Prefer:   append([]PoolKey(nil), rule.Prefer...),
		Fallback: append([]PoolKey(nil), rule.Fallback...),
	}
}

func roleDefaultRule(cfg KitchenConfig, role string) (RoutingRule, bool) {
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return RoutingRule{}, false
	}
	rule, ok := cfg.RoleDefaults[role]
	if !ok || len(rule.Prefer) == 0 {
		return RoutingRule{}, false
	}
	return cloneRoutingRule(rule), true
}

func setRoutingForRole(cfg *KitchenConfig, role string, routing map[Complexity]RoutingRule) {
	if cfg == nil {
		return
	}
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		cfg.Routing = cloneRoutingMap(routing)
		return
	}
	if cfg.RoleRouting == nil {
		cfg.RoleRouting = make(map[string]map[Complexity]RoutingRule)
	}
	if cfg.RoleDefaults == nil {
		cfg.RoleDefaults = make(map[string]RoutingRule)
	}
	overrides := make(map[Complexity]RoutingRule)
	for _, complexity := range allComplexities {
		rule, ok := routing[complexity]
		if !ok || len(rule.Prefer) == 0 {
			continue
		}
		if routingRuleEqual(rule, cfg.Routing[complexity]) {
			continue
		}
		overrides[complexity] = cloneRoutingRule(rule)
	}
	if len(overrides) == 0 {
		delete(cfg.RoleRouting, role)
		return
	}
	cfg.RoleRouting[role] = overrides
}

func setRoleDefault(cfg *KitchenConfig, role string, rule RoutingRule) {
	if cfg == nil {
		return
	}
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return
	}
	if cfg.RoleDefaults == nil {
		cfg.RoleDefaults = make(map[string]RoutingRule)
	}
	if len(rule.Prefer) == 0 {
		delete(cfg.RoleDefaults, role)
		return
	}
	cfg.RoleDefaults[role] = cloneRoutingRule(rule)
}

func setRoleComplexityOverrides(cfg *KitchenConfig, role string, overrides map[Complexity]RoutingRule) {
	if cfg == nil {
		return
	}
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return
	}
	if cfg.RoleRouting == nil {
		cfg.RoleRouting = make(map[string]map[Complexity]RoutingRule)
	}
	if len(overrides) == 0 {
		delete(cfg.RoleRouting, role)
		return
	}
	cfg.RoleRouting[role] = cloneRoutingMap(overrides)
}

func clearRoutingForRole(cfg *KitchenConfig, role string) {
	if cfg == nil {
		return
	}
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return
	}
	if cfg.RoleRouting != nil {
		delete(cfg.RoleRouting, role)
	}
	if cfg.RoleDefaults != nil {
		delete(cfg.RoleDefaults, role)
	}
}

func roleHasRoutingOverride(cfg KitchenConfig, role string) bool {
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return true
	}
	return len(cfg.RoleRouting[role]) > 0 || len(cfg.RoleDefaults[role].Prefer) > 0
}

func routingRuleEqual(a, b RoutingRule) bool {
	if len(a.Prefer) != len(b.Prefer) || len(a.Fallback) != len(b.Fallback) {
		return false
	}
	for i := range a.Prefer {
		if a.Prefer[i] != b.Prefer[i] {
			return false
		}
	}
	for i := range a.Fallback {
		if a.Fallback[i] != b.Fallback[i] {
			return false
		}
	}
	return true
}

func DefaultKitchenHome() (string, error) {
	if home := strings.TrimSpace(os.Getenv("KITCHEN_HOME")); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".kitchen"), nil
}

func DefaultKitchenPaths() (KitchenPaths, error) {
	home, err := DefaultKitchenHome()
	if err != nil {
		return KitchenPaths{}, err
	}
	return KitchenPaths{
		HomeDir:      home,
		ConfigPath:   filepath.Join(home, "config.yaml"),
		StateDir:     filepath.Join(home, "state"),
		ProjectsDir:  filepath.Join(home, "projects"),
		WorktreesDir: filepath.Join(home, "worktrees"),
	}, nil
}

func (p KitchenPaths) Ensure() error {
	for _, dir := range []string{p.HomeDir, p.StateDir, p.ProjectsDir, p.WorktreesDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func (p KitchenPaths) Project(repoPath string) (ProjectPaths, error) {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return ProjectPaths{}, fmt.Errorf("resolve repo path: %w", err)
	}
	key := projectKey(absRepo)
	root := filepath.Join(p.ProjectsDir, key)
	return ProjectPaths{
		Key:         key,
		RootDir:     root,
		PlansDir:    filepath.Join(root, "plans"),
		LineagesDir: filepath.Join(root, "lineages"),
		PoolsDir:    filepath.Join(root, "pools"),
	}, nil
}

func (p ProjectPaths) Ensure() error {
	for _, dir := range []string{p.RootDir, p.PlansDir, p.LineagesDir, p.PoolsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// KitchenConfigPath returns the path to the user's Kitchen config file.
func KitchenConfigPath() (string, error) {
	home, err := DefaultKitchenHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "config.yaml"), nil
}

// LoadKitchenConfigFile reads the user config at path without merging defaults.
// Returns nil (not an error) when the file does not exist.
func LoadKitchenConfigFile(path string) (*KitchenConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg KitchenConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// MergeKitchenConfig applies user overrides on top of base.
// Routing rules are merged per complexity key; keys absent in user retain base values.
// Concurrency limits are overridden only when the user value is positive.
// Failure policy rules are merged per class; snapshot limit overridden when positive.
func MergeKitchenConfig(base KitchenConfig, user KitchenConfig) KitchenConfig {
	merged := base

	// Deep-copy routing map before mutating.
	merged.Routing = cloneRoutingMap(base.Routing)
	for c, rule := range user.Routing {
		if len(rule.Prefer) > 0 {
			merged.Routing[c] = cloneRoutingRule(rule)
		}
	}

	merged.RoleDefaults = make(map[string]RoutingRule, len(base.RoleDefaults))
	for role, rule := range base.RoleDefaults {
		merged.RoleDefaults[role] = cloneRoutingRule(rule)
	}
	for role, rule := range user.RoleDefaults {
		role = normalizeRoutingRole(role)
		if role == defaultRoutingRole {
			continue
		}
		if len(rule.Prefer) > 0 {
			merged.RoleDefaults[role] = cloneRoutingRule(rule)
		}
	}

	merged.RoleRouting = make(map[string]map[Complexity]RoutingRule, len(base.RoleRouting))
	for role, routes := range base.RoleRouting {
		merged.RoleRouting[role] = cloneRoutingMap(routes)
	}
	for role, routes := range user.RoleRouting {
		role = normalizeRoutingRole(role)
		if role == defaultRoutingRole {
			continue
		}
		if merged.RoleRouting[role] == nil {
			merged.RoleRouting[role] = make(map[Complexity]RoutingRule)
		}
		for complexity, rule := range routes {
			if len(rule.Prefer) > 0 {
				merged.RoleRouting[role][complexity] = cloneRoutingRule(rule)
			}
		}
	}

	// Concurrency: apply positive user values.
	if user.Concurrency.MaxActiveLineages > 0 {
		merged.Concurrency.MaxActiveLineages = user.Concurrency.MaxActiveLineages
	}
	if user.Concurrency.MaxPlanningWorkers > 0 {
		merged.Concurrency.MaxPlanningWorkers = user.Concurrency.MaxPlanningWorkers
	}
	if user.Concurrency.MaxWorkersTotal > 0 {
		merged.Concurrency.MaxWorkersTotal = user.Concurrency.MaxWorkersTotal
	}
	if user.Concurrency.MaxWorkersPerPool > 0 {
		merged.Concurrency.MaxWorkersPerPool = user.Concurrency.MaxWorkersPerPool
	}
	if user.Concurrency.MaxWorkersPerLineage > 0 {
		merged.Concurrency.MaxWorkersPerLineage = user.Concurrency.MaxWorkersPerLineage
	}
	if user.Concurrency.MaxIdlePerPool > 0 {
		merged.Concurrency.MaxIdlePerPool = user.Concurrency.MaxIdlePerPool
	}
	if user.Concurrency.CouncilSeatIdleTTLSeconds > 0 {
		merged.Concurrency.CouncilSeatIdleTTLSeconds = user.Concurrency.CouncilSeatIdleTTLSeconds
	}

	// Failure policy: deep-copy then merge per class.
	merged.FailurePolicy = make(map[string]FailurePolicyRule, len(base.FailurePolicy))
	for k, v := range base.FailurePolicy {
		merged.FailurePolicy[k] = v
	}
	for class, rule := range user.FailurePolicy {
		if strings.TrimSpace(rule.Action) != "" {
			merged.FailurePolicy[class] = rule
		}
	}

	// Snapshots: override positive values only.
	if user.Snapshots.PlanHistoryLimit > 0 {
		merged.Snapshots.PlanHistoryLimit = user.Snapshots.PlanHistoryLimit
	}

	return merged
}

func LoadKitchenConfig(path string) (KitchenConfig, error) {
	cfg := DefaultKitchenConfig()
	if strings.TrimSpace(path) == "" {
		defaultPaths, err := DefaultKitchenPaths()
		if err != nil {
			return KitchenConfig{}, err
		}
		path = defaultPaths.ConfigPath
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return KitchenConfig{}, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return KitchenConfig{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return KitchenConfig{}, err
	}
	return cfg, nil
}

func (c KitchenConfig) Validate() error {
	for _, complexity := range allComplexities {
		rule, ok := c.Routing[complexity]
		if !ok || len(rule.Prefer) == 0 {
			return fmt.Errorf("routing.%s.prefer must not be empty", complexity)
		}
		entries := append([]PoolKey{}, rule.Prefer...)
		entries = append(entries, rule.Fallback...)
		for i, key := range entries {
			if strings.TrimSpace(key.Provider) == "" || strings.TrimSpace(key.Model) == "" {
				return fmt.Errorf("routing.%s entry %d must include provider and model", complexity, i)
			}
		}
	}
	for role, routes := range c.RoleRouting {
		role = normalizeRoutingRole(role)
		if role == defaultRoutingRole {
			return fmt.Errorf("roleRouting.%s is reserved; use routing for default settings", defaultRoutingRole)
		}
		for complexity, rule := range routes {
			if !isValidComplexity(complexity) {
				return fmt.Errorf("roleRouting.%s.%s is not a valid complexity", role, complexity)
			}
			if len(rule.Prefer) == 0 {
				return fmt.Errorf("roleRouting.%s.%s.prefer must not be empty", role, complexity)
			}
			entries := append([]PoolKey{}, rule.Prefer...)
			entries = append(entries, rule.Fallback...)
			for i, key := range entries {
				if strings.TrimSpace(key.Provider) == "" || strings.TrimSpace(key.Model) == "" {
					return fmt.Errorf("roleRouting.%s.%s entry %d must include provider and model", role, complexity, i)
				}
			}
		}
	}
	for role, rule := range c.RoleDefaults {
		role = normalizeRoutingRole(role)
		if role == defaultRoutingRole {
			return fmt.Errorf("roleDefaults.%s is reserved; use routing for default settings", defaultRoutingRole)
		}
		entries := append([]PoolKey{}, rule.Prefer...)
		entries = append(entries, rule.Fallback...)
		if len(rule.Prefer) == 0 {
			return fmt.Errorf("roleDefaults.%s.prefer must not be empty", role)
		}
		for i, key := range entries {
			if strings.TrimSpace(key.Provider) == "" || strings.TrimSpace(key.Model) == "" {
				return fmt.Errorf("roleDefaults.%s entry %d must include provider and model", role, i)
			}
		}
	}

	if c.Concurrency.MaxActiveLineages <= 0 ||
		c.Concurrency.MaxPlanningWorkers <= 0 ||
		c.Concurrency.MaxWorkersTotal <= 0 ||
		c.Concurrency.MaxWorkersPerPool <= 0 ||
		c.Concurrency.MaxWorkersPerLineage <= 0 {
		return fmt.Errorf("concurrency limits must be positive")
	}
	if c.Concurrency.MaxIdlePerPool < 0 {
		return fmt.Errorf("concurrency.maxIdlePerPool must be zero or greater")
	}
	if c.Concurrency.CouncilSeatIdleTTLSeconds <= 0 {
		return fmt.Errorf("concurrency.councilSeatIdleTTLSeconds must be positive")
	}
	if c.Snapshots.PlanHistoryLimit < 0 {
		return fmt.Errorf("snapshots.planHistoryLimit must be zero or greater")
	}

	for class, policy := range c.FailurePolicy {
		if strings.TrimSpace(class) == "" || strings.TrimSpace(policy.Action) == "" {
			return fmt.Errorf("failure_policy entries must include class and action")
		}
	}
	return nil
}

// SaveKitchenConfigFile marshals cfg as YAML and writes it atomically.
func SaveKitchenConfigFile(path string, cfg *KitchenConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

func projectKey(repoPath string) string {
	base := strings.ToLower(filepath.Base(repoPath))
	base = nonSlug.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "project"
	}
	sum := sha256.Sum256([]byte(repoPath))
	return fmt.Sprintf("%s-%s", base, hex.EncodeToString(sum[:])[:10])
}
