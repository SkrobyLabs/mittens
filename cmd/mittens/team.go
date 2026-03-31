package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"gopkg.in/yaml.v3"

	firewallext "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/firewall"
	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/adapter"
	"github.com/SkrobyLabs/mittens/internal/initcfg"
	"github.com/SkrobyLabs/mittens/internal/pool"
)

// sessionMeta holds metadata about a team session for status display.
type sessionMeta struct {
	Workspace    string    `json:"workspace"`
	SessionID    string    `json:"sessionId"`
	StartedAt    time.Time `json:"startedAt"`
	LastActivity time.Time `json:"lastActivity"`
	Running      bool      `json:"running"`
	WorkerCount  int       `json:"workerCount,omitempty"`
	ProjectDir   string    `json:"projectDir"`
}

// handleTeam dispatches the "mittens team" subcommand.
func handleTeam(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return handleTeamInit(args[1:])
		case "status":
			return handleTeamStatus(args[1:])
		case "resume":
			return handleTeamResume(args[1:])
		case "clean":
			return handleTeamClean(args[1:])
		case "help", "--help", "-h":
			return printTeamHelp()
		}
	}
	return runTeamSession(args)
}

// extractTeamName extracts --name VALUE from args and returns the name and remaining args.
func extractTeamName(args []string) (string, []string) {
	var remaining []string
	var name string
	for i := 0; i < len(args); i++ {
		if args[i] == "--name" && i+1 < len(args) {
			name = args[i+1]
			i++ // skip value
		} else if strings.HasPrefix(args[i], "--name=") {
			name = strings.TrimPrefix(args[i], "--name=")
		} else {
			remaining = append(remaining, args[i])
		}
	}
	return name, remaining
}

func expandedExtensionMounts(exts []*registry.Extension, home string, provider *Provider) []registry.ResolvedMount {
	if provider == nil {
		return nil
	}
	var mounts []registry.ResolvedMount
	for _, ext := range exts {
		if !ext.Enabled {
			continue
		}
		mounts = append(mounts, ext.ExpandedMounts(home, provider.HomePath())...)
	}
	return mounts
}

// runTeamSession launches a team session with a leader container.
func runTeamSession(args []string) error {
	workspace := detectWorkspace()
	projDir := ProjectDir(workspace)

	// Extract --name flag before forwarding to ParseFlags.
	teamName, args := extractTeamName(args)

	// Load team.yaml (returns defaults if file doesn't exist).
	teamConfigPath := filepath.Join(ConfigHome(), "projects", projDir, "team.yaml")
	tc, err := pool.LoadTeamConfig(teamConfigPath)
	if err != nil {
		logWarn("team config: %v (using defaults)", err)
		tc = &pool.TeamConfig{}
	}

	// Auto-prune stale pool state directories.
	poolsDir := filepath.Join(ConfigHome(), "projects", projDir, "pools")
	pruneStalePoolDirs(poolsDir)

	// Generate session ID.
	var sessionID string
	if teamName != "" {
		sessionID = teamName
	} else {
		sessionID = fmt.Sprintf("team-%d-%s", os.Getpid(), strconv.FormatInt(time.Now().Unix(), 36))
	}

	// Create state directory.
	stateDir := filepath.Join(poolsDir, sessionID)

	// Guard against collision (stale dir with existing WAL).
	if _, err := os.Stat(filepath.Join(stateDir, "events.jsonl")); err == nil {
		return fmt.Errorf("session %q already exists — use 'mittens team resume %s' or 'mittens team clean'", sessionID, sessionID)
	}

	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Persist session metadata (best-effort).
	metaPath := filepath.Join(stateDir, "session.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		meta := map[string]interface{}{
			"workspace": workspace,
			"sessionId": sessionID,
			"startedAt": time.Now().UTC().Format(time.RFC3339),
		}
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			os.WriteFile(metaPath, data, 0644)
		}
	}

	// Copy team.yaml into state dir so the container can read it.
	teamConfigDst := filepath.Join(stateDir, "team.yaml")
	if data, err := os.ReadFile(teamConfigPath); err == nil {
		os.WriteFile(teamConfigDst, data, 0644)
	}

	// Determine max workers from config.
	maxWorkers := 4
	if tc.MaxWorkers > 0 {
		maxWorkers = tc.MaxWorkers
	}

	// Parse remaining flags using the normal flow.
	app := &App{
		Provider:        DefaultProvider(),
		ImageName:       "mittens",
		ImageTag:        "latest",
		Yolo:            true,
		worktreeOrigins: make(map[string]string),
		worktreeRepos:   make(map[string]string),
	}

	// Load extensions.
	exts, err := loadExtensions()
	if err != nil {
		return fmt.Errorf("loading extensions: %w", err)
	}
	app.Extensions = exts

	// Set firewall defaults (same as main.go init flow).
	firewallext.DefaultConfPath = filepath.Join(containerDir(), "firewall.conf")
	firewallext.EmbeddedConf = embeddedFirewallConf
	firewallext.EmbeddedDevConf = embeddedFirewallDevConf

	// Load config: user defaults + project config + CLI args.
	userArgs, _ := LoadUserDefaults()
	configArgs, _ := LoadProjectConfig(workspace)
	merged := append(userArgs, configArgs...)
	merged = append(merged, args...)

	// Resolve provider.
	provider, err := resolveProviderFromArgs(merged)
	if err != nil {
		return err
	}
	app.Provider = provider

	if err := app.ParseFlags(merged); err != nil {
		return err
	}

	extraProviders, err := extraTeamProviders(tc, app.Provider)
	if err != nil {
		return err
	}
	app.teamExtraProviders = extraProviders

	// Team mode requires workers to have full write permissions.
	if !app.Yolo {
		return fmt.Errorf("--no-yolo is incompatible with team mode: workers require full permissions to operate")
	}

	// Resolve firewall conf once for worker mounts.
	for _, ext := range app.Extensions {
		if ext.Name == "firewall" && ext.Enabled {
			fwPath, err := firewallext.ResolveConfFile(ext.RawArg)
			if err != nil {
				logWarn("Firewall conf resolution: %v", err)
			} else {
				app.firewallConfPath = fwPath
				app.tempDirs = append(app.tempDirs, fwPath)
			}
			break
		}
	}

	// Team-specific overrides.
	app.ContainerName = "mittens-team-" + sessionID

	// Create plans directory (project-scoped, persists across sessions).
	plansDir := filepath.Join(ConfigHome(), "projects", projDir, "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		return fmt.Errorf("create plans dir: %w", err)
	}

	// Set team environment variables that get passed to the container.
	// These are read by team-mcp inside the leader container.
	teamEnv := map[string]string{
		"MITTENS_STATE_DIR":   stateDir,
		"MITTENS_SESSION_ID":  sessionID,
		"MITTENS_MAX_WORKERS": strconv.Itoa(maxWorkers),
		"MITTENS_TEAM_CONFIG": teamConfigDst,
		"MITTENS_PLANS_DIR":   plansDir,
	}

	// Store team state on the App for use in assembleDockerArgs.
	app.teamMode = true
	app.teamPlansDir = plansDir
	app.teamSessionID = sessionID
	app.teamStateDir = stateDir
	app.teamEnv = teamEnv
	app.teamMaxWorkers = maxWorkers
	app.teamSessionReuse = tc.SessionReuse

	logInfo("Team session: %s (max workers: %d)", sessionID, maxWorkers)

	// Ensure worker containers are cleaned up on any exit path.
	// sync.Once guards against the signal handler and defer racing.
	var once sync.Once
	cleanup := func() { once.Do(func() { cleanupTeamSession(stateDir, sessionID) }) }
	defer cleanup()

	// Install signal handler to clean up worker containers on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		cleanup()
	}()

	return app.Run()
}

