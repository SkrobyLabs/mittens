// Package credutil provides shared credential utilities used by both the
// host binary (cmd/mittens) and the container entrypoint (cmd/mittens-init).
package credutil

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// ExpiresAt extracts a generic highest expiry timestamp from credential JSON.
// It understands shared/simple formats only:
//   - expiresAt
//   - expires_at
//   - expiry_date
//   - nested expiresAt / expires_at
//
// Provider-specific formats should use ExpiresAtForProvider.
func ExpiresAt(data []byte) int64 {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return 0
	}

	var best int64

	// Check root-level expiry fields.
	for _, field := range []string{"expiresAt", "expires_at", "expiry_date"} {
		best = max(best, extractNumericTimestamp(obj[field]))
	}

	// Check nested objects for expiry fields (e.g. claudeAiOauth).
	for _, raw := range obj {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			continue
		}
		for _, field := range []string{"expiresAt", "expires_at"} {
			best = max(best, extractNumericTimestamp(nested[field]))
		}
	}

	return best
}

// ExpiresAtForProvider extracts an expiry timestamp using provider-specific
// credential conventions when needed.
func ExpiresAtForProvider(provider string, data []byte) int64 {
	best := ExpiresAt(data)

	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return best
	}

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "openai":
		if raw, ok := obj["tokens"]; ok {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil {
				best = max(best, extractJWTExpiryMillis(nested["access_token"]))
				best = max(best, extractJWTExpiryMillis(nested["id_token"]))
			}
		}
	}

	return best
}

func extractNumericTimestamp(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var ts float64
	if json.Unmarshal(raw, &ts) != nil {
		return 0
	}
	return int64(ts)
}

func extractJWTExpiryMillis(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var token string
	if json.Unmarshal(raw, &token) != nil || strings.TrimSpace(token) == "" {
		return 0
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return 0
	}
	exp := extractNumericTimestamp(claims["exp"])
	if exp <= 0 {
		return 0
	}
	return exp * 1000
}

func max(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

// ExpiresAtString is a convenience wrapper for string credential data.
func ExpiresAtString(jsonData string) int64 {
	return ExpiresAt([]byte(jsonData))
}
