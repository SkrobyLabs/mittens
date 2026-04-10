package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeServeProvider(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "claude", want: "claude", ok: true},
		{in: "anthropic", want: "claude", ok: true},
		{in: "codex", want: "codex", ok: true},
		{in: "openai", want: "codex", ok: true},
		{in: "gemini", want: "gemini", ok: true},
		{in: "google", want: "gemini", ok: true},
		{in: "nope", ok: false},
	}
	for _, tc := range tests {
		got, err := normalizeServeProvider(tc.in)
		if tc.ok {
			if err != nil {
				t.Fatalf("normalizeServeProvider(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeServeProvider(%q) = %q, want %q", tc.in, got, tc.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("normalizeServeProvider(%q) unexpectedly succeeded with %q", tc.in, got)
		}
	}
}

func TestParseDaemonStartupLine(t *testing.T) {
	state := &supervisedDaemon{}
	parseDaemonStartupLine("MITTENS_BROKER_TOKEN=broker-secret", state)
	parseDaemonStartupLine("MITTENS_POOL_TOKEN=pool-secret", state)
	if state.brokerToken != "broker-secret" {
		t.Fatalf("brokerToken = %q, want broker-secret", state.brokerToken)
	}
	if state.poolToken != "pool-secret" {
		t.Fatalf("poolToken = %q, want pool-secret", state.poolToken)
	}
}

func TestCleanupSupervisedDaemonRemovesStaleFiles(t *testing.T) {
	paths := newKitchenTestPaths(t)
	provider := "codex"
	if err := os.MkdirAll(runtimeSupervisorDir(paths), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pidPath := supervisedRuntimePIDPath(paths, provider)
	socketPath := supervisedRuntimeSocketPath(paths, provider)
	if err := os.WriteFile(pidPath, []byte("0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile pid: %v", err)
	}
	if err := os.WriteFile(socketPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile socket: %v", err)
	}
	if err := cleanupSupervisedDaemon(paths, provider); err != nil {
		t.Fatalf("cleanupSupervisedDaemon: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file still exists, stat err=%v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists, stat err=%v", err)
	}
}

func TestWaitForSupervisedDaemonReady(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer closeFn()

	state := &supervisedDaemon{
		socketPath:  socketPath,
		brokerToken: "broker-secret",
		poolToken:   "pool-secret",
		waitCh:      make(chan error, 1),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForSupervisedDaemonReady(ctx, state); err != nil {
		t.Fatalf("waitForSupervisedDaemonReady: %v", err)
	}
}

func TestWaitForSupervisedDaemonReadyFailsWhenProcessExits(t *testing.T) {
	state := &supervisedDaemon{
		socketPath:  "/tmp/nowhere.sock",
		brokerToken: "broker-secret",
		poolToken:   "pool-secret",
		waitCh:      make(chan error, 1),
	}
	state.waitCh <- context.DeadlineExceeded
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForSupervisedDaemonReady(ctx, state); err == nil {
		t.Fatal("waitForSupervisedDaemonReady unexpectedly succeeded")
	}
}

func TestConfiguredServeProvidersDeduplicatesAndNormalizesRoutingProviders(t *testing.T) {
	providers, err := configuredServeProviders(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityTrivial: {
				Prefer: []PoolKey{
					{Provider: "anthropic", Model: "haiku"},
				},
			},
			ComplexityMedium: {
				Prefer: []PoolKey{
					{Provider: "openai", Model: "gpt-5.4"},
				},
				Fallback: []PoolKey{
					{Provider: "claude", Model: "opus"},
				},
			},
			ComplexityHigh: {
				Prefer: []PoolKey{
					{Provider: "google", Model: "gemini-2.5-pro"},
				},
			},
		},
		RoleRouting: map[string]map[Complexity]RoutingRule{
			"reviewer": {
				ComplexityCritical: {
					Prefer: []PoolKey{
						{Provider: "codex", Model: "gpt-5.4"},
					},
					Fallback: []PoolKey{
						{Provider: "gemini", Model: "gemini-2.5-pro"},
					},
				},
			},
		},
		RoleDefaults: map[string]RoutingRule{
			"implementer": {
				Prefer: []PoolKey{
					{Provider: "anthropic", Model: "opus"},
				},
			},
		},
		CouncilSeats: map[string]CouncilSeatRoutingConfig{
			"B": {
				Default: RoutingRule{
					Prefer: []PoolKey{
						{Provider: "google", Model: "gemini-2.5-pro"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("configuredServeProviders: %v", err)
	}
	want := []string{"claude", "codex", "gemini"}
	if len(providers) != len(want) {
		t.Fatalf("providers = %v, want %v", providers, want)
	}
	for i := range want {
		if providers[i] != want[i] {
			t.Fatalf("providers = %v, want %v", providers, want)
		}
	}
}

func TestConfiguredServeProvidersIncludesCouncilSeatOnlyProviders(t *testing.T) {
	providers, err := configuredServeProviders(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityTrivial:  {Prefer: []PoolKey{{Provider: "anthropic", Model: "haiku"}}},
			ComplexityLow:      {Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}}},
			ComplexityMedium:   {Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}}},
			ComplexityHigh:     {Prefer: []PoolKey{{Provider: "anthropic", Model: "opus"}}},
			ComplexityCritical: {Prefer: []PoolKey{{Provider: "anthropic", Model: "opus"}}},
		},
		CouncilSeats: map[string]CouncilSeatRoutingConfig{
			"B": {
				Default: RoutingRule{
					Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("configuredServeProviders: %v", err)
	}
	if len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
		t.Fatalf("providers = %v, want [claude codex]", providers)
	}
}

func TestSupervisedPoolStateUsesKitchenProjectPoolDir(t *testing.T) {
	paths := newKitchenTestPaths(t)
	project, err := paths.Project("/tmp/example-repo")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if got := supervisedPoolSession(project); got != "kitchen-"+project.Key {
		t.Fatalf("supervisedPoolSession = %q, want %q", got, "kitchen-"+project.Key)
	}
	if got := supervisedPoolStateDir(project); got != filepath.Join(project.PoolsDir, defaultPoolStateName) {
		t.Fatalf("supervisedPoolStateDir = %q, want %q", got, filepath.Join(project.PoolsDir, defaultPoolStateName))
	}
}