// spawnWorkerContainer creates a worker Docker container for the team pool.
func (a *App) spawnWorkerContainer(spec pool.WorkerSpec) (string, string, error) {
	wid := spec.ID
	if wid == "" {
		return "", "", fmt.Errorf("spawn worker: spec.ID is required")
	}
	workerProvider, err := resolveWorkerProvider(spec.Provider, a.Provider)
	if err != nil {
		return "", "", fmt.Errorf("spawn worker: %w", err)
	}
	containerName := fmt.Sprintf("mittens-%s-%s", a.teamSessionID, wid)

	// Create per-worker team directory for file-based data exchange.
	workerDir := filepath.Join(a.teamStateDir, "workers", wid)
	if _, err := os.Stat(workerDir); err == nil {
		os.RemoveAll(workerDir) // stale data from dead worker must not leak
	}
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		return "", "", fmt.Errorf("create worker dir: %w", err)
	}
	os.Chmod(workerDir, 0777) // container drops to uid 1000, needs world-writable

	args := []string{
		"run", "-d",
		"--name", containerName,
		"-l", "mittens.pool=" + a.teamSessionID,
		"-l", "mittens.role=worker",
		"-l", "mittens.worker_id=" + wid,
		"-v", a.WorkspaceMountSrc + ":" + a.WorkspaceMountSrc,
		"-v", workerDir + ":/team",
		"--add-host=host.docker.internal:host-gateway",
	}

	// Pass worker-specific env vars.
	args = append(args, "-e", "MITTENS_WORKER_ID="+wid)
	args = append(args, "-e", "MITTENS_TEAM_DIR=/team")

	// Workers only need the leader's task broker, not the host broker.
	leaderPort, err := captureCommand("docker", "port", a.ContainerName, "8080")
	if err == nil && leaderPort != "" {
		// docker port output: "0.0.0.0:XXXXX" — extract port number.
		parts := strings.Split(strings.TrimSpace(leaderPort), ":")
		if len(parts) >= 2 {
			args = append(args, "-e", "MITTENS_LEADER_ADDR=host.docker.internal:"+parts[len(parts)-1])
		}
	}

	// Model/adapter env vars from spec.
	if spec.Adapter != "" {
		args = append(args, "-e", "MITTENS_ADAPTER="+spec.Adapter)
	}
	if spec.Model != "" {
		args = append(args, "-e", "MITTENS_MODEL="+spec.Model)
	}
	args = append(args, "-e", "MITTENS_PROVIDER="+workerProvider.Name)

	// API key and provider-specific env vars.
	if workerProvider.APIKeyEnv != "" {
		args = append(args, "-e", workerProvider.APIKeyEnv+"="+os.Getenv(workerProvider.APIKeyEnv))
	}
	if workerProvider.BaseURLEnv != "" && os.Getenv(workerProvider.BaseURLEnv) != "" {
		args = append(args, "-e", workerProvider.BaseURLEnv+"="+os.Getenv(workerProvider.BaseURLEnv))
	}
	for k, v := range workerProvider.ContainerEnv {
		args = append(args, "-e", k+"="+v)
	}

	// Per-worker environment variables from spec (includes MITTENS_WORKER_TOKEN).
	for k, v := range spec.Environment {
		args = append(args, "-e", k+"="+v)
	}

	// Session reuse configuration.
	if a.teamSessionReuse.Enabled {
		args = append(args, "-e", "MITTENS_SESSION_REUSE=1")
		ttl := a.teamSessionReuse.TTLSeconds
		if ttl <= 0 {
			ttl = 300
		}
		args = append(args, "-e", "MITTENS_SESSION_REUSE_TTL="+strconv.Itoa(ttl))
		maxTasks := a.teamSessionReuse.MaxTasks
		if maxTasks <= 0 {
			maxTasks = 3
		}
		args = append(args, "-e", "MITTENS_SESSION_REUSE_MAX_TASKS="+strconv.Itoa(maxTasks))
		maxTokens := a.teamSessionReuse.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 100000
		}
		args = append(args, "-e", "MITTENS_SESSION_REUSE_MAX_TOKENS="+strconv.Itoa(maxTokens))
		if !a.teamSessionReuse.SameRoleOnly {
			args = append(args, "-e", "MITTENS_SESSION_REUSE_SAME_ROLE=0")
		}
	}

	// Credential file mount.
	if credFile := a.workerCredentialFile(workerProvider); credFile != "" && workerProvider.CredentialFile != "" {
		args = append(args, "-v", credFile+":"+workerProvider.StagingCredentialPath()+":ro")
	}

	// AI config staging (read-only).
	home := os.Getenv("HOME")
	hostConfigDir := workerProvider.HostConfigDir(home)
	ensureDir(hostConfigDir)
	args = append(args, "-v", hostConfigDir+":"+workerProvider.StagingConfigDir()+":ro")
	if workerProvider.UserPrefsFile != "" {
		hostPrefs := workerProvider.HostUserPrefsPath(home)
		if _, err := os.Stat(hostPrefs); err == nil {
			args = append(args, "-v", hostPrefs+":"+workerProvider.StagingUserPrefsPath()+":ro")
		}
	}

	// Resource limits.
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}

	// Security hardening (matching leader — app.go:1350-1357).
	args = append(args,
		"--cap-drop", "ALL",
		"--cap-add", "SETUID",
		"--cap-add", "SETGID",
		"--security-opt", "no-new-privileges",
	)
	// Extension mounts, env vars, capabilities (matching leader — app.go assembleDockerArgs).
	for _, m := range expandedExtensionMounts(a.Extensions, home, workerProvider) {
		mountStr := m.Src + ":" + m.Dst
		if m.Mode != "" {
			mountStr += ":" + m.Mode
		}
		args = append(args, "-v", mountStr)
		for k, v := range m.Env {
			args = append(args, "-e", k+"="+v)
		}
	}
	for _, ext := range a.Extensions {
		if !ext.Enabled {
			continue
		}
		for k, v := range ext.Env {
			args = append(args, "-e", k+"="+v)
		}
		for _, cap := range ext.Capabilities {
			args = append(args, "--cap-add", cap)
		}
	}

	// Extra directory mounts (matching leader — app.go assembleDockerArgs).
	var resolvedExtraDirs []string
	wsInfo, wsStatErr := os.Stat(a.WorkspaceMountSrc)
	for _, dirSpec := range a.ExtraDirs {
		spec := parseExtraDirSpec(dirSpec)
		resolved, err := filepath.Abs(spec.Path)
		if err != nil {
			logWarn("worker --dir path not resolvable: %s", spec.Path)
			continue
		}
		resolvedInfo, err := os.Stat(resolved)
		if err != nil {
			logWarn("worker --dir path does not exist: %s", spec.Path)
			continue
		}
		if wsStatErr == nil && os.SameFile(resolvedInfo, wsInfo) {
			logVerbose(a.Verbose, "Skipping duplicate worker mount: %s (same as workspace)", spec.Path)
			continue
		}
		mount := resolved + ":" + resolved
		if spec.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
		resolvedExtraDirs = append(resolvedExtraDirs, resolved)
	}

	// Mount firewall.conf (resolved once in runTeamSession/handleTeamResume).
	if a.firewallConfPath != "" {
		args = append(args, "-v", a.firewallConfPath+":/mnt/mittens-staging/firewall.conf:ro")
	}

	// Build worker-specific mittens-init config and mount it.
	workerCfg := a.buildWorkerInitConfig(workerProvider, containerName)
	if len(resolvedExtraDirs) > 0 {
		workerCfg.ExtraDirs = resolvedExtraDirs
	}
	cfgFile, err := os.CreateTemp("", "mittens-worker-*.json")
	if err != nil {
		return "", "", fmt.Errorf("create worker config temp file: %w", err)
	}
	cfgPath := cfgFile.Name()
	cfgFile.Close()
	if err := workerCfg.Write(cfgPath); err != nil {
		os.Remove(cfgPath)
		return "", "", fmt.Errorf("write worker config: %w", err)
	}
	os.Chmod(cfgPath, 0644)
	a.tempDirs = append(a.tempDirs, cfgPath)
	args = append(args,
		"-v", cfgPath+":"+initcfg.ConfigPath+":ro",
		"-e", "MITTENS_CONFIG="+initcfg.ConfigPath,
	)

	image := a.ImageName + ":" + a.ImageTag
	args = append(args,
		"-e", "AI_USERNAME="+a.Provider.Username,
		image, workerProvider.Binary, "--print")

	cmd := exec.Command("docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("spawn worker container: %w", err)
	}
	containerID := strings.TrimSpace(string(out))
	return containerName, containerID, nil
}

