package main

import (
	"fmt"
	"strings"
	"time"
)

type ComplexityRouter struct {
	table     map[string]map[Complexity][]PoolKey
	fallback  map[string]map[Complexity][]PoolKey
	health    *ProviderHealth
	hostPools []PoolKey
}

func NewComplexityRouter(cfg KitchenConfig, health *ProviderHealth, hostPool ...PoolKey) *ComplexityRouter {
	r := &ComplexityRouter{
		table:    make(map[string]map[Complexity][]PoolKey),
		fallback: make(map[string]map[Complexity][]PoolKey),
		health:   health,
	}
	for _, pool := range hostPool {
		if strings.TrimSpace(pool.Provider) == "" {
			continue
		}
		r.hostPools = append(r.hostPools, pool)
	}
	r.setRoleRouting(defaultRoutingRole, cfg.Routing)
	for role := range cfg.RoleRouting {
		role = normalizeRoutingRole(role)
		if role == defaultRoutingRole {
			continue
		}
		r.setRoleRouting(role, effectiveRoutingForRole(cfg, role))
	}
	for _, seat := range []string{"A", "B"} {
		r.setRoleRouting(councilSeatRoutingRole(seat), effectiveRoutingForCouncilSeat(cfg, seat))
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
	role = normalizeRoutingRole(role)

	table := r.table[role]
	fallback := r.fallback[role]
	if len(table) == 0 && len(fallback) == 0 && role != defaultRoutingRole {
		table = r.table[defaultRoutingRole]
		fallback = r.fallback[defaultRoutingRole]
	}

	ordered := append([]PoolKey(nil), table[c]...)
	ordered = append(ordered, fallback[c]...)
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

func (r *ComplexityRouter) ResolveCouncilSeat(seat string, c Complexity) []PoolKey {
	return r.ResolveForRole(councilSeatRoutingRole(seat), c)
}

func (r *ComplexityRouter) setRoleRouting(role string, routing map[Complexity]RoutingRule) {
	role = normalizeRoutingRole(role)
	if r.table[role] == nil {
		r.table[role] = make(map[Complexity][]PoolKey, len(routing))
	}
	if r.fallback[role] == nil {
		r.fallback[role] = make(map[Complexity][]PoolKey, len(routing))
	}
	for complexity, rule := range routing {
		r.table[role][complexity] = append([]PoolKey(nil), rule.Prefer...)
		r.fallback[role][complexity] = append([]PoolKey(nil), rule.Fallback...)
	}
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
