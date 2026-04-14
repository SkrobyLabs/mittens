package main

import (
	"fmt"
	"strings"
	"time"
)

type ComplexityRouter struct {
	cfg       KitchenConfig
	health    *ProviderHealth
	hostPools []PoolKey
}

func NewComplexityRouter(cfg KitchenConfig, health *ProviderHealth, hostPool ...PoolKey) *ComplexityRouter {
	r := &ComplexityRouter{
		cfg:    cloneKitchenConfig(cfg),
		health: health,
	}
	for _, pool := range hostPool {
		if strings.TrimSpace(pool.Provider) == "" {
			continue
		}
		r.hostPools = append(r.hostPools, pool)
	}
	return r
}

func (r *ComplexityRouter) Resolve(c Complexity) []PoolKey {
	return r.ResolveForRole(defaultRoutingRole, c)
}

func (r *ComplexityRouter) ResolveForRole(role string, c Complexity) []PoolKey {
	if r == nil {
		return nil
	}
	policy := effectiveProviderPolicyForRole(r.cfg, role)
	return r.resolvePolicy(policy, c)
}

func (r *ComplexityRouter) ResolveCouncilSeat(seat string, c Complexity) []PoolKey {
	if r == nil {
		return nil
	}
	policy := effectiveProviderPolicyForCouncilSeat(r.cfg, seat)
	return r.resolvePolicy(policy, c)
}

func (r *ComplexityRouter) resolvePolicy(policy ProviderPolicy, c Complexity) []PoolKey {
	if len(policy.Prefer) == 0 {
		return nil
	}
	ordered := r.poolKeysForPolicy(policy, c)
	healthy := ordered
	if r.health != nil {
		now := time.Now().UTC()
		healthy = nil
		for _, key := range ordered {
			if r.health.Available(poolKeyID(key), now) {
				healthy = append(healthy, key)
			}
		}
	}
	if len(r.hostPools) == 0 {
		return healthy
	}
	if len(r.hostPools) == 1 {
		for _, key := range healthy {
			if sameProvider(key.Provider, r.hostPools[0].Provider) {
				return healthy
			}
		}
		return append([]PoolKey(nil), r.hostPools...)
	}
	supported := make([]PoolKey, 0, len(healthy))
	for _, key := range healthy {
		if r.supportsProvider(key.Provider) {
			supported = append(supported, key)
		}
	}
	if len(supported) > 0 {
		return supported
	}
	return nil
}

func (r *ComplexityRouter) poolKeysForPolicy(policy ProviderPolicy, c Complexity) []PoolKey {
	keys := make([]PoolKey, 0, len(policy.Prefer)+len(policy.Fallback))
	appendProvider := func(provider string) {
		model, ok := modelForProviderComplexity(r.cfg, provider, c)
		if !ok {
			return
		}
		keys = append(keys, PoolKey{
			Provider: provider,
			Model:    model,
		})
	}
	for _, provider := range policy.Prefer {
		appendProvider(provider)
	}
	for _, provider := range policy.Fallback {
		appendProvider(provider)
	}
	return keys
}

func councilSeatRoutingRole(seat string) string {
	if normalized := normalizeCouncilSeat(seat); normalized != "" {
		return fmt.Sprintf("council-seat:%s", normalized)
	}
	return defaultRoutingRole
}

func (r *ComplexityRouter) supportsProvider(provider string) bool {
	for _, pool := range r.hostPools {
		if sameProvider(pool.Provider, provider) {
			return true
		}
	}
	return false
}

func canonicalKitchenProviderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "anthropic":
		return "claude"
	case "codex", "openai":
		return "codex"
	case "gemini", "google":
		return "gemini"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func sameProvider(a, b string) bool {
	return canonicalKitchenProviderName(a) == canonicalKitchenProviderName(b)
}

func (r *ComplexityRouter) Escalate(c Complexity) (Complexity, bool) {
	switch c {
	case ComplexityTrivial:
		return ComplexityLow, true
	case ComplexityLow:
		return ComplexityMedium, true
	case ComplexityMedium:
		return ComplexityHigh, true
	case ComplexityHigh:
		return ComplexityCritical, true
	default:
		return "", false
	}
}

func poolKeyID(key PoolKey) string {
	return key.Provider + "/" + key.Model
}