// buildWorkerInitConfig creates a minimal initcfg for a worker container.
// Workers run in --print mode and don't need DinD, X11 clipboard, team MCP, or notification hooks.
func (a *App) buildWorkerInitConfig(provider *Provider, containerName string) *initcfg.ContainerConfig {
	// Check if the firewall extension is enabled and collect extra domains from extensions.
	firewallEnabled := false
	var firewallDomains []string
	for _, ext := range a.Extensions {
		if ext.Name == "firewall" && ext.Enabled {
			firewallEnabled = true
		}
		if ext.Enabled {
			firewallDomains = append(firewallDomains, ext.FirewallDomains()...)
		}
	}
	firewallDomains = append(firewallDomains, provider.FirewallDomains...)

	cfg := &initcfg.ContainerConfig{
		AI: initcfg.AIConfig{
			Binary:          provider.Binary,
			ConfigDir:       provider.ConfigDir,
			CredFile:        provider.CredentialFile,
			PrefsFile:       provider.UserPrefsFile,
			SettingsFile:    provider.SettingsFile,
			ProjectFile:     provider.ProjectFile,
			TrustedDirsKey:  provider.TrustedDirsKey,
			YoloKey:         provider.YoloKey,
			MCPServersKey:   provider.MCPServersKey,
			TrustedDirsFile: provider.TrustedDirsFile,
			InitSettingsJQ:  provider.InitSettingsJQ,
			StopHookEvent:   provider.StopHookEvent,
			PersistFiles:    provider.PersistFiles,
			SettingsFormat:  provider.SettingsFormat,
			ConfigSubdirs:   provider.ConfigSubdirs,
			PluginDir:       provider.PluginDir,
			PluginFiles:     provider.PluginFiles,
			SkipPermsFlag:   provider.SkipPermsFlag,
		},
		Flags: initcfg.Flags{
			Verbose:   a.Verbose,
			Yolo:      true,
			PrintMode: true,
			Firewall:  firewallEnabled,
			NoNotify:  true,
		},
		ContainerName: containerName,
		HostWorkspace: a.EffectiveWorkspace,
		Broker: initcfg.BrokerConfig{
			Port:  a.brokerPort,
			Token: a.brokerToken,
		},
	}
	if len(firewallDomains) > 0 {
		cfg.FirewallExtra = firewallDomains
	}
	return cfg
}

