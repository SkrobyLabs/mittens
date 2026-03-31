package adapter

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Adapter executes AI tasks and manages session state.
type Adapter interface {
	Execute(ctx context.Context, prompt string, priorContext string) (Result, error)
	ClearSession() error
	ForceClean() error
	Healthy() bool
}

// Result contains the output of a completed adapter execution.
type Result struct {
	Output       string
	ExitCode     int
	InputTokens  int
	OutputTokens int
}

// SessionReuseConfig controls whether workers reuse Claude Code sessions
// between tasks to benefit from prompt caching.
type SessionReuseConfig struct {
	Enabled      bool
	TTL          time.Duration
	MaxTasks     int
	MaxTokens    int  // cumulative input+output tokens before forced clear
	SameRoleOnly bool
}

// Config holds settings for creating an Adapter.
type Config struct {
	WorkDir       string
	Model         string
	SkipPermsFlag string
	OnToolUse     func(toolName, inputSummary string)
	OnLog         func(string)
	SessionReuse  SessionReuseConfig
}

// New returns an Adapter for the given name.
func New(name string, workDir string, opts ...func(*Config)) (Adapter, error) {
	cfg := Config{WorkDir: workDir}
	for _, o := range opts {
		o(&cfg)
	}
	switch name {
	case "claude-code", "":
		return &claudeAdapter{
			workDir:       cfg.WorkDir,
			model:         cfg.Model,
			skipPermsFlag: cfg.SkipPermsFlag,
			onToolUse:     cfg.OnToolUse,
			onLog:         cfg.OnLog,
			reuse:         cfg.SessionReuse,
		}, nil
	case "openai-codex", "codex":
		return &codexAdapter{
			workDir:       cfg.WorkDir,
			model:         cfg.Model,
			skipPermsFlag: cfg.SkipPermsFlag,
			onToolUse:     cfg.OnToolUse,
		}, nil
	default:
		return nil, fmt.Errorf("unknown adapter: %q", name)
	}
}

func DefaultAdapterForProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "claude", "anthropic":
		return "claude-code"
	case "codex", "openai":
		return "openai-codex"
	case "gemini", "google":
		return "gemini-cli"
	default:
		return ""
	}
}
