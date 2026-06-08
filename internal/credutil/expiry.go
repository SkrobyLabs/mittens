// Package credutil provides shared credential utilities used by both the
// host binary (cmd/mittens) and the container entrypoint (cmd/mittens-init).
package credutil

import (
	"encoding/json"
	"time"
)

const minPlausibleExpiryMillis int64 = 946684800000 // 2000-01-01T00:00:00Z

// ExpiresAt extracts the highest expiry timestamp from credential JSON.
// Checks root-level fields: expiresAt (Claude), expires_at (Codex), expiry_date (Gemini).
// Also checks nested objects (e.g. claudeAiOauth.expiresAt, claudeAiOauth.expires_at).
// All timestamps are in milliseconds. Returns 0 if no valid expiry is found.
func ExpiresAt(data []byte) int64 {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return 0
	}

	var best int64

	// Check root-level expiry fields.
	for _, field := range []string{"expiresAt", "expires_at", "expiry_date"} {
		if raw, ok := obj[field]; ok {
			var ts float64
			if json.Unmarshal(raw, &ts) == nil && int64(ts) > best {
				best = int64(ts)
			}
		}
	}

	// Check nested objects for expiry fields (e.g. claudeAiOauth).
	for _, raw := range obj {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			continue
		}
		for _, field := range []string{"expiresAt", "expires_at"} {
			if expRaw, ok := nested[field]; ok {
				var ts float64
				if json.Unmarshal(expRaw, &ts) == nil && int64(ts) > best {
					best = int64(ts)
				}
			}
		}
	}

	return best
}

// ExpiresAtString is a convenience wrapper for string credential data.
func ExpiresAtString(jsonData string) int64 {
	return ExpiresAt([]byte(jsonData))
}

// ObjectFieldCount returns the number of top-level fields in a JSON object.
// The boolean is false when the input is not a valid JSON object.
func ObjectFieldCount(data []byte) (int, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return 0, false
	}
	return len(obj), true
}

// ExpiresAtMillis normalizes credential expiry to Unix milliseconds.
// Codex auth.json may store expires_at as Unix seconds, while Claude/Gemini
// credentials use milliseconds.
func ExpiresAtMillis(data []byte) int64 {
	exp := ExpiresAt(data)
	if exp > 0 && exp < 1_000_000_000_000 {
		return exp * 1000
	}
	return exp
}

// IsExpired reports whether the credential JSON has an expiry timestamp that
// is already at or before now. Missing or invalid expiry is not treated as
// expired here; callers that require a valid expiry should check ExpiresAt too.
func IsExpired(data []byte, now time.Time) bool {
	exp := ExpiresAtMillis(data)
	return exp >= minPlausibleExpiryMillis && exp <= now.UnixMilli()
}