// killWorkerContainer stops and removes a worker container.
func (a *App) killWorkerContainer(workerID string) error {
	containerName := fmt.Sprintf("mittens-%s-%s", a.teamSessionID, workerID)
	// Send SIGTERM first for graceful shutdown, then force remove.
	exec.Command("docker", "stop", "-t", "10", containerName).Run()
	return exec.Command("docker", "rm", "-f", containerName).Run()
}

// cleanupTeamSession stops worker containers but preserves the state directory
// so the session can be resumed later. The auto-prune on startup and
// `mittens team clean` handle old state dir cleanup.
func cleanupTeamSession(stateDir, sessionID string) {
	// Gracefully stop remaining worker containers with our session label.
	out, err := captureCommand("docker", "ps", "-q", "--filter", "label=mittens.pool="+sessionID)
	if err == nil && out != "" {
		for _, cid := range strings.Fields(out) {
			exec.Command("docker", "stop", "-t", "10", cid).Run()
			exec.Command("docker", "rm", "-f", cid).Run()
		}
	}

	// Clean up ephemeral worker dirs (outputs stay for get_task_output).
	workersDir := filepath.Join(stateDir, "workers")
	os.RemoveAll(workersDir)
}

// handleTeamResume reconnects to an existing team session.
func handleTeamResume(args []string) error {
	workspace := detectWorkspace()
	projDir := ProjectDir(workspace)
	poolsDir := filepath.Join(ConfigHome(), "projects", projDir, "pools")

	// Split on "--" separator — everything after is forwarded to the provider.
	var resumeArgs, providerArgs []string
	for i, a := range args {
		if a == "--" {
			providerArgs = args[i+1:]
			break
		}
		resumeArgs = append(resumeArgs, a)
	}
	args = resumeArgs

	// Reject unknown flags and extra positional args.
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("unknown flag %q for \"mittens team resume\"", a)
		}
	}
	if len(args) > 1 {
		return fmt.Errorf("too many arguments for \"mittens team resume\" (expected at most one session ID)")
	}

	if len(args) == 0 {
		// List available sessions.
		entries, err := os.ReadDir(poolsDir)
		if err != nil {
			return fmt.Errorf("no sessions found: %w", err)
		}
		var sessions []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			walPath := filepath.Join(poolsDir, e.Name(), "events.jsonl")
			if _, err := os.Stat(walPath); err == nil {
				sessions = append(sessions, e.Name())
			}
		}
		if len(sessions) == 0 {
			return fmt.Errorf("no resumable sessions found")
		}
		fmt.Fprintln(os.Stderr, "Available sessions:")
		for _, s := range sessions {
			fmt.Fprintf(os.Stderr, "  %s\n", s)
		}
		return fmt.Errorf("usage: mittens team resume <session-id>")
	}

	sessionID := args[0]
	stateDir := filepath.Join(poolsDir, sessionID)

	// Verify state directory and WAL exist.
	walPath := filepath.Join(stateDir, "events.jsonl")
	if _, err := os.Stat(walPath); err != nil {
		return fmt.Errorf("session %q not found: no WAL at %s", sessionID, walPath)
	}

	// Check no leader is already running for this session.
	out, err := captureCommand("docker", "ps", "-q",
		"--filter", "label=mittens.pool="+sessionID,
		"--filter", "label=mittens.role=leader")
	if err == nil && strings.TrimSpace(out) != "" {
		return fmt.Errorf("leader already running for session %s", sessionID)
	}

	// Backfill session.json if missing (best-effort).
	metaPath := filepath.Join(stateDir, "session.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		meta := map[string]interface{}{
			"workspace": workspace,
			"sessionId": sessionID,
			"startedAt": time.Now().UTC().Format(time.RFC3339),
		}
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			os.WriteFile(metaPath, data, 0644)
		}
	}

	// Load team config from state dir or project config.
	teamConfigPath := filepath.Join(stateDir, "team.yaml")
	if _, err := os.Stat(teamConfigPath); err != nil {
		teamConfigPath = filepath.Join(ConfigHome(), "projects", projDir, "team.yaml")
	}

	tc, err := pool.LoadTeamConfig(teamConfigPath)
	if err != nil {
		logWarn("team config: %v (using defaults)", err)
		tc = &pool.TeamConfig{}
	}

	maxWorkers := 4
	if tc.MaxWorkers > 0 {
		maxWorkers = tc.MaxWorkers
	}

	// Build App with same setup as runTeamSession.
	app := &App{
		Provider:        DefaultProvider(),
		ImageName:       "mittens",
		ImageTag:        "latest",
		Yolo:            true,
		worktreeOrigins: make(map[string]string),
		worktreeRepos:   make(map[string]string),
	}

	exts, err := loadExtensions()
	if err != nil {
		return fmt.Errorf("loading extensions: %w", err)
	}
	app.Extensions = exts

	// Set firewall defaults (same as main.go init flow).
	firewallext.DefaultConfPath = filepath.Join(containerDir(), "firewall.conf")
	firewallext.EmbeddedConf = embeddedFirewallConf
	firewallext.EmbeddedDevConf = embeddedFirewallDevConf

	userArgs, _ := LoadUserDefaults()
	configArgs, _ := LoadProjectConfig(workspace)
	merged := append(userArgs, configArgs...)
	if len(providerArgs) > 0 {
		merged = append(merged, "--")
		merged = append(merged, providerArgs...)
	}

	provider, err := resolveProviderFromArgs(merged)
	if err != nil {
		return err
	}
	app.Provider = provider

	if err := app.ParseFlags(merged); err != nil {
		return err
	}

	extraProviders, err := extraTeamProviders(tc, app.Provider)
	if err != nil {
		return err
	}
	app.teamExtraProviders = extraProviders

	// Team mode requires workers to have full write permissions.
	if !app.Yolo {
		return fmt.Errorf("--no-yolo is incompatible with team mode: workers require full permissions to operate")
	}

	app.ContainerName = "mittens-team-" + sessionID

	// Resolve firewall conf once for worker mounts.
	for _, ext := range app.Extensions {
		if ext.Name == "firewall" && ext.Enabled {
			fwPath, err := firewallext.ResolveConfFile(ext.RawArg)
			if err != nil {
				logWarn("Firewall conf resolution: %v", err)
			} else {
				app.firewallConfPath = fwPath
				app.tempDirs = append(app.tempDirs, fwPath)
			}
			break
		}
	}

	// Create plans directory (project-scoped, persists across sessions).
	plansDir := filepath.Join(ConfigHome(), "projects", projDir, "plans")
	if err := os.MkdirAll(plansDir, 0755); err != nil {
		return fmt.Errorf("create plans dir: %w", err)
	}

	teamEnv := map[string]string{
		"MITTENS_STATE_DIR":   stateDir,
		"MITTENS_SESSION_ID":  sessionID,
		"MITTENS_MAX_WORKERS": strconv.Itoa(maxWorkers),
		"MITTENS_TEAM_CONFIG": teamConfigPath,
		"MITTENS_PLANS_DIR":   plansDir,
	}

	app.teamMode = true
	app.teamSessionID = sessionID
	app.teamStateDir = stateDir
	app.teamEnv = teamEnv
	app.teamMaxWorkers = maxWorkers
	app.teamPlansDir = plansDir
	app.teamSessionReuse = tc.SessionReuse

	logInfo("Resuming team session: %s (max workers: %d)", sessionID, maxWorkers)

	// Ensure worker containers are cleaned up on any exit path.
	var once sync.Once
	cleanup := func() { once.Do(func() { cleanupTeamSession(stateDir, sessionID) }) }
	defer cleanup()

	// Install signal handler to clean up worker containers on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		cleanup()
	}()

	return app.Run()
}

