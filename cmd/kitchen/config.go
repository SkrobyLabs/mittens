package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

type ProviderPolicy struct {
	Prefer   []string `json:"prefer,omitempty" yaml:"prefer,omitempty"`
	Fallback []string `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

const defaultRoutingRole = "default"

type ConcurrencyConfig struct {
	MaxActiveLineages         int `json:"maxActiveLineages" yaml:"maxActiveLineages"`
	MaxPlanningWorkers        int `json:"maxPlanningWorkers" yaml:"maxPlanningWorkers"`
	MaxWorkersTotal           int `json:"maxWorkersTotal" yaml:"maxWorkersTotal"`
	MaxWorkersPerPool         int `json:"maxWorkersPerPool" yaml:"maxWorkersPerPool"`
	MaxWorkersPerLineage      int `json:"maxWorkersPerLineage" yaml:"maxWorkersPerLineage"`
	MaxIdlePerPool            int `json:"maxIdlePerPool" yaml:"maxIdlePerPool"`
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
	ProviderModels      map[string]map[Complexity]string `json:"providerModels" yaml:"providerModels"`
	RoleProviders       map[string]ProviderPolicy        `json:"roleProviders,omitempty" yaml:"roleProviders,omitempty"`
	CouncilSeatProviders map[string]ProviderPolicy       `json:"councilSeatProviders,omitempty" yaml:"councilSeatProviders,omitempty"`
	Concurrency         ConcurrencyConfig                `json:"concurrency" yaml:"concurrency"`
	FailurePolicy       map[string]FailurePolicyRule     `json:"failure_policy" yaml:"failure_policy"`
	Snapshots           SnapshotConfig                   `json:"snapshots" yaml:"snapshots"`
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
		ProviderModels: map[string]map[Complexity]string{
			"anthropic": defaultProviderModelMap("sonnet"),
			"openai":    defaultProviderModelMap("gpt-5.4"),
			"gemini":    defaultProviderModelMap("gemini-3-flash-preview"),
		},
		RoleProviders: map[string]ProviderPolicy{
			defaultRoutingRole: {
				Prefer:   []string{"anthropic"},
				Fallback: []string{"openai", "gemini"},
			},
		},
		CouncilSeatProviders: map[string]ProviderPolicy{},
		Concurrency: ConcurrencyConfig{
			MaxActiveLineages:         5,
			MaxPlanningWorkers:        2,
			MaxWorkersTotal:           12,
			MaxWorkersPerPool:         6,
			MaxWorkersPerLineage:      4,
			MaxIdlePerPool:            2,
			CouncilSeatIdleTTLSeconds: 270,
		},
		FailurePolicy: map[string]FailurePolicyRule{
			"capability":     {Action: "escalate_complexity", Max: 2},
			"plan":           {Action: "replan", Max: 1},
			"environment":    {Action: "retry", Max: 2},
			"conflict":       {Action: "retry_merge", Max: 1},
			"auth":           {Action: "recycle_worker_retry_same_provider", Max: 1},
			"infrastructure": {Action: "respawn", Max: 1},
		},
		Snapshots: SnapshotConfig{
			PlanHistoryLimit: defaultPlanProgressHistoryLimit,
		},
	}
}

func defaultProviderModelMap(model string) map[Complexity]string {
	m := make(map[Complexity]string, len(allComplexities))
	for _, complexity := range allComplexities {
		m[complexity] = model
	}
	return m
}

func configurableKitchenRoles() []string {
	return []string{
		defaultRoutingRole,
		plannerTaskRole,
		"implementer",
		"reviewer",
		lineageFixMergeRole,
		researcherTaskRole,
	}
}

func normalizeRoutingRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return defaultRoutingRole
	}
	return role
}

func normalizeCouncilSeat(seat string) string {
	seat = strings.ToUpper(strings.TrimSpace(seat))
	switch seat {
	case "A", "B":
		return seat
	default:
		return ""
	}
}

func normalizeConfigProviderName(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "anthropic", "claude":
		return "anthropic", nil
	case "openai", "codex":
		return "openai", nil
	case "gemini", "google":
		return "gemini", nil
	case "":
		return "", fmt.Errorf("provider must not be empty")
	default:
		return "", fmt.Errorf("unknown provider %q (available: anthropic, openai, gemini)", name)
	}
}

func cloneProviderPolicy(policy ProviderPolicy) ProviderPolicy {
	return ProviderPolicy{
		Prefer:   append([]string(nil), policy.Prefer...),
		Fallback: append([]string(nil), policy.Fallback...),
	}
}

func cloneProviderModels(src map[string]map[Complexity]string) map[string]map[Complexity]string {
	out := make(map[string]map[Complexity]string, len(src))
	for provider, models := range src {
		modelCopy := make(map[Complexity]string, len(models))
		for complexity, model := range models {
			modelCopy[complexity] = model
		}
		out[provider] = modelCopy
	}
	return out
}

func cloneKitchenConfig(cfg KitchenConfig) KitchenConfig {
	clone := cfg
	clone.ProviderModels = cloneProviderModels(cfg.ProviderModels)
	clone.RoleProviders = make(map[string]ProviderPolicy, len(cfg.RoleProviders))
	for role, policy := range cfg.RoleProviders {
		clone.RoleProviders[role] = cloneProviderPolicy(policy)
	}
	clone.CouncilSeatProviders = make(map[string]ProviderPolicy, len(cfg.CouncilSeatProviders))
	for seat, policy := range cfg.CouncilSeatProviders {
		clone.CouncilSeatProviders[seat] = cloneProviderPolicy(policy)
	}
	clone.FailurePolicy = make(map[string]FailurePolicyRule, len(cfg.FailurePolicy))
	for class, rule := range cfg.FailurePolicy {
		clone.FailurePolicy[class] = rule
	}
	return clone
}

func providerPolicyEqual(a, b ProviderPolicy) bool {
	return slices.Equal(a.Prefer, b.Prefer) && slices.Equal(a.Fallback, b.Fallback)
}

func providerModelsEqual(a, b map[Complexity]string) bool {
	if len(a) != len(b) {
		return false
	}
	for complexity, model := range a {
		if b[complexity] != model {
			return false
		}
	}
	return true
}

func modelForProviderComplexity(cfg KitchenConfig, provider string, complexity Complexity) (string, bool) {
	provider, err := normalizeConfigProviderName(provider)
	if err != nil {
		return "", false
	}
	models := cfg.ProviderModels[provider]
	model, ok := models[complexity]
	if !ok || strings.TrimSpace(model) == "" {
		return "", false
	}
	return model, true
}

func roleProviderPolicy(cfg KitchenConfig, role string) (ProviderPolicy, bool) {
	role = normalizeRoutingRole(role)
	policy, ok := cfg.RoleProviders[role]
	if !ok || len(policy.Prefer) == 0 {
		return ProviderPolicy{}, false
	}
	return cloneProviderPolicy(policy), true
}

func effectiveProviderPolicyForRole(cfg KitchenConfig, role string) ProviderPolicy {
	role = normalizeRoutingRole(role)
	if policy, ok := roleProviderPolicy(cfg, role); ok {
		return policy
	}
	if role != defaultRoutingRole {
		if policy, ok := roleProviderPolicy(cfg, defaultRoutingRole); ok {
			return policy
		}
	}
	return ProviderPolicy{}
}

func councilSeatProviderPolicy(cfg KitchenConfig, seat string) (ProviderPolicy, bool) {
	seat = normalizeCouncilSeat(seat)
	if seat == "" {
		return ProviderPolicy{}, false
	}
	policy, ok := cfg.CouncilSeatProviders[seat]
	if !ok || (len(policy.Prefer) == 0 && len(policy.Fallback) == 0) {
		return ProviderPolicy{}, false
	}
	return cloneProviderPolicy(policy), true
}

func effectiveProviderPolicyForCouncilSeat(cfg KitchenConfig, seat string) ProviderPolicy {
	policy := effectiveProviderPolicyForRole(cfg, plannerTaskRole)
	seatPolicy, ok := councilSeatProviderPolicy(cfg, seat)
	if !ok {
		return policy
	}
	if len(seatPolicy.Prefer) > 0 {
		policy.Prefer = append([]string(nil), seatPolicy.Prefer...)
	}
	if len(seatPolicy.Fallback) > 0 {
		policy.Fallback = append([]string(nil), seatPolicy.Fallback...)
	}
	return policy
}

func setProviderModel(cfg *KitchenConfig, provider string, complexity Complexity, model string) error {
	if cfg == nil {
		return nil
	}
	provider, err := normalizeConfigProviderName(provider)
	if err != nil {
		return err
	}
	if !isValidComplexity(complexity) {
		return fmt.Errorf("invalid complexity %q", complexity)
	}
	if cfg.ProviderModels == nil {
		cfg.ProviderModels = make(map[string]map[Complexity]string)
	}
	if cfg.ProviderModels[provider] == nil {
		cfg.ProviderModels[provider] = make(map[Complexity]string)
	}
	cfg.ProviderModels[provider][complexity] = strings.TrimSpace(model)
	return nil
}

func setRoleProviderPolicy(cfg *KitchenConfig, role string, policy ProviderPolicy) {
	if cfg == nil {
		return
	}
	role = normalizeRoutingRole(role)
	policy = cloneProviderPolicy(policy)
	if cfg.RoleProviders == nil {
		cfg.RoleProviders = make(map[string]ProviderPolicy)
	}
	if role != defaultRoutingRole && len(policy.Prefer) == 0 {
		delete(cfg.RoleProviders, role)
		return
	}
	cfg.RoleProviders[role] = policy
}

func clearProviderPolicyForRole(cfg *KitchenConfig, role string) {
	if cfg == nil || cfg.RoleProviders == nil {
		return
	}
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return
	}
	delete(cfg.RoleProviders, role)
}

func roleHasProviderOverride(cfg KitchenConfig, role string) bool {
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return true
	}
	policy, ok := cfg.RoleProviders[role]
	return ok && len(policy.Prefer) > 0
}

func setCouncilSeatProviderPolicy(cfg *KitchenConfig, seat string, policy ProviderPolicy) {
	if cfg == nil {
		return
	}
	seat = normalizeCouncilSeat(seat)
	if seat == "" {
		return
	}
	if cfg.CouncilSeatProviders == nil {
		cfg.CouncilSeatProviders = make(map[string]ProviderPolicy)
	}
	policy = cloneProviderPolicy(policy)
	if len(policy.Prefer) == 0 && len(policy.Fallback) == 0 {
		delete(cfg.CouncilSeatProviders, seat)
		return
	}
	cfg.CouncilSeatProviders[seat] = policy
}

func clearCouncilSeatProviderPolicy(cfg *KitchenConfig, seat string) {
	if cfg == nil || cfg.CouncilSeatProviders == nil {
		return
	}
	seat = normalizeCouncilSeat(seat)
	if seat == "" {
		return
	}
	delete(cfg.CouncilSeatProviders, seat)
}

func councilSeatHasProviderOverride(cfg KitchenConfig, seat string) bool {
	seat = normalizeCouncilSeat(seat)
	policy, ok := cfg.CouncilSeatProviders[seat]
	return ok && (len(policy.Prefer) > 0 || len(policy.Fallback) > 0)
}

func canonicalizeProviderPolicy(policy ProviderPolicy) (ProviderPolicy, error) {
	canonical := ProviderPolicy{}
	appendUnique := func(dst *[]string, src []string) error {
		for _, provider := range src {
			normalized, err := normalizeConfigProviderName(provider)
			if err != nil {
				return err
			}
			if !slices.Contains(*dst, normalized) {
				*dst = append(*dst, normalized)
			}
		}
		return nil
	}
	if err := appendUnique(&canonical.Prefer, policy.Prefer); err != nil {
		return ProviderPolicy{}, err
	}
	if err := appendUnique(&canonical.Fallback, policy.Fallback); err != nil {
		return ProviderPolicy{}, err
	}
	return canonical, nil
}

func canonicalizeKitchenConfig(cfg *KitchenConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.ProviderModels == nil {
		cfg.ProviderModels = make(map[string]map[Complexity]string)
	}
	normalizedModels := make(map[string]map[Complexity]string, len(cfg.ProviderModels))
	for provider, models := range cfg.ProviderModels {
		canonicalProvider, err := normalizeConfigProviderName(provider)
		if err != nil {
			return fmt.Errorf("providerModels.%s: %w", provider, err)
		}
		modelCopy := make(map[Complexity]string, len(models))
		for complexity, model := range models {
			modelCopy[complexity] = strings.TrimSpace(model)
		}
		if existing, ok := normalizedModels[canonicalProvider]; ok {
			if !providerModelsEqual(existing, modelCopy) {
				return fmt.Errorf("providerModels.%s conflicts with another alias of %s", provider, canonicalProvider)
			}
			continue
		}
		normalizedModels[canonicalProvider] = modelCopy
	}
	cfg.ProviderModels = normalizedModels

	if cfg.RoleProviders == nil {
		cfg.RoleProviders = make(map[string]ProviderPolicy)
	}
	normalizedRoles := make(map[string]ProviderPolicy, len(cfg.RoleProviders))
	for role, policy := range cfg.RoleProviders {
		role = normalizeRoutingRole(role)
		canonicalPolicy, err := canonicalizeProviderPolicy(policy)
		if err != nil {
			return fmt.Errorf("roleProviders.%s: %w", role, err)
		}
		normalizedRoles[role] = canonicalPolicy
	}
	cfg.RoleProviders = normalizedRoles

	if cfg.CouncilSeatProviders == nil {
		cfg.CouncilSeatProviders = make(map[string]ProviderPolicy)
	}
	normalizedSeats := make(map[string]ProviderPolicy, len(cfg.CouncilSeatProviders))
	for seat, policy := range cfg.CouncilSeatProviders {
		normalizedSeat := normalizeCouncilSeat(seat)
		if normalizedSeat == "" {
			normalizedSeat = seat
		}
		canonicalPolicy, err := canonicalizeProviderPolicy(policy)
		if err != nil {
			return fmt.Errorf("councilSeatProviders.%s: %w", seat, err)
		}
		normalizedSeats[normalizedSeat] = canonicalPolicy
	}
	cfg.CouncilSeatProviders = normalizedSeats
	pruneKitchenConfig(cfg)
	return nil
}

func pruneKitchenConfig(cfg *KitchenConfig) {
	if cfg == nil {
		return
	}
	if cfg.RoleProviders == nil {
		cfg.RoleProviders = make(map[string]ProviderPolicy)
	}
	defaultPolicy, ok := cfg.RoleProviders[defaultRoutingRole]
	if !ok || len(defaultPolicy.Prefer) == 0 {
		cfg.RoleProviders[defaultRoutingRole] = DefaultKitchenConfig().RoleProviders[defaultRoutingRole]
		defaultPolicy = cfg.RoleProviders[defaultRoutingRole]
	}
	for role, policy := range cfg.RoleProviders {
		role = normalizeRoutingRole(role)
		if role == defaultRoutingRole {
			cfg.RoleProviders[role] = cloneProviderPolicy(policy)
			continue
		}
		if len(policy.Prefer) == 0 || providerPolicyEqual(policy, defaultPolicy) {
			delete(cfg.RoleProviders, role)
		}
	}
	if cfg.CouncilSeatProviders == nil {
		cfg.CouncilSeatProviders = make(map[string]ProviderPolicy)
	}
	plannerPolicy := effectiveProviderPolicyForRole(*cfg, plannerTaskRole)
	for seat, policy := range cfg.CouncilSeatProviders {
		trimmed := cloneProviderPolicy(policy)
		if slices.Equal(trimmed.Prefer, plannerPolicy.Prefer) {
			trimmed.Prefer = nil
		}
		if slices.Equal(trimmed.Fallback, plannerPolicy.Fallback) {
			trimmed.Fallback = nil
		}
		if len(trimmed.Prefer) == 0 && len(trimmed.Fallback) == 0 {
			delete(cfg.CouncilSeatProviders, seat)
			continue
		}
		cfg.CouncilSeatProviders[seat] = trimmed
	}
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

func decodeKitchenConfig(data []byte) (*KitchenConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg KitchenConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := canonicalizeKitchenConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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
	return decodeKitchenConfig(data)
}

// MergeKitchenConfig applies user overrides on top of base.
func MergeKitchenConfig(base KitchenConfig, user KitchenConfig) KitchenConfig {
	merged := cloneKitchenConfig(base)

	for provider, models := range user.ProviderModels {
		if merged.ProviderModels[provider] == nil {
			merged.ProviderModels[provider] = make(map[Complexity]string)
		}
		for complexity, model := range models {
			if strings.TrimSpace(model) != "" {
				merged.ProviderModels[provider][complexity] = strings.TrimSpace(model)
			}
		}
	}

	for role, policy := range user.RoleProviders {
		merged.RoleProviders[normalizeRoutingRole(role)] = cloneProviderPolicy(policy)
	}
	for seat, policy := range user.CouncilSeatProviders {
		merged.CouncilSeatProviders[normalizeCouncilSeat(seat)] = cloneProviderPolicy(policy)
	}

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
	if user.Concurrency.MaxIdlePerPool >= 0 {
		merged.Concurrency.MaxIdlePerPool = user.Concurrency.MaxIdlePerPool
	}
	if user.Concurrency.CouncilSeatIdleTTLSeconds > 0 {
		merged.Concurrency.CouncilSeatIdleTTLSeconds = user.Concurrency.CouncilSeatIdleTTLSeconds
	}

	for class, rule := range user.FailurePolicy {
		if strings.TrimSpace(rule.Action) != "" {
			merged.FailurePolicy[class] = rule
		}
	}
	if user.Snapshots.PlanHistoryLimit > 0 {
		merged.Snapshots.PlanHistoryLimit = user.Snapshots.PlanHistoryLimit
	}
	pruneKitchenConfig(&merged)
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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return KitchenConfig{}, fmt.Errorf("parse config: %w", err)
	}
	if err := canonicalizeKitchenConfig(&cfg); err != nil {
		return KitchenConfig{}, err
	}
	if err := cfg.Validate(); err != nil {
		return KitchenConfig{}, err
	}
	return cfg, nil
}

func (c KitchenConfig) Validate() error {
	if len(c.ProviderModels) == 0 {
		return fmt.Errorf("providerModels must not be empty")
	}
	for provider, models := range c.ProviderModels {
		canonicalProvider, err := normalizeConfigProviderName(provider)
		if err != nil {
			return fmt.Errorf("providerModels.%s: %w", provider, err)
		}
		if canonicalProvider != provider {
			return fmt.Errorf("providerModels.%s must use canonical provider names", provider)
		}
		for complexity, model := range models {
			if !isValidComplexity(complexity) {
				return fmt.Errorf("providerModels.%s.%s is not a valid complexity", provider, complexity)
			}
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("providerModels.%s.%s must not be empty", provider, complexity)
			}
		}
		for _, complexity := range allComplexities {
			if strings.TrimSpace(models[complexity]) == "" {
				return fmt.Errorf("providerModels.%s.%s must not be empty", provider, complexity)
			}
		}
	}

	defaultPolicy, ok := c.RoleProviders[defaultRoutingRole]
	if !ok || len(defaultPolicy.Prefer) == 0 {
		return fmt.Errorf("roleProviders.%s.prefer must not be empty", defaultRoutingRole)
	}
	for role, policy := range c.RoleProviders {
		role = normalizeRoutingRole(role)
		if role != defaultRoutingRole && len(policy.Prefer) == 0 {
			return fmt.Errorf("roleProviders.%s.prefer must not be empty", role)
		}
		if err := validateProviderPolicy("roleProviders."+role, policy, c.ProviderModels, role == defaultRoutingRole); err != nil {
			return err
		}
	}
	for seat, policy := range c.CouncilSeatProviders {
		normalized := normalizeCouncilSeat(seat)
		if normalized == "" {
			return fmt.Errorf("councilSeatProviders.%s is not a valid seat; expected A or B", seat)
		}
		if err := validateProviderPolicy("councilSeatProviders."+normalized, policy, c.ProviderModels, false); err != nil {
			return err
		}
		if len(policy.Prefer) == 0 && len(policy.Fallback) == 0 {
			return fmt.Errorf("councilSeatProviders.%s must not be empty", normalized)
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

func validateProviderPolicy(scope string, policy ProviderPolicy, models map[string]map[Complexity]string, requirePrefer bool) error {
	if requirePrefer && len(policy.Prefer) == 0 {
		return fmt.Errorf("%s.prefer must not be empty", scope)
	}
	seen := make(map[string]bool)
	for idx, provider := range append(append([]string(nil), policy.Prefer...), policy.Fallback...) {
		canonicalProvider, err := normalizeConfigProviderName(provider)
		if err != nil {
			return fmt.Errorf("%s entry %d: %w", scope, idx, err)
		}
		if canonicalProvider != provider {
			return fmt.Errorf("%s entry %d must use canonical provider names", scope, idx)
		}
		if seen[canonicalProvider] {
			return fmt.Errorf("%s entry %d duplicates provider %q", scope, idx, canonicalProvider)
		}
		seen[canonicalProvider] = true
		if _, ok := models[canonicalProvider]; !ok {
			return fmt.Errorf("%s entry %d references provider %q with no providerModels entry", scope, idx, canonicalProvider)
		}
	}
	return nil
}

// SaveKitchenConfigFile marshals cfg as YAML and writes it atomically.
func SaveKitchenConfigFile(path string, cfg *KitchenConfig) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}
	canonical := cloneKitchenConfig(*cfg)
	if err := canonicalizeKitchenConfig(&canonical); err != nil {
		return err
	}
	if err := canonical.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(&canonical)
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
