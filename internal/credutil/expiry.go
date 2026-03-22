// Package credutil provides shared credential utilities used by both the
// host binary (cmd/mittens) and the container entrypoint (cmd/mittens-init).
package credutil

import "encoding/json"

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