// handleTeamClean removes old pool state directories.
func handleTeamClean(args []string) error {
	workspace := detectWorkspace()
	projDir := ProjectDir(workspace)
	poolsDir := filepath.Join(ConfigHome(), "projects", projDir, "pools")

	entries, err := os.ReadDir(poolsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No pool state directories found.")
			return nil
		}
		return fmt.Errorf("read pools dir: %w", err)
	}

	all := false
	dryRun := false
	maxAge := 7 * 24 * time.Hour
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			all = true
		case "--dry-run":
			dryRun = true
		case "--older-than":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					return fmt.Errorf("invalid duration %q: %w", args[i], err)
				}
				maxAge = d
			}
		default:
			return fmt.Errorf("unknown flag %q for \"mittens team clean\" (supported: --all, --dry-run, --older-than)", args[i])
		}
	}

	running := runningTeamSessions()
	cutoff := time.Now().Add(-maxAge)
	removed := 0

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		if running[sid] {
			if dryRun {
				fmt.Fprintf(os.Stderr, "  skip (running): %s\n", sid)
			}
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		if !all && info.ModTime().After(cutoff) {
			if dryRun {
				fmt.Fprintf(os.Stderr, "  skip (recent): %s (modified %s)\n", sid, info.ModTime().Format(time.RFC3339))
			}
			continue
		}

		dirPath := filepath.Join(poolsDir, sid)
		if dryRun {
			fmt.Fprintf(os.Stderr, "  would remove: %s\n", sid)
		} else {
			os.RemoveAll(dirPath)
			fmt.Fprintf(os.Stderr, "  removed: %s\n", sid)
		}
		removed++
	}

	if removed == 0 {
		fmt.Println("Nothing to clean.")
	} else if dryRun {
		fmt.Fprintf(os.Stderr, "%d directories would be removed.\n", removed)
	} else {
		fmt.Fprintf(os.Stderr, "Removed %d session directories.\n", removed)
	}
	return nil
}

