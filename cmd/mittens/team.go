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
	"regexp"
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
	"github.com/SkrobyLabs/mittens/internal/initcfg"
	"github.com/SkrobyLabs/mittens/internal/pool"
)

// sessionMeta holds metadata about a team session for status display.
type sessionMeta struct {
	Workspace    string    `json:"workspace"`
	TeamName     string    `json:"teamName,omitempty"`
	SessionID    string    `json:"sessionId"`
	StartedAt    time.Time `json:"startedAt"`
	LastActivity time.Time `json:"lastActivity"`
	Running      bool      `json:"running"`
	WorkerCount  int       `json:"workerCount,omitempty"`
	ProjectDir   string    `json:"projectDir"`
}

type teamConfigMeta struct {
	Name         string `json:"name"`
	ConfigPath   string `json:"configPath"`
	SessionCount int    `json:"sessionCount"`
}

// handleTeam dispatches the "mittens team" subcommand.
func handleTeam(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return handleTeamInit(args[1:])
		case "list":
			return handleTeamList(args[1:])
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

func teamConfigRootDir() string {
	return filepath.Join(ConfigHome(), "teams")
}

func teamConfigDir(teamName string) string {
	return filepath.Join(teamConfigRootDir(), teamName)
}

func teamConfigPath(teamName string) string {
	return filepath.Join(teamConfigDir(teamName), "team.yaml")
}

func listTeamConfigs(configHome string) ([]teamConfigMeta, error) {
	root := filepath.Join(configHome, "teams")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read teams dir: %w", err)
	}

	sessions, err := listAllTeamSessions(configHome, "", true)
	if err != nil {
		return nil, err
	}
	sessionCounts := map[string]int{}
	for _, session := range sessions {
		if session.TeamName != "" {
			sessionCounts[session.TeamName]++
		}
	}

	var teams []teamConfigMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		configPath := filepath.Join(configHome, "teams", name, "team.yaml")
		if _, err := os.Stat(configPath); err != nil {
			continue
		}
		teams = append(teams, teamConfigMeta{
			Name:         name,
			ConfigPath:   configPath,
			SessionCount: sessionCounts[name],
		})
	}

	sort.Slice(teams, func(i, j int) bool {
		return teams[i].Name < teams[j].Name
	})
	return teams, nil
}

func validateTeamName(teamName string) error {
	if strings.TrimSpace(teamName) == "" {
		return fmt.Errorf("team name is required")
	}
	if err := pool.ValidateSessionID(teamName); err != nil {
		return fmt.Errorf("invalid team name %q: %w", teamName, err)
	}
	return nil
}

func generateTeamSessionID(teamName string) string {
	return fmt.Sprintf("%s-%s", teamName, strconv.FormatInt(time.Now().UnixNano(), 36))
}

