package main

import (
	"strings"
	"time"
)

type ComplexityRouter struct {
	table    map[Complexity][]PoolKey
	fallback map[Complexity][]PoolKey
	health   *ProviderHealth
	hostPools []PoolKey
}

func NewComplexityRouter(cfg KitchenConfig, health *ProviderHealth, hostPool ...PoolKey) *ComplexityRouter {
	r := &ComplexityRouter{
		table:    make(map[Complexity][]PoolKey, len(cfg.Routing)),
		fallback: make(map[Complexity][]PoolKey, len(cfg.Routing)),
		health:   health,
	}
	for _, pool := range hostPool {
		if strings.TrimSpace(pool.Provider) == "" {
			continue
		}
		r.hostPools = append(r.hostPools, pool)
	}
	for complexity, rule := range cfg.Routing {
		r.table[complexity] = append([]PoolKey(nil), rule.Prefer...)
		r.fallback[complexity] = append([]PoolKey(nil), rule.Fallback...)
	}
	return r
}

func (r *ComplexityRouter) Resolve(c Complexity) []PoolKey {
	if r == nil {
		return nil
	}

	ordered := append([]PoolKey(nil), r.table[c]...)
	ordered = append(ordered, r.fallback[c]...)
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