// pruneStalePoolDirs removes old pool state directories on startup.
// Keeps the newest 20 regardless of age. Above 20, prunes dirs older than 7 days.
// Skips dirs with running leader containers.
func pruneStalePoolDirs(poolsDir string) {
	entries, err := os.ReadDir(poolsDir)
	if err != nil {
		return
	}

	type dirEntry struct {
		name    string
		modTime time.Time
	}

	var dirs []dirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirEntry{name: e.Name(), modTime: info.ModTime()})
	}

	// Keep newest 20 — nothing to prune.
	if len(dirs) <= 20 {
		return
	}

	// Sort by mod time descending (newest first).
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].modTime.After(dirs[j].modTime)
	})

	running := runningTeamSessions()
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	// Skip the newest 20, prune the rest if older than 7 days and not running.
	for _, d := range dirs[20:] {
		if running[d.name] {
			continue
		}
		if d.modTime.Before(cutoff) {
			os.RemoveAll(filepath.Join(poolsDir, d.name))
		}
	}
}

// runningTeamSessions returns a set of session IDs that have a running leader container.
func runningTeamSessions() map[string]bool {
	out, _ := captureCommand("docker", "ps", "--filter", "label=mittens.role=leader", "--format", `{{.Label "mittens.pool"}}`)
	running := make(map[string]bool)
	for _, s := range strings.Fields(out) {
		running[s] = true
	}
	return running
}