func defaultTeamInitProvider() string {
	userArgs, err := LoadUserDefaults()
	if err != nil {
		return DefaultProvider().Name
	}
	provider, err := resolveProviderFromArgs(userArgs)
	if err != nil || provider == nil || strings.TrimSpace(provider.Name) == "" {
		return DefaultProvider().Name
	}
	return provider.Name
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
	if err := validateTeamName(teamName); err != nil {
		return fmt.Errorf("%w — launch an existing team with 'mittens team --name <team>' or create one with 'mittens team init --name <team>'", err)
	}

	// Load the named global team config. Launch is explicit: new teams must be
	// configured first via `mittens team init --name <team>`.
	teamConfigSrc := teamConfigPath(teamName)
	if _, err := os.Stat(teamConfigSrc); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("team %q is not configured — run 'mittens team init --name %s' first", teamName, teamName)
		}
		return fmt.Errorf("stat team config: %w", err)
	}
	tc, err := pool.LoadTeamConfig(teamConfigSrc)
	if err != nil {
		return fmt.Errorf("load team config: %w", err)
	}

	// Auto-prune stale pool state directories.
	poolsDir := filepath.Join(ConfigHome(), "projects", projDir, "pools")
	pruneStalePoolDirs(poolsDir)

	// Generate a unique runtime session ID distinct from the persistent team name.
	sessionID := generateTeamSessionID(teamName)
	if err := pool.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("invalid team session name %q: %w", sessionID, err)
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
			"teamName":  teamName,
			"sessionId": sessionID,
			"startedAt": time.Now().UTC().Format(time.RFC3339),
		}
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			os.WriteFile(metaPath, data, 0644)
		}
	}

	// Copy the named team config into state dir so the container resumes
	// against the launch-time snapshot even if the named team changes later.
	teamConfigDst := filepath.Join(stateDir, "team.yaml")
	data, err := os.ReadFile(teamConfigSrc)
	if err != nil {
		return fmt.Errorf("read team config: %w", err)
	}
	if err := os.WriteFile(teamConfigDst, data, 0644); err != nil {
		return fmt.Errorf("copy team config: %w", err)
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

	logInfo("Team session: %s (team %s, max workers: %d)", sessionID, teamName, maxWorkers)

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

type discoveredNamedContainer struct {
	pool.ContainerInfo
	Name string
}

func discoverExactNameContainers(containerName string) ([]discoveredNamedContainer, error) {
	out, err := captureCommand("docker", "ps", "-a",
		"--filter", "name=^/"+regexp.QuoteMeta(containerName)+"$",
		"--format", `{{.ID}}\t{{.Names}}\t{{.State}}\t{{.Status}}`)
	if err != nil {
		return nil, err
	}

	var containers []discoveredNamedContainer
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 || parts[1] != containerName {
			continue
		}
		containers = append(containers, discoveredNamedContainer{
			ContainerInfo: pool.ContainerInfo{
				ContainerID: parts[0],
				State:       parts[2],
				Status:      parts[3],
			},
			Name: parts[1],
		})
	}
	return containers, nil
}

func removeStaleExactNameContainers(containerName string) error {
	containers, err := discoverExactNameContainers(containerName)
	if err != nil {
		return fmt.Errorf("discover existing container %q: %w", containerName, err)
	}

	for _, c := range containers {
		if c.IsRunning() {
			state := strings.TrimSpace(c.State)
			if state == "" {
				state = strings.TrimSpace(c.Status)
			}
			if state == "" {
				state = "active"
			}
			return fmt.Errorf("worker container %q already exists in %q state", containerName, state)
		}
	}

	for _, c := range containers {
		target := c.ContainerID
		if target == "" {
			target = c.Name
		}
		if err := exec.Command("docker", "rm", "-f", target).Run(); err != nil {
			return fmt.Errorf("remove stale container %q: %w", containerName, err)
		}
	}

	return nil
}

func discoverSessionWorkerContainers(sessionID string) ([]pool.ContainerInfo, error) {
	out, err := captureCommand("docker", "ps", "-a",
		"--filter", "label=mittens.pool="+sessionID,
		"--filter", "label=mittens.role=worker",
		"--format", `{{.ID}}\t{{.Label "mittens.worker_id"}}\t{{.State}}\t{{.Status}}`)
	if err != nil {
		return nil, err
	}

	var containers []pool.ContainerInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		containers = append(containers, pool.ContainerInfo{
			ContainerID: parts[0],
			WorkerID:    parts[1],
			State:       parts[2],
			Status:      parts[3],
		})
	}
	return containers, nil
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
	if err := removeStaleExactNameContainers(containerName); err != nil {
		return "", "", fmt.Errorf("spawn worker: %w", err)
	}

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

