package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const defaultPoolStateName = "default"

func openKitchen(repoPath string) (*Kitchen, func() error, error) {
	return openKitchenWithOptions(repoPath, kitchenOpenOptions{})
}

type kitchenOpenOptions struct {
	hostAPI         pool.RuntimeAPI
	hostPool        []PoolKey
	keepDeadWorkers bool
}

func openKitchenWithOptions(repoPath string, opts kitchenOpenOptions) (*Kitchen, func() error, error) {
	paths, err := DefaultKitchenPaths()
	if err != nil {
		return nil, nil, err
	}
	if err := paths.Ensure(); err != nil {
		return nil, nil, err
	}

	cfg, err := LoadKitchenConfig(paths.ConfigPath)
	if err != nil {
		return nil, nil, err
	}

	repoRoot, err := resolveRepoRoot(repoPath)
	if err != nil {
		return nil, nil, err
	}
	project, err := paths.Project(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	if err := project.Ensure(); err != nil {
		return nil, nil, err
	}

	poolStateDir := filepath.Join(project.PoolsDir, defaultPoolStateName)
	if err := os.MkdirAll(poolStateDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("create pool state dir: %w", err)
	}

	wal, err := pool.OpenWAL(filepath.Join(poolStateDir, "events.jsonl"))
	if err != nil {
		return nil, nil, err
	}

	health, err := NewProviderHealth(filepath.Join(project.RootDir, "provider_health.json"))
	if err != nil {
		_ = wal.Close()
		return nil, nil, err
	}

	hostAPI := opts.hostAPI
	if hostAPI == nil {
		hostAPI = kitchenHostAPIFromEnv()
	}
	hostPool := append([]PoolKey(nil), opts.hostPool...)
	if len(hostPool) == 0 {
		hostPool = kitchenHostPoolFromEnv()
	}
	pm, err := pool.RecoverPoolManager(pool.PoolConfig{
		SessionID:  "kitchen-" + project.Key,
		MaxWorkers: cfg.Concurrency.MaxWorkersTotal,
		StateDir:   poolStateDir,
	}, wal, hostAPI)
	if err != nil {
		_ = wal.Close()
		return nil, nil, err
	}

	k := &Kitchen{
		pm:              pm,
		wal:             wal,
		hostAPI:         hostAPI,
		router:          NewComplexityRouter(cfg, health, hostPool...),
		health:          health,
		planStore:       NewPlanStore(project.PlansDir),
		lineageMgr:      NewLineageManager(project.LineagesDir, project.PlansDir),
		cfg:             cfg,
		repoPath:        repoRoot,
		paths:           paths,
		project:         project,
		keepDeadWorkers: opts.keepDeadWorkers,
	}
	return k, wal.Close, nil
}

func resolveRepoRoot(repoPath string) (string, error) {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	root, err := runGit(absRepo, "rev-parse", "--show-toplevel")
	if err == nil {
		return strings.TrimSpace(root), nil
	}
	return absRepo, nil
}

func kitchenHostAPIFromEnv() pool.RuntimeAPI {
	runtimeSocket := strings.TrimSpace(os.Getenv("MITTENS_RUNTIME_SOCKET"))
	brokerToken := strings.TrimSpace(os.Getenv("MITTENS_BROKER_TOKEN"))
	poolToken := strings.TrimSpace(os.Getenv("MITTENS_POOL_TOKEN"))
	if runtimeSocket != "" && poolToken != "" {
		return newRuntimeClient(runtimeSocket, brokerToken, poolToken)
	}
	if meta, ok := loadRuntimeMetadata(); ok {
		return newRuntimeClient(meta.SocketPath, meta.BrokerToken, meta.PoolToken)
	}
	return nil
}

func kitchenHostPoolFromEnv() []PoolKey {
	meta, ok := loadRuntimeMetadata()
	if !ok || strings.TrimSpace(meta.Provider) == "" {
		return nil
	}
	return []PoolKey{{
		Provider: strings.TrimSpace(meta.Provider),
		Model:    strings.TrimSpace(meta.Model),
	}}
}

func (k *Kitchen) gitManager() (*GitManager, error) {
	if k == nil {
		return nil, fmt.Errorf("kitchen not configured")
	}
	return NewGitManager(k.repoPath, k.paths.WorktreesDir)
}