// listAllTeamSessions discovers team sessions from disk and cross-references
// with running Docker containers. When allProjects is false, only the project
// matching currentProjectDir is scanned.
func listAllTeamSessions(configHome string, currentProjectDir string, allProjects bool) ([]sessionMeta, error) {
	projectsBase := filepath.Join(configHome, "projects")

	var projDirs []string
	if allProjects {
		entries, err := os.ReadDir(projectsBase)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("read projects dir: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				projDirs = append(projDirs, e.Name())
			}
		}
	} else {
		projDirs = []string{currentProjectDir}
	}

	running := runningTeamSessions()
	var sessions []sessionMeta

	for _, pd := range projDirs {
		poolsDir := filepath.Join(projectsBase, pd, "pools")
		entries, err := os.ReadDir(poolsDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sessionDir := filepath.Join(poolsDir, e.Name())
			walPath := filepath.Join(sessionDir, "events.jsonl")
			walInfo, err := os.Stat(walPath)
			if err != nil {
				continue // not a real session
			}

			meta := sessionMeta{
				SessionID:    e.Name(),
				ProjectDir:   pd,
				LastActivity: walInfo.ModTime(),
				Running:      running[e.Name()],
			}

			// Try session.json first.
			if data, err := os.ReadFile(filepath.Join(sessionDir, "session.json")); err == nil {
				var sj struct {
					Workspace string `json:"workspace"`
					SessionID string `json:"sessionId"`
					StartedAt string `json:"startedAt"`
				}
				if json.Unmarshal(data, &sj) == nil {
					meta.Workspace = sj.Workspace
					if sj.SessionID != "" {
						meta.SessionID = sj.SessionID
					}
					if t, err := time.Parse(time.RFC3339, sj.StartedAt); err == nil {
						meta.StartedAt = t
					}
				}
			}

			// Fallback: read first line of WAL for timestamp.
			if meta.StartedAt.IsZero() {
				if f, err := os.Open(walPath); err == nil {
					defer f.Close()
					scanner := bufio.NewScanner(f)
					if scanner.Scan() {
						var ev struct {
							Ts time.Time `json:"ts"`
						}
						if json.Unmarshal(scanner.Bytes(), &ev) == nil {
							meta.StartedAt = ev.Ts
						}
					}
				}
			}

			// Fallback workspace from project dir name.
			if meta.Workspace == "" {
				meta.Workspace = pd
			}

			sessions = append(sessions, meta)
		}
	}

	// Sort: running first, then by lastActivity descending.
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Running != sessions[j].Running {
			return sessions[i].Running
		}
		return sessions[i].LastActivity.After(sessions[j].LastActivity)
	})

	return sessions, nil
}

// handleTeamInit runs the team configuration wizard.
func handleTeamInit(args []string) error {
	workspace := detectWorkspace()
	projDir := ProjectDir(workspace)
	configPath := filepath.Join(ConfigHome(), "projects", projDir, "team.yaml")

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardTitle.Render("Team Mode Configuration"))
	fmt.Fprintln(os.Stderr, wizardDim.Render("  Project: "+workspace))
	fmt.Fprintln(os.Stderr)

	// Max workers.
	maxWorkersStr := "4"
	if err := huh.NewInput().
		Title("Max concurrent workers").
		Placeholder("4").
		Value(&maxWorkersStr).
		Run(); err != nil {
		return gracefulAbort(err)
	}
	maxWorkers, err := strconv.Atoi(strings.TrimSpace(maxWorkersStr))
	if err != nil || maxWorkers < 1 {
		maxWorkers = 4
	}

	// Model routing.
	configRouting := false
	if err := huh.NewConfirm().
		Title("Configure per-role model routing?").
		Value(&configRouting).
		Run(); err != nil {
		return gracefulAbort(err)
	}

	models := map[string]pool.ModelConfig{}
	if configRouting {
		for _, role := range []string{"planner", "implementer", "reviewer"} {
			provider := ""
			model := ""
			adapterName := ""

			if err := huh.NewInput().
				Title(fmt.Sprintf("%s — provider", titleCase(role))).
				Placeholder("claude").
				Value(&provider).
				Run(); err != nil {
				return gracefulAbort(err)
			}
			provider = strings.TrimSpace(provider)
			modelPlaceholder := defaultTeamModel(provider)
			if err := huh.NewInput().
				Title(fmt.Sprintf("%s — model", titleCase(role))).
				Placeholder(modelPlaceholder).
				Value(&model).
				Run(); err != nil {
				return gracefulAbort(err)
			}
			adapterPlaceholder := adapter.DefaultAdapterForProvider(provider)
			if err := huh.NewInput().
				Title(fmt.Sprintf("%s — adapter", titleCase(role))).
				Placeholder(adapterPlaceholder).
				Value(&adapterName).
				Run(); err != nil {
				return gracefulAbort(err)
			}

			if provider != "" || model != "" || adapterName != "" {
				models[role] = pool.ModelConfig{
					Provider: provider,
					Model:    strings.TrimSpace(model),
					Adapter:  strings.TrimSpace(adapterName),
				}
			}
		}
	}

	// Build team config.
	cfg := teamYAMLConfig{
		MaxWorkers: maxWorkers,
	}
	if len(models) > 0 {
		cfg.Models = models
	}

	// Write team.yaml.
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal team config: %w", err)
	}

	header := "# mittens team config — generated by mittens team init\n"
	if err := os.WriteFile(configPath, append([]byte(header), data...), 0644); err != nil {
		return fmt.Errorf("write team config: %w", err)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("Saved to "+configPath))
	return nil
}

// teamYAMLConfig is the serialization format for team.yaml.
type teamYAMLConfig struct {
	MaxWorkers int                         `yaml:"max_workers,omitempty"`
	Models     map[string]pool.ModelConfig `yaml:"models,omitempty"`
}

func resolveWorkerProvider(name string, fallback *Provider) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return fallback, nil
	}
	return providerByName(strings.TrimSpace(name))
}

