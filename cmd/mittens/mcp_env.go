package main

import (
	"regexp"
)

// mcpEnvVarRE matches ${VAR} and ${VAR:-default} tokens.
var mcpEnvVarRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandMCPValue substitutes ${VAR} and ${VAR:-default} tokens in value using
// lookup. It returns the expanded string, the names of variables resolved from
// the environment (injected), and the names of variables that were neither set
// nor defaulted (unresolved). Unresolved tokens are left untouched (never
// substituted with empty).
func expandMCPValue(value string, lookup func(string) (string, bool)) (result string, injected, unresolved []string) {
	result = mcpEnvVarRE.ReplaceAllStringFunc(value, func(match string) string {
		m := mcpEnvVarRE.FindStringSubmatch(match)
		name := m[1]
		hasDefault := m[2] != ""
		def := m[3]
		if v, ok := lookup(name); ok {
			injected = append(injected, name)
			return v
		}
		if hasDefault {
			return def
		}
		unresolved = append(unresolved, name)
		return match
	})
	return result, injected, unresolved
}
