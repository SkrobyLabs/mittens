package credutil

import (
	"encoding/base64"
	"fmt"
	"testing"
)

func TestExpiresAt(t *testing.T) {
	tests := []struct {
		name string
		json string
		want int64
	}{
		{
			name: "claude nested expiresAt",
			json: `{"claudeAiOauth":{"accessToken":"tok","expiresAt":1772566429991}}`,
			want: 1772566429991,
		},
		{
			name: "codex root expires_at",
			json: `{"access_token":"tok","expires_at":1700000999}`,
			want: 1700000999,
		},
		{
			name: "codex chatgpt tokens ignores invalid jwt",
			json: `{"auth_mode":"chatgpt","tokens":{"access_token":"not-a-jwt","id_token":"still-not-a-jwt"}}`,
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpiresAt([]byte(tc.json)); got != tc.want {
				t.Fatalf("ExpiresAt(%q) = %d, want %d", tc.json, got, tc.want)
			}
		})
	}
}

func TestExpiresAtForProvider(t *testing.T) {
	codexJSON := fmt.Sprintf(`{"OPENAI_API_KEY":null,"auth_mode":"chatgpt","last_refresh":"2026-04-15T22:09:13Z","tokens":{"access_token":%q,"id_token":%q,"refresh_token":"rt"}}`,
		testJWT(1777154953), testJWT(1776294552))

	tests := []struct {
		name     string
		provider string
		json     string
		want     int64
	}{
		{
			name:     "codex jwt tokens",
			provider: "codex",
			json:     codexJSON,
			want:     1777154953000,
		},
		{
			name:     "unknown provider does not infer codex jwt",
			provider: "",
			json:     codexJSON,
			want:     0,
		},
		{
			name:     "claude nested remains supported",
			provider: "claude",
			json:     `{"claudeAiOauth":{"accessToken":"tok","expiresAt":1772566429991}}`,
			want:     1772566429991,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpiresAtForProvider(tc.provider, []byte(tc.json)); got != tc.want {
				t.Fatalf("ExpiresAtForProvider(%q, %q) = %d, want %d", tc.provider, tc.json, got, tc.want)
			}
		})
	}
}

func testJWT(exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return header + "." + payload + ".sig"
}
