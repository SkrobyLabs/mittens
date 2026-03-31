package pool

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SessionReuseConfig controls whether worker containers keep their AI session
// alive between tasks instead of clearing it each time.
type SessionReuseConfig struct {
	Enabled      bool `yaml:"enabled" json:"enabled"`
	TTLSeconds   int  `yaml:"ttl_seconds" json:"ttlSeconds"`       // default 300
	MaxTasks     int  `yaml:"max_tasks" json:"maxTasks"`            // default 3
	MaxTokens    int  `yaml:"max_tokens" json:"maxTokens"`          // default 100000
	SameRoleOnly bool `yaml:"same_role_only" json:"sameRoleOnly"`   // default true
}

// DefaultSessionReuseConfig returns the default session reuse settings.
func DefaultSessionReuseConfig() SessionReuseConfig {
	return SessionReuseConfig{
		Enabled:      false,
		TTLSeconds:   300,
		MaxTasks:     3,
		MaxTokens:    100000,
		SameRoleOnly: true,
	}
}

// TeamConfig holds team session configuration parsed from team.yaml.
type TeamConfig struct {
	MaxWorkers   int                        `yaml:"max_workers"`
	Models       map[string]ModelConfig     `yaml:"models"`
	SessionReuse SessionReuseConfig         `yaml:"session_reuse"`
}

// LoadTeamConfig reads a team.yaml file and returns the parsed config.
// Returns an empty config (not an error) if the file does not exist.
func LoadTeamConfig(path string) (*TeamConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &TeamConfig{}, nil
		}
		return nil, fmt.Errorf("read team config: %w", err)
	}

	var cfg TeamConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse team config: %w", err)
	}
	return &cfg, nil
}

// DefaultTeamConfigPath returns the default team.yaml path within a state directory.
func DefaultTeamConfigPath(stateDir string) string {
	return filepath.Join(stateDir, "team.yaml")
}