func extraTeamProviders(tc *pool.TeamConfig, primary *Provider) ([]*Provider, error) {
	if tc == nil || len(tc.Models) == 0 {
		return nil, nil
	}
	var providers []*Provider
	seen := map[string]bool{}
	for _, cfg := range tc.Models {
		if strings.TrimSpace(cfg.Provider) == "" {
			continue
		}
		p, err := providerByName(strings.TrimSpace(cfg.Provider))
		if err != nil {
			return nil, fmt.Errorf("invalid team provider %q: %w", cfg.Provider, err)
		}
		if primary != nil && p.Name == primary.Name {
			continue
		}
		if seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		providers = append(providers, p)
	}
	return providers, nil
}

func defaultTeamModel(provider string) string {
	switch canonicalProviderName(provider) {
	case "codex":
		return "gpt-5.3-spark"
	case "gemini":
		return "gemini-2.5-pro"
	default:
		return "claude-sonnet-4-6"
	}
}

// handleTeamStatus lists running team sessions.
func handleTeamStatus(args []string) error {
	// Parse flags.
	allProjects := false
	limit := 10
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all-projects":
			allProjects = true
		case "--json":
			jsonOutput = true
		case "--limit":
			i++
			if i >= len(args) {
				return fmt.Errorf("--limit requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return fmt.Errorf("--limit must be a non-negative integer, got %q", args[i])
			}
			limit = n
		default:
			if strings.HasPrefix(args[i], "--limit=") {
				v := strings.TrimPrefix(args[i], "--limit=")
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return fmt.Errorf("--limit must be a non-negative integer, got %q", v)
				}
				limit = n
				continue
			}
			return fmt.Errorf("unknown flag %q for \"mittens team status\"", args[i])
		}
	}

	workspace := detectWorkspace()
	projDir := ProjectDir(workspace)
	configHome := ConfigHome()

	sessions, err := listAllTeamSessions(configHome, projDir, allProjects)
	if err != nil {
		return err
	}

	// Separate running and stopped sessions.
	var running, stopped []sessionMeta
	for _, s := range sessions {
		if s.Running {
			running = append(running, s)
		} else {
			stopped = append(stopped, s)
		}
	}

	// Query worker count for running sessions.
	for i := range running {
		out, err := captureCommand("docker", "ps",
			"--filter", "label=mittens.pool="+running[i].SessionID,
			"--filter", "label=mittens.role=worker",
			"--format", "{{.ID}}")
		if err == nil {
			lines := strings.Fields(strings.TrimSpace(out))
			running[i].WorkerCount = len(lines)
		}
	}

	// Apply limit to stopped sessions (0 means no limit).
	if limit > 0 && limit < len(stopped) {
		stopped = stopped[:limit]
	}

	// Combine: running first, then stopped (already sorted by listAllTeamSessions).
	display := append(running, stopped...)

	if jsonOutput {
		if display == nil {
			display = []sessionMeta{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(display)
	}

	if len(display) == 0 {
		fmt.Println("No team sessions found. Run 'mittens team' to start one.")
		return nil
	}

	const timeFmt = "2006-01-02 15:04"
	fmt.Printf("%-28s %-18s %-10s %-18s %-18s %s\n", "PROJECT", "SESSION", "STATUS", "STARTED", "LAST ACTIVITY", "WORKERS")
	for _, s := range display {
		status := "stopped"
		if s.Running {
			status = "running"
		}
		started := "-"
		if !s.StartedAt.IsZero() {
			started = s.StartedAt.Local().Format(timeFmt)
		}
		activity := s.LastActivity.Local().Format(timeFmt)
		workers := "-"
		if s.Running {
			workers = strconv.Itoa(s.WorkerCount)
		}
		fmt.Printf("%-28s %-18s %-10s %-18s %-18s %s\n", s.ProjectDir, s.SessionID, status, started, activity, workers)
	}
	return nil
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// printTeamHelp prints the team subcommand help.
func printTeamHelp() error {
	fmt.Println(`mittens team - Launch and manage team AI sessions

Usage: mittens team [command] [flags] [-- provider-args...]

Commands:
  (none)     Launch a team session with a leader container
  init       Interactive team configuration wizard
  status     Show team sessions (--all-projects, --limit N, --json)
  resume     Reconnect to a crashed team session
  clean      Remove old session state directories

Team flags:
  --name NAME    Give the session a human-readable name for easy resume

Other flags are forwarded to the leader container (same as normal mittens flags).
Everything after -- is passed through to the AI provider unchanged.

Configuration:
  Team config is stored at ~/.mittens/projects/<workspace>/team.yaml
  Run 'mittens team init' to create or edit it.

Examples:
  mittens team                          Launch a team session
  mittens team --name refactor-auth     Launch a named session
  mittens team init                     Configure team settings
  mittens team status                   List team sessions for current project
  mittens team status --all-projects    List sessions across all projects
  mittens team status --limit 5         Show at most 5 stopped sessions
  mittens team status --json            Output as JSON
  mittens team resume refactor-auth     Resume a named session
  mittens team clean                    Prune sessions older than 7 days
  mittens team clean --all              Remove all non-running sessions
  mittens team clean --dry-run          Preview what would be removed`)
	return nil
}
