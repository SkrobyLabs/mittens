package pool

// ModelRouter maps role names to ModelConfig values.
// It is read-only after construction — no mutex needed.
type ModelRouter struct {
	routes   map[string]ModelConfig
	fallback ModelConfig
}

// NewModelRouter creates a ModelRouter from a role→config map.
// The "default" key, if present, is used as the fallback for unknown roles.
func NewModelRouter(routes map[string]ModelConfig) *ModelRouter {
	if routes == nil {
		routes = make(map[string]ModelConfig)
	}

	r := &ModelRouter{
		routes: make(map[string]ModelConfig, len(routes)),
	}

	for k, v := range routes {
		if k == "default" {
			r.fallback = v
		} else {
			r.routes[k] = v
		}
	}
	return r
}

// Resolve returns the ModelConfig for the given role.
// Resolution: exact match → default fallback → zero-value ModelConfig.
func (r *ModelRouter) Resolve(role string) ModelConfig {
	if cfg, ok := r.routes[role]; ok {
		return cfg
	}
	return r.fallback
}
