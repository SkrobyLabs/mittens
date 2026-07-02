package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

// mcpPinPayload is the canonical, JSON-marshaled shape hashed to pin a proxy
// server's command. Args is always a non-nil slice ([] never null) and EnvKeys
// is sorted and de-duplicated so the digest cannot drift with map ordering.
type mcpPinPayload struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	EnvKeys []string `json:"envKeys"`
}

// mcpCommandPin computes the stable "sha256:<hex>" pin for a server from its
// pre-env-expansion command, args, and env variable names.
func mcpCommandPin(s mcpconfig.Server) string {
	args := s.Args
	if args == nil {
		args = []string{}
	}
	keys := make([]string, 0, len(s.Env))
	seen := map[string]struct{}{}
	for k := range s.Env {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	payload := mcpPinPayload{Command: s.Command, Args: args, EnvKeys: keys}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