// cleanupTeamSession removes worker containers in any state but preserves the
// state directory so the session can be resumed later. The auto-prune on startup and
// `mittens team clean` handle old state dir cleanup.
func cleanupTeamSession(stateDir, sessionID string) {
	containers, err := discoverSessionWorkerContainers(sessionID)
	if err == nil {
		for _, c := range containers {
			if c.ContainerID == "" {
				continue
			}
			if c.IsRunning() {
				exec.Command("docker", "stop", "-t", "10", c.ContainerID).Run()
			}
			exec.Command("docker", "rm", "-f", c.ContainerID).Run()
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
	resumeTeamName := ""
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		meta := map[string]interface{}{
			"workspace": workspace,
			"sessionId": sessionID,
			"startedAt": time.Now().UTC().Format(time.RFC3339),
		}
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			os.WriteFile(metaPath, data, 0644)
		}
	} else if data, err := os.ReadFile(metaPath); err == nil {
		var meta struct {
			TeamName string `json:"teamName"`
		}
		if json.Unmarshal(data, &meta) == nil {
			resumeTeamName = meta.TeamName
		}
	}

	// Load team config from state dir or named team config.
	teamConfigFile := filepath.Join(stateDir, "team.yaml")
	if _, err := os.Stat(teamConfigFile); err != nil {
		if resumeTeamName != "" {
			teamConfigFile = teamConfigPath(resumeTeamName)
		} else {
			return fmt.Errorf("resume team session: missing team config snapshot at %s", teamConfigFile)
		}
	}

	tc, err := pool.LoadTeamConfig(teamConfigFile)
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
		"MITTENS_TEAM_CONFIG": teamConfigFile,
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
					TeamName  string `json:"teamName"`
					SessionID string `json:"sessionId"`
					StartedAt string `json:"startedAt"`
				}
				if json.Unmarshal(data, &sj) == nil {
					meta.Workspace = sj.Workspace
					meta.TeamName = sj.TeamName
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
				if startedAt, ok := readSessionStartedAtFromWAL(walPath); ok {
					meta.StartedAt = startedAt
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

func readSessionStartedAtFromWAL(walPath string) (time.Time, bool) {
	f, err := os.Open(walPath)
	if err != nil {
		return time.Time{}, false
	}
	scanner := bufio.NewScanner(f)
	var startedAt time.Time
	if scanner.Scan() {
		var ev struct {
			Ts time.Time `json:"ts"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) == nil {
			startedAt = ev.Ts
		}
	}
	_ = f.Close()
	if startedAt.IsZero() {
		return time.Time{}, false
	}
	return startedAt, true
}

const (
	teamConfigDefaultMaxWorkers = 4
	teamConfigFileHeader        = "# mittens team config — generated by mittens team init\n"
)

// teamConfigRoleSpec describes one editable team role.
type teamConfigRoleSpec struct {
	Key     string
	Label   string
	Summary string
}

// teamConfigRoleInput holds editable fields for a role route.
type teamConfigRoleInput struct {
	Provider string
	Model    string
}

type teamModelMode struct {
	Key   string
	Label string
}

// teamConfigEditor isolates team.yaml defaults, normalization, and persistence
// from the interactive wizard flow.
type teamConfigEditor struct {
	MaxWorkers   int
	Models       map[string]teamConfigRoleInput
	SessionReuse pool.SessionReuseConfig
}

func newTeamConfigEditor() *teamConfigEditor {
	return &teamConfigEditor{
		MaxWorkers:   teamConfigDefaultMaxWorkers,
		Models:       map[string]teamConfigRoleInput{},
		SessionReuse: pool.DefaultSessionReuseConfig(),
	}
}

func loadTeamConfigEditor(path string) (*teamConfigEditor, error) {
	cfg, err := pool.LoadTeamConfig(path)
	if err != nil {
		return nil, err
	}
	return newTeamConfigEditorFromPool(cfg), nil
}

func newTeamConfigEditorFromPool(cfg *pool.TeamConfig) *teamConfigEditor {
	editor := newTeamConfigEditor()
	if cfg == nil {
		return editor
	}
	editor.MaxWorkers = normalizeTeamMaxWorkers(cfg.MaxWorkers)
	editor.SessionReuse = cfg.SessionReuse
	if editor.SessionReuse.TTLSeconds == 0 && editor.SessionReuse.MaxTasks == 0 && editor.SessionReuse.MaxTokens == 0 {
		editor.SessionReuse = pool.DefaultSessionReuseConfig()
	}
	for role, modelCfg := range cfg.Models {
		editor.SetModel(role, teamConfigRoleInput{
			Provider: teamConfigProviderFromModel(modelCfg),
			Model:    modelCfg.Model,
		})
	}
	return editor
}

func (e *teamConfigEditor) SetMaxWorkers(raw string) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		n = 0
	}
	e.MaxWorkers = normalizeTeamMaxWorkers(n)
}

func (e *teamConfigEditor) Model(role string) teamConfigRoleInput {
	if e == nil || len(e.Models) == 0 {
		return teamConfigRoleInput{}
	}
	return e.Models[normalizeTeamRoleKey(role)]
}

func (e *teamConfigEditor) SetModel(role string, input teamConfigRoleInput) {
	role = normalizeTeamRoleKey(role)
	if role == "" {
		return
	}
	cfg, ok := normalizeTeamModelInput(input)
	if !ok {
		delete(e.Models, role)
		return
	}
	if e.Models == nil {
		e.Models = map[string]teamConfigRoleInput{}
	}
	e.Models[role] = teamConfigRoleInput{
		Provider: cfg.Provider,
		Model:    cfg.Model,
	}
}

func (e *teamConfigEditor) YAMLConfig() teamYAMLConfig {
	cfg := teamYAMLConfig{
		MaxWorkers: normalizeTeamMaxWorkers(e.MaxWorkers),
	}
	cfg.SessionReuse = normalizeSessionReuseConfig(e.SessionReuse)
	if len(e.Models) > 0 {
		models := make(map[string]pool.ModelConfig, len(e.Models))
		for role, input := range e.Models {
			modelCfg, ok := normalizeTeamModelInput(input)
			if !ok {
				continue
			}
			models[normalizeTeamRoleKey(role)] = modelCfg
		}
		if len(models) > 0 {
			cfg.Models = models
		}
	}
	return cfg
}

func (e *teamConfigEditor) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(e.YAMLConfig())
	if err != nil {
		return fmt.Errorf("marshal team config: %w", err)
	}
	if err := os.WriteFile(path, append([]byte(teamConfigFileHeader), data...), 0644); err != nil {
		return fmt.Errorf("write team config: %w", err)
	}
	return nil
}

func teamInitRoleSpecs() []teamConfigRoleSpec {
	return []teamConfigRoleSpec{
		{Key: "default", Label: "Default", Summary: "Fallback route used when a role has no explicit override."},
		{Key: "planner", Label: "Planner", Summary: "Breaks work into steps and coordinates the team."},
		{Key: "implementer", Label: "Implementer", Summary: "Executes tasks and makes the requested code changes."},
		{Key: "reviewer", Label: "Reviewer", Summary: "Checks for bugs, regressions, and missing coverage."},
	}
}

func teamRoleSpec(role string) teamConfigRoleSpec {
	switch normalizeTeamRoleKey(role) {
	case "planner":
		return teamConfigRoleSpec{Key: "planner", Label: "Planner", Summary: "Breaks work into steps and coordinates the team."}
	case "implementer":
		return teamConfigRoleSpec{Key: "implementer", Label: "Implementer", Summary: "Executes tasks and makes the requested code changes."}
	case "reviewer":
		return teamConfigRoleSpec{Key: "reviewer", Label: "Reviewer", Summary: "Checks for bugs, regressions, and missing coverage."}
	case "default":
		return teamConfigRoleSpec{Key: "default", Label: "Default", Summary: "Fallback route used when a role has no explicit model override."}
	default:
		role = normalizeTeamRoleKey(role)
		return teamConfigRoleSpec{Key: role, Label: titleCase(role), Summary: "Custom role route."}
	}
}

func teamRolePromptDefaults(provider string) teamConfigRoleInput {
	provider = teamPromptProvider(provider)
	return teamConfigRoleInput{
		Provider: provider,
		Model:    defaultTeamModel(provider),
	}
}

func teamProviderChoices() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("Claude", "claude"),
		huh.NewOption("Codex", "codex"),
		huh.NewOption("Gemini", "gemini"),
	}
}

func normalizeSessionReuseConfig(cfg pool.SessionReuseConfig) pool.SessionReuseConfig {
	defaults := pool.DefaultSessionReuseConfig()
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = defaults.TTLSeconds
	}
	if cfg.MaxTasks <= 0 {
		cfg.MaxTasks = defaults.MaxTasks
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaults.MaxTokens
	}
	return cfg
}

func teamProcessSummary(cfg pool.SessionReuseConfig) string {
	cfg = normalizeSessionReuseConfig(cfg)
	if !cfg.Enabled {
		return "fresh session each task"
	}
	scope := "same role"
	if !cfg.SameRoleOnly {
		scope = "cross role"
	}
	return fmt.Sprintf("reuse %d tasks / %ds / %s", cfg.MaxTasks, cfg.TTLSeconds, scope)
}

func teamModelModes(provider string) []teamModelMode {
	recommended := defaultTeamModel(provider)
	return []teamModelMode{
		{Key: "provider-default", Label: "Provider default"},
		{Key: "recommended", Label: "Recommended preset (" + recommended + ")"},
		{Key: "custom", Label: "Custom model"},
	}
}

func teamRoleSummary(editor *teamConfigEditor, role, baseProvider string) string {
	current := editor.Model(role)
	if role == "default" {
		if current.Provider == "" && current.Model == "" {
			defaults := teamRolePromptDefaults(baseProvider)
			return fmt.Sprintf("%s / %s", defaults.Provider, defaults.Model)
		}
		return teamRoleValueSummary(current)
	}
	if current.Provider == "" && current.Model == "" {
		return "inherits default"
	}
	return teamRoleValueSummary(current)
}

func teamRoleValueSummary(input teamConfigRoleInput) string {
	provider := normalizeTeamProvider(input.Provider)
	model := strings.TrimSpace(input.Model)
	switch {
	case provider != "" && model != "":
		return fmt.Sprintf("%s / %s", provider, model)
	case provider != "":
		return fmt.Sprintf("%s / provider default", provider)
	default:
		return "not configured"
	}
}

func promptTeamMaxWorkers(editor *teamConfigEditor) error {
	maxWorkersStr := strconv.Itoa(editor.MaxWorkers)
	if err := huh.NewInput().
		Title("Max concurrent workers").
		Placeholder(strconv.Itoa(teamConfigDefaultMaxWorkers)).
		Value(&maxWorkersStr).
		Run(); err != nil {
		return err
	}
	editor.SetMaxWorkers(maxWorkersStr)
	return nil
}

func promptTeamProcess(editor *teamConfigEditor) error {
	cfg := normalizeSessionReuseConfig(editor.SessionReuse)
	mode := "fresh"
	switch {
	case !cfg.Enabled:
		mode = "fresh"
	case cfg.MaxTasks == 3 && cfg.TTLSeconds == 300 && cfg.MaxTokens == 100000 && cfg.SameRoleOnly:
		mode = "balanced"
	case cfg.MaxTasks == 8 && cfg.TTLSeconds == 900 && cfg.MaxTokens == 200000 && cfg.SameRoleOnly:
		mode = "aggressive"
	default:
		mode = "custom"
	}

	if err := huh.NewSelect[string]().
		Title("Worker session reuse").
		Description("Controls whether workers keep their AI session between tasks to trade freshness for speed and cost.").
		Options(
			huh.NewOption("Fresh each task", "fresh"),
			huh.NewOption("Balanced reuse", "balanced"),
			huh.NewOption("Aggressive reuse", "aggressive"),
			huh.NewOption("Custom", "custom"),
		).
		Value(&mode).
		Run(); err != nil {
		return err
	}

	switch mode {
	case "fresh":
		editor.SessionReuse = pool.DefaultSessionReuseConfig()
		editor.SessionReuse.Enabled = false
		return nil
	case "balanced":
		editor.SessionReuse = pool.SessionReuseConfig{
			Enabled:      true,
			TTLSeconds:   300,
			MaxTasks:     3,
			MaxTokens:    100000,
			SameRoleOnly: true,
		}
		return nil
	case "aggressive":
		editor.SessionReuse = pool.SessionReuseConfig{
			Enabled:      true,
			TTLSeconds:   900,
			MaxTasks:     8,
			MaxTokens:    200000,
			SameRoleOnly: true,
		}
		return nil
	}

	cfg.Enabled = true
	ttl := strconv.Itoa(cfg.TTLSeconds)
	maxTasks := strconv.Itoa(cfg.MaxTasks)
	maxTokens := strconv.Itoa(cfg.MaxTokens)
	sameRoleOnly := cfg.SameRoleOnly

	if err := huh.NewInput().
		Title("Reuse TTL in seconds").
		Placeholder("300").
		Value(&ttl).
		Run(); err != nil {
		return err
	}
	if err := huh.NewInput().
		Title("Max tasks before session reset").
		Placeholder("3").
		Value(&maxTasks).
		Run(); err != nil {
		return err
	}
	if err := huh.NewInput().
		Title("Max cumulative tokens before session reset").
		Placeholder("100000").
		Value(&maxTokens).
		Run(); err != nil {
		return err
	}
	if err := huh.NewConfirm().
		Title("Keep reuse within the same worker role only?").
		Value(&sameRoleOnly).
		Run(); err != nil {
		return err
	}

	cfg.TTLSeconds, _ = strconv.Atoi(strings.TrimSpace(ttl))
	cfg.MaxTasks, _ = strconv.Atoi(strings.TrimSpace(maxTasks))
	cfg.MaxTokens, _ = strconv.Atoi(strings.TrimSpace(maxTokens))
	cfg.SameRoleOnly = sameRoleOnly
	editor.SessionReuse = normalizeSessionReuseConfig(cfg)
	editor.SessionReuse.Enabled = true
	return nil
}

func promptTeamRoleEditor(editor *teamConfigEditor, role, baseProvider string) error {
	spec := teamRoleSpec(role)
	if role != "default" {
		mode := "override"
		if current := editor.Model(role); current.Provider == "" && current.Model == "" {
			mode = "inherit"
		}
		if err := huh.NewSelect[string]().
			Title(spec.Label+" route").
			Description(spec.Summary).
			Options(
				huh.NewOption("Inherit default", "inherit"),
				huh.NewOption("Custom override", "override"),
			).
			Value(&mode).
			Run(); err != nil {
			return err
		}
		if mode == "inherit" {
			editor.SetModel(role, teamConfigRoleInput{})
			return nil
		}
	}

	current := editor.Model(role)
	provider := current.Provider
	if provider == "" {
		if role == "default" {
			provider = teamPromptProvider(baseProvider)
		} else {
			provider = teamPromptProvider(editor.Model("default").Provider)
		}
	}
	if provider == "" {
		provider = teamPromptProvider(baseProvider)
	}

	if err := huh.NewSelect[string]().
		Title(spec.Label + " provider").
		Description(spec.Summary).
		Options(teamProviderChoices()...).
		Value(&provider).
		Run(); err != nil {
		return err
	}

	modelMode := "recommended"
	model := strings.TrimSpace(current.Model)
	switch {
	case model == "":
		modelMode = "provider-default"
	case model == defaultTeamModel(provider):
		modelMode = "recommended"
	default:
		modelMode = "custom"
	}

	modeOptions := make([]huh.Option[string], 0, len(teamModelModes(provider)))
	for _, mode := range teamModelModes(provider) {
		modeOptions = append(modeOptions, huh.NewOption(mode.Label, mode.Key))
	}
	if err := huh.NewSelect[string]().
		Title(spec.Label + " model").
		Options(modeOptions...).
		Value(&modelMode).
		Run(); err != nil {
		return err
	}

	switch modelMode {
	case "provider-default":
		model = ""
	case "recommended":
		model = defaultTeamModel(provider)
	default:
		if model == "" {
			model = defaultTeamModel(provider)
		}
		if err := huh.NewInput().
			Title(spec.Label + " custom model").
			Placeholder(defaultTeamModel(provider)).
			Value(&model).
			Run(); err != nil {
			return err
		}
	}

	editor.SetModel(role, teamConfigRoleInput{
		Provider: provider,
		Model:    model,
	})
	return nil
}

func promptTeamConfigLoop(editor *teamConfigEditor, baseProvider string) error {
	for {
		selection := ""
		options := []huh.Option[string]{
			huh.NewOption(fmt.Sprintf("Max workers (%d)", editor.MaxWorkers), "max-workers"),
			huh.NewOption(fmt.Sprintf("Process (%s)", teamProcessSummary(editor.SessionReuse)), "process"),
		}
		for _, role := range teamInitRoleSpecs() {
			options = append(options, huh.NewOption(
				fmt.Sprintf("%s (%s)", role.Label, teamRoleSummary(editor, role.Key, baseProvider)),
				role.Key,
			))
		}
		options = append(options, huh.NewOption("Save and launch", "save"))

		if err := huh.NewSelect[string]().
			Title("Team configuration").
			Options(options...).
			Value(&selection).
			Run(); err != nil {
			return err
		}

		switch selection {
		case "max-workers":
			if err := promptTeamMaxWorkers(editor); err != nil {
				return err
			}
		case "process":
			if err := promptTeamProcess(editor); err != nil {
				return err
			}
		case "save":
			return nil
		default:
			if err := promptTeamRoleEditor(editor, selection, baseProvider); err != nil {
				return err
			}
		}
	}
}

func normalizeTeamRoleKey(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func normalizeTeamMaxWorkers(n int) int {
	if n < 1 {
		return teamConfigDefaultMaxWorkers
	}
	return n
}

func normalizeTeamProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	return canonicalProviderName(provider)
}

func teamPromptProvider(provider string) string {
	if normalized := normalizeTeamProvider(provider); normalized != "" {
		return normalized
	}
	return DefaultProvider().Name
}

func teamConfigProviderFromModel(cfg pool.ModelConfig) string {
	if provider := normalizeTeamProvider(cfg.Provider); provider != "" {
		return provider
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Adapter)) {
	case "claude", "claude-code":
		return "claude"
	case "codex", "openai", "openai-codex":
		return "codex"
	case "gemini", "google", "gemini-cli":
		return "gemini"
	default:
		return ""
	}
}

func normalizeTeamModelInput(input teamConfigRoleInput) (pool.ModelConfig, bool) {
	cfg := pool.ModelConfig{
		Provider: normalizeTeamProvider(input.Provider),
		Model:    strings.TrimSpace(input.Model),
	}
	if cfg.Provider == "" && cfg.Model == "" {
		return pool.ModelConfig{}, false
	}
	return cfg, true
}

// handleTeamInit runs the team configuration wizard.
func handleTeamInit(args []string) error {
	workspace := detectWorkspace()
	teamName, args := extractTeamName(args)
	if len(args) > 0 {
		return fmt.Errorf("unknown flag %q for \"mittens team init\" (supported: --name)", args[0])
	}
	if strings.TrimSpace(teamName) == "" {
		if err := huh.NewInput().
			Title("Team name").
			Placeholder("strike-team-a").
			Value(&teamName).
			Run(); err != nil {
			return gracefulAbort(err)
		}
	}
	if err := validateTeamName(teamName); err != nil {
		return err
	}

	configPath := teamConfigPath(teamName)
	editor, err := loadTeamConfigEditor(configPath)
	if err != nil {
		return err
	}
	baseProvider := defaultTeamInitProvider()

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardTitle.Render("Team Mode Configuration"))
	fmt.Fprintln(os.Stderr, wizardDim.Render("  Project: "+workspace))
	fmt.Fprintln(os.Stderr, wizardDim.Render("  Team: "+teamName))
	fmt.Fprintln(os.Stderr)

	if editor.Model("default") == (teamConfigRoleInput{}) {
		editor.SetModel("default", teamRolePromptDefaults(baseProvider))
	}

	if err := promptTeamConfigLoop(editor, baseProvider); err != nil {
		return gracefulAbort(err)
	}

	if err := editor.Save(configPath); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("Saved team "+teamName+" to "+configPath))
	fmt.Fprintln(os.Stderr, wizardDim.Render("Launching team "+teamName+"..."))
	return runTeamSession([]string{"--name", teamName})
}

// teamYAMLConfig is the serialization format for team.yaml.
type teamYAMLConfig struct {
	MaxWorkers   int                         `yaml:"max_workers,omitempty"`
	Models       map[string]pool.ModelConfig `yaml:"models,omitempty"`
	SessionReuse pool.SessionReuseConfig     `yaml:"session_reuse,omitempty"`
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
		return "gpt-5.4"
	case "gemini":
		return "gemini-2.5-pro"
	default:
		return "claude-sonnet-4-6"
	}
}

func handleTeamList(args []string) error {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			return fmt.Errorf("unknown flag %q for \"mittens team list\" (supported: --json)", arg)
		}
	}

	teams, err := listTeamConfigs(ConfigHome())
	if err != nil {
		return err
	}

	if jsonOutput {
		if teams == nil {
			teams = []teamConfigMeta{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(teams)
	}

	if len(teams) == 0 {
		fmt.Println("No named teams configured. Run 'mittens team init --name <team>' to create one.")
		return nil
	}

	fmt.Printf("%-20s %-8s %s\n", "TEAM", "SESSIONS", "CONFIG")
	for _, team := range teams {
		fmt.Printf("%-20s %-8d %s\n", team.Name, team.SessionCount, team.ConfigPath)
	}
	return nil
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
		fmt.Println("No team sessions found. Run 'mittens team init --name <team>' to create one.")
		return nil
	}

	const timeFmt = "2006-01-02 15:04"
	fmt.Printf("%-28s %-18s %-20s %-10s %-18s %-18s %s\n", "PROJECT", "TEAM", "SESSION", "STATUS", "STARTED", "LAST ACTIVITY", "WORKERS")
	for _, s := range display {
		status := "stopped"
		if s.Running {
			status = "running"
		}
		teamName := s.TeamName
		if teamName == "" {
			teamName = "-"
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
		fmt.Printf("%-28s %-18s %-20s %-10s %-18s %-18s %s\n", s.ProjectDir, teamName, s.SessionID, status, started, activity, workers)
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
  (none)     Launch an existing named team session
  init       Create or edit a named team, then launch it
  list       Show configured named teams
  status     Show team sessions (--all-projects, --limit N, --json)
  resume     Reconnect to a crashed team session
  clean      Remove old session state directories

Team flags:
  --name NAME    Team config name to launch or edit

Other flags are forwarded to the leader container (same as normal mittens flags).
Everything after -- is passed through to the AI provider unchanged.

Configuration:
  Team configs are stored globally at ~/.mittens/teams/<team>/team.yaml
  Sessions are per-project launches of a named team config with a generated session ID.
  Run 'mittens team init --name <team>' to create or edit a team before launching it.

Examples:
  mittens team --name strike-team-a     Launch an existing named team
  mittens team init                     Create a new named team interactively, then launch it
  mittens team init --name strike-team-a Create or edit strike-team-a, then launch it
  mittens team list                     List named team configs
  mittens team list --json              Output named team configs as JSON
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
