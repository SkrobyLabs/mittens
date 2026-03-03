package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/Skroby/mittens/extensions/registry"
)

// App holds all state for a single mittens invocation.
type App struct {
	// Provider configuration (AI assistant identity, paths, keys).
	Provider *Provider

	// Core flags
	Verbose      bool
	NoConfig     bool
	NoHistory    bool
	NoBuild      bool
	DinD         bool
	Yolo         bool
	NoNotify     bool
	NetworkHost  bool
	Worktree     bool
	Shell        bool
	NoResume     bool
	ExtraDirs    []string
	InstanceName string // user-provided name via --name
	ClaudeArgs   []string

	// Computed state
	Workspace          string // git root or cwd
	EffectiveWorkspace string // worktree path if --worktree, else same as Workspace
	WorkspaceMountSrc  string // what actually gets mounted at /workspace
	Extensions         []*registry.Extension
	Credentials        *CredentialManager
	ContainerName      string
	ImageName          string
	ImageTag           string
	HostProjectDir     string // cx_project_dir(Workspace)

	// Image build state
	imageTagParts []string
	buildArgs     map[string]string

	// Cleanup tracking
	tempDirs        []string
	worktreeDirs    []string
	worktreeOrigins map[string]string // worktree path -> original HEAD sha
	worktreeRepos   map[string]string // worktree path -> original repo root

	// Clipboard sync
	clipboardDir string
	clipboardPID int

	// Credential broker
	broker     *CredentialBroker
	brokerPort int
}

// coreFlags maps flag names to a setter function on *App.
var coreFlags = map[string]func(*App){
	"--verbose":      func(a *App) { a.Verbose = true },
	"-v":             func(a *App) { a.Verbose = true },
	"--no-config":    func(a *App) { a.NoConfig = true },
	"--no-history":   func(a *App) { a.NoHistory = true },
	"--no-build":     func(a *App) { a.NoBuild = true },
	"--dind":         func(a *App) { a.DinD = true },
	"--yolo":         func(a *App) { a.Yolo = true },
	"--no-notify":    func(a *App) { a.NoNotify = true },
	"--network-host": func(a *App) { a.NetworkHost = true },
	"--worktree":     func(a *App) { a.Worktree = true },
	"--shell":        func(a *App) { a.Shell = true },
	"--no-resume":    func(a *App) { a.NoResume = true },
	"--init":         func(a *App) {}, // legacy bash flag, ignored (use `mittens init`)
}

// coreFlagsWithArg maps flag names that consume the next argument.
var coreFlagsWithArg = map[string]func(*App, string){
	"--dir":          func(a *App, val string) { a.ExtraDirs = append(a.ExtraDirs, val) },
	"--name":         func(a *App, val string) { a.InstanceName = val },
	"--provider":     func(a *App, val string) {}, // already applied in main.go pre-scan
}

// ParseFlags parses all flags (core + extension) from the given args.
// Everything after "--" is collected into ClaudeArgs.
func (a *App) ParseFlags(args []string) error {
	i := 0
	for i < len(args) {
		arg := args[i]

		// "--" separator: everything after goes to Claude.
		if arg == "--" {
			a.ClaudeArgs = append(a.ClaudeArgs, args[i+1:]...)
			break
		}

		// Core boolean flags.
		if setter, ok := coreFlags[arg]; ok {
			setter(a)
			i++
			continue
		}

		// Core flags with an argument.
		if setter, ok := coreFlagsWithArg[arg]; ok {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return fmt.Errorf("%s requires an argument", arg)
			}
			setter(a, args[i+1])
			i += 2
			continue
		}

		// Try each extension.
		claimed := false
		for _, ext := range a.Extensions {
			consumed, ok := ext.ParseFlag(args[i:])
			if ok {
				claimed = true
				i += consumed
				break
			}
		}
		if claimed {
			continue
		}

		// Help flags -- handle here since cobra doesn't parse for us.
		if arg == "--help" || arg == "-h" {
			printHelp(a.Extensions)
			os.Exit(0)
		}
		if arg == "--extensions" {
			printExtensions(a.Extensions)
			os.Exit(0)
		}
		if arg == "--json-caps" {
			printJSONCaps(a.Extensions)
			os.Exit(0)
		}

		// Unrecognised flag or positional arg -- forward to Claude.
		// Claude Code accepts flags like --resume, --model, --print, etc.
		a.ClaudeArgs = append(a.ClaudeArgs, arg)
		i++
	}
	return nil
}

// Run is the main orchestration method.
func (a *App) Run() error {
	defer a.Cleanup()

	// Precondition checks.
	if os.Getenv("HOME") == "" {
		return fmt.Errorf("HOME environment variable is not set")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is not installed or not in PATH")
	}

	home := os.Getenv("HOME")
	ensureDir(a.Provider.HostConfigDir(home))

	// Detect workspace.
	a.Workspace = detectWorkspace()
	a.EffectiveWorkspace = a.Workspace
	cwd, _ := os.Getwd()
	a.WorkspaceMountSrc = cwd

	// Session persistence setup.
	if !a.NoHistory {
		a.HostProjectDir = ProjectDir(a.Workspace)
		ensureDir(filepath.Join(a.Provider.HostConfigDir(home), "projects", a.HostProjectDir))
	}

	// Worktree setup for primary workspace.
	if a.Worktree {
		gitRoot := a.Workspace
		if gitRoot == cwd {
			return fmt.Errorf("--worktree requires a git repository")
		}
		dirty, _ := captureCommand("git", "-C", gitRoot, "status", "--porcelain")
		if dirty != "" {
			logWarn("Working tree is dirty -- worktree will start clean from HEAD")
		}
		suffix := fmt.Sprintf("wt-%d", os.Getpid())
		wtPath, err := a.createWorktree(gitRoot, suffix)
		if err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}
		a.EffectiveWorkspace = wtPath
		rel := strings.TrimPrefix(cwd, gitRoot)
		rel = strings.TrimPrefix(rel, "/")
		if rel != "" {
			a.WorkspaceMountSrc = filepath.Join(wtPath, rel)
		} else {
			a.WorkspaceMountSrc = wtPath
		}
		logInfo("Worktree: %s", wtPath)
	}

	// Run setup resolvers for enabled extensions.
	var resolverDockerArgs []string
	var resolverFirewallExtra []string
	for _, ext := range a.Extensions {
		if !ext.Enabled {
			continue
		}
		setupFn := registry.GetSetupResolver(ext.Name)
		if setupFn == nil {
			continue
		}
		// Create a staging directory for this extension.
		staging, err := os.MkdirTemp("", "mittens-"+ext.Name+"-*")
		if err != nil {
			return fmt.Errorf("creating staging dir for %s: %w", ext.Name, err)
		}
		a.tempDirs = append(a.tempDirs, staging)

		ctx := &registry.SetupContext{
			Home:          home,
			Extension:     ext,
			DockerArgs:    &resolverDockerArgs,
			FirewallExtra: &resolverFirewallExtra,
			TempDirs:      &a.tempDirs,
			StagingDir:    staging,
		}
		if err := setupFn(ctx); err != nil {
			return fmt.Errorf("extension %s setup: %w", ext.Name, err)
		}
	}

	// Include non-default provider in image tag to avoid cache collisions.
	if a.Provider.Name != "claude" {
		a.imageTagParts = append(a.imageTagParts, a.Provider.Name)
	}

	// Collect extension build state.
	a.buildArgs = make(map[string]string)
	var installExtensions []string
	for _, ext := range a.Extensions {
		if !ext.Enabled {
			continue
		}
		if ext.Build != nil && ext.Build.Script != "" {
			installExtensions = append(installExtensions, ext.Name)
		}
		for k, v := range ext.BuildArgs() {
			a.buildArgs[k] = v
		}
		tag := ext.ImageTagPart()
		if tag != "" {
			a.imageTagParts = append(a.imageTagParts, tag)
		}
	}
	if len(installExtensions) > 0 {
		a.buildArgs["INSTALL_EXTENSIONS"] = strings.Join(installExtensions, ",")
	}
	if len(a.imageTagParts) > 0 {
		sort.Strings(a.imageTagParts)
		a.ImageTag = strings.Join(a.imageTagParts, "-")
	}

	// Setup credentials.
	a.Credentials = NewCredentialManager(a.Provider)
	if err := a.Credentials.Setup(); err != nil {
		logWarn("Credential setup: %v", err)
	}

	// Start broker (credential sync + host URL opening via TCP).
	{
		var seed string
		if a.Credentials.TmpFile() != "" {
			data, _ := os.ReadFile(a.Credentials.TmpFile())
			seed = string(data)
		}
		a.broker = NewCredentialBroker("", seed, a.Credentials.Stores())
		a.broker.OnOpen = openOnHost
		if !a.NoNotify {
			displayName := a.Provider.DisplayName
			a.broker.OnNotify = func(container, event, message string) {
				notifyOnHost(container, event, message, displayName)
			}
		}

		// Persistent broker log for debugging.
		logDir := filepath.Join(home, ".mittens", "logs")
		ensureDir(logDir)
		rotateBrokerLog(filepath.Join(logDir, "broker.log"))
		if lf, err := os.OpenFile(filepath.Join(logDir, "broker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			a.broker.LogFile = lf
		}

		port, err := a.broker.ListenTCP()
		if err == nil {
			a.brokerPort = port
			go func() {
				if err := a.broker.Serve(); err != nil && err != http.ErrServerClosed {
					logWarn("Broker: %v", err)
				}
			}()
			logInfo("Broker: started on port %d", port)
		} else {
			logWarn("Broker: failed to start: %v", err)
			a.broker = nil
		}
	}

	// Build Docker image.
	if !a.NoBuild {
		if err := a.buildImage(); err != nil {
			return err
		}
	}

	// Container name.
	if a.InstanceName != "" {
		if !isValidContainerName(a.InstanceName) {
			return fmt.Errorf("invalid --name %q: must match [a-zA-Z0-9][a-zA-Z0-9_.-]*", a.InstanceName)
		}
		a.ContainerName = "mittens-" + a.InstanceName

		// Remove stale (stopped) container with the same name, or error if running.
		if exists, running := InspectContainerRunning(a.ContainerName); exists {
			if running {
				return fmt.Errorf("container %q is already running; stop it first or use a different --name", a.ContainerName)
			}
			logInfo("Removed stale container: %s", a.ContainerName)
			_ = RemoveContainer(a.ContainerName)
		}
	} else {
		a.ContainerName = fmt.Sprintf("mittens-%d", os.Getpid())
	}

	// Auto-continue last session unless opted out or user already specified resume/continue.
	if !a.NoResume && !a.Shell && a.HostProjectDir != "" {
		hasResume := false
		for _, arg := range a.ClaudeArgs {
			if a.Provider.IsResumeFlag(arg) {
				hasResume = true
				break
			}
		}
		if !hasResume {
			projDir := filepath.Join(a.Provider.HostConfigDir(home), "projects", a.HostProjectDir)
			if matches, _ := filepath.Glob(filepath.Join(projDir, "*.jsonl")); len(matches) > 0 {
				a.ClaudeArgs = append([]string{"--continue"}, a.ClaudeArgs...)
			}
		}
	}

	// Yolo mode: prepend skip-permissions flag.
	if a.Yolo {
		a.ClaudeArgs = append([]string{a.Provider.SkipPermsFlag}, a.ClaudeArgs...)
		logWarn("YOLO mode: all permission prompts will be skipped")
	}

	// Assemble docker run args and run.
	dockerArgs := a.assembleDockerArgs(resolverDockerArgs, resolverFirewallExtra)

	// Summary logging.
	logInfo("Working directory: %s", cwd)
	if !a.NoHistory && a.HostProjectDir != "" {
		logInfo("Session persistence: enabled (project dir: %s)", a.HostProjectDir)
	} else if a.NoHistory {
		logInfo("Session persistence: disabled (--no-history)")
	}
	if len(a.ClaudeArgs) > 0 {
		logInfo("Claude args: %s", strings.Join(a.ClaudeArgs, " "))
	}

	if a.Verbose {
		logInfo("Command: docker run %s", strings.Join(dockerArgs, " "))
	}

	return a.runContainer(dockerArgs)
}

// Cleanup extracts credentials, removes the container, cleans temp state.
func (a *App) Cleanup() {
	// Stop clipboard sync.
	if a.clipboardPID > 0 {
		if p, err := os.FindProcess(a.clipboardPID); err == nil {
			_ = p.Signal(syscall.SIGTERM)
			_, _ = p.Wait()
		}
	}
	if a.clipboardDir != "" {
		os.RemoveAll(a.clipboardDir)
	}

	// Extract refreshed credentials from the stopped container.
	if a.ContainerName != "" {
		if a.Credentials != nil {
			if a.broker != nil {
				// Broker has the freshest credentials from all containers.
				finalCreds := a.broker.Credentials()
				_ = a.broker.Close()
				if finalCreds != "" {
					a.Credentials.PersistAll(finalCreds)
				}
			} else {
				// Fallback: docker cp from the single container.
				_ = a.Credentials.PersistFromContainer(a.ContainerName, a.Provider.ContainerCredentialPath())
			}
		}
		RemoveContainer(a.ContainerName)

		// Remove per-container DinD volume.
		if a.DinD {
			_ = exec.Command("docker", "volume", "rm", a.ContainerName+"-docker").Run()
		}
	}

	// Clean up credential temp file.
	if a.Credentials != nil {
		a.Credentials.Cleanup()
	}

	// Clean up worktrees: remove if clean, keep if dirty/new commits.
	for _, wt := range a.worktreeDirs {
		info, err := os.Stat(wt)
		if err != nil || !info.IsDir() {
			continue
		}
		dirty, _ := captureCommand("git", "-C", wt, "status", "--porcelain")
		cur, _ := captureCommand("git", "-C", wt, "rev-parse", "HEAD")
		orig := a.worktreeOrigins[wt]
		if dirty == "" && cur == orig {
			if err := exec.Command("git", "worktree", "remove", wt).Run(); err == nil {
				logInfo("Removed clean worktree: %s", wt)
			}
		} else {
			logInfo("Keeping worktree with changes: %s", wt)
		}
	}

	// Clean up temp dirs.
	for _, d := range a.tempDirs {
		os.RemoveAll(d)
	}
}

// buildImage runs docker build.
func (a *App) buildImage() error {
	logInfo("Building Docker image...")

	projectRoot := scriptDir()
	dockerfile := filepath.Join(projectRoot, "container", "Dockerfile")
	if _, err := os.Stat(dockerfile); err != nil {
		return fmt.Errorf("Dockerfile not found at %s", dockerfile)
	}

	uid, gid := CurrentUserIDs()

	var enabledExts []*registry.Extension
	for _, ext := range a.Extensions {
		if ext.Enabled {
			enabledExts = append(enabledExts, ext)
		}
	}

	return BuildImage(BuildContext{
		ContextDir: projectRoot,
		Dockerfile: dockerfile,
		ImageName:  a.ImageName,
		ImageTag:   a.ImageTag,
		UserID:     uid,
		GroupID:    gid,
		Extensions: enabledExts,
		ExtraBuildArgs: map[string]string{
			"AI_USERNAME":    a.Provider.Username,
			"AI_BINARY":     a.Provider.Binary,
			"AI_INSTALL_CMD": a.Provider.InstallCmd,
			"AI_CONFIG_DIR":  a.Provider.ConfigDir,
		},
		Quiet: true,
	})
}

// assembleDockerArgs builds the full docker run argument list.
// resolverArgs and resolverFirewall come from extension setup resolvers.
func (a *App) assembleDockerArgs(resolverArgs []string, resolverFirewall []string) []string {
	home := os.Getenv("HOME")
	args := []string{
		"-it",
		"--name", a.ContainerName,
	}

	// Primary workspace mount.
	args = append(args, "-v", a.WorkspaceMountSrc+":/workspace")

	// AI config staging (read-only).
	args = append(args, "-v", a.Provider.HostConfigDir(home)+":"+a.Provider.StagingConfigDir()+":ro")

	// Environment variables.
	if a.Provider.APIKeyEnv != "" {
		args = append(args, "-e", a.Provider.APIKeyEnv+"="+os.Getenv(a.Provider.APIKeyEnv))
	}
	args = append(args, "-e", "TERM="+envOrDefault("TERM", "xterm-256color"))
	args = append(args, "-e", fmt.Sprintf("MITTENS_DIND=%v", a.DinD))
	if a.Yolo {
		args = append(args, "-e", "MITTENS_YOLO=true")
	}
	args = append(args, "-e", "MITTENS_CONTAINER_NAME="+a.ContainerName)
	if a.NoNotify {
		args = append(args, "-e", "MITTENS_NO_NOTIFY=true")
	}
	if a.InstanceName != "" {
		args = append(args, "-e", "MITTENS_INSTANCE_NAME="+a.InstanceName)
	}

	// Credential mount.
	if a.Credentials != nil && a.Credentials.TmpFile() != "" {
		args = append(args, "-v", a.Credentials.TmpFile()+":"+a.Provider.StagingCredentialPath()+":ro")
	}

	// Provider env vars — passed into the container for entrypoint.sh.
	args = append(args,
		"-e", "MITTENS_AI_USERNAME="+a.Provider.Username,
		"-e", "MITTENS_AI_BINARY="+a.Provider.Binary,
		"-e", "MITTENS_AI_CONFIG_DIR="+a.Provider.ConfigDir,
		"-e", "MITTENS_AI_CRED_FILE="+a.Provider.CredentialFile,
		"-e", "MITTENS_AI_PREFS_FILE="+a.Provider.UserPrefsFile,
		"-e", "MITTENS_AI_SETTINGS_FILE="+a.Provider.SettingsFile,
		"-e", "MITTENS_AI_PROJECT_FILE="+a.Provider.ProjectFile,
		"-e", "MITTENS_AI_TRUSTED_DIRS_KEY="+a.Provider.TrustedDirsKey,
		"-e", "MITTENS_AI_YOLO_KEY="+a.Provider.YoloKey,
		"-e", "MITTENS_AI_MCP_SERVERS_KEY="+a.Provider.MCPServersKey,
		"-e", "MITTENS_AI_SETTINGS_FORMAT="+a.Provider.SettingsFormat,
		"-e", "MITTENS_AI_CONFIG_SUBDIRS="+strings.Join(a.Provider.ConfigSubdirs, ","),
		"-e", "MITTENS_AI_PLUGIN_DIR="+a.Provider.PluginDir,
		"-e", "MITTENS_AI_PLUGIN_FILES="+strings.Join(a.Provider.PluginFiles, ","),
	)

	// Credential broker (TCP).
	if a.brokerPort > 0 {
		args = append(args, "-e", fmt.Sprintf("MITTENS_BROKER_PORT=%d", a.brokerPort))
		args = append(args, "--add-host=host.docker.internal:host-gateway")
	}

	// Session persistence mounts.
	if !a.NoHistory && a.HostProjectDir != "" {
		hostConfigDir := a.Provider.HostConfigDir(home)
		containerConfigDir := a.Provider.ContainerConfigDir()
		projDir := filepath.Join(hostConfigDir, "projects", a.HostProjectDir)
		containerProjDir := filepath.Join(containerConfigDir, "projects", a.HostProjectDir)
		args = append(args, "-v", projDir+":"+containerProjDir)

		if !a.Worktree {
			ensureDir(filepath.Join(hostConfigDir, "plans"))
			ensureDir(filepath.Join(hostConfigDir, "tasks"))
			args = append(args, "-v", filepath.Join(hostConfigDir, "plans")+":"+filepath.Join(containerConfigDir, "plans"))
			args = append(args, "-v", filepath.Join(hostConfigDir, "tasks")+":"+filepath.Join(containerConfigDir, "tasks"))
		}

		sessionWS := a.EffectiveWorkspace
		if sessionWS != "" && sessionWS != "/workspace" {
			args = append(args, "-e", "MITTENS_HOST_WORKSPACE="+sessionWS)
			args = append(args, "-v", a.WorkspaceMountSrc+":"+sessionWS)
		}
	}

	// Extra directory mounts.
	if len(a.ExtraDirs) > 0 {
		wtSuffix := fmt.Sprintf("wt-%d", os.Getpid())
		var extraPaths []string
		for _, dir := range a.ExtraDirs {
			resolved, err := filepath.Abs(dir)
			if err != nil {
				logWarn("--dir path not resolvable: %s", dir)
				continue
			}
			if _, err := os.Stat(resolved); err != nil {
				logError("--dir path does not exist: %s", dir)
				continue
			}

			var extraPath string
			if a.Worktree {
				gitRoot, err := captureCommand("git", "-C", resolved, "rev-parse", "--show-toplevel")
				if err == nil && gitRoot != "" {
					wtPath, err := a.createWorktree(gitRoot, wtSuffix)
					if err == nil {
						rel := strings.TrimPrefix(resolved, gitRoot)
						rel = strings.TrimPrefix(rel, "/")
						if rel != "" {
							extraPath = filepath.Join(wtPath, rel)
						} else {
							extraPath = wtPath
						}
						args = append(args, "-v", wtPath+":"+wtPath)
						logInfo("Extra directory worktree: %s", wtPath)
					} else {
						logWarn("Failed to create worktree for %s, mounting shared", gitRoot)
						extraPath = resolved
						args = append(args, "-v", resolved+":"+resolved)
					}
				} else {
					extraPath = resolved
					args = append(args, "-v", resolved+":"+resolved)
				}
			} else {
				extraPath = resolved
				args = append(args, "-v", resolved+":"+resolved)
			}
			extraPaths = append(extraPaths, extraPath)
			logInfo("Extra directory: %s", extraPath)
		}
		if len(extraPaths) > 0 {
			args = append(args, "-e", "MITTENS_EXTRA_DIRS="+strings.Join(extraPaths, ":"))
		}
	}

	// Worktree git metadata mounts.
	if len(a.worktreeRepos) > 0 {
		mounted := make(map[string]bool)
		for _, repo := range a.worktreeRepos {
			if !mounted[repo] {
				gitDir := filepath.Join(repo, ".git")
				args = append(args, "-v", gitDir+":"+gitDir)
				mounted[repo] = true
			}
		}
	}

	// User prefs file (account info, MCP servers) — skip if provider has no prefs file.
	if a.Provider.UserPrefsFile != "" {
		userPrefsPath := a.Provider.HostUserPrefsPath(home)
		if fileExists(userPrefsPath) {
			args = append(args, "-v", userPrefsPath+":"+a.Provider.StagingUserPrefsPath()+":ro")
		}
	}

	// .gitconfig
	gitconfig := filepath.Join(home, ".gitconfig")
	if fileExists(gitconfig) {
		args = append(args, "-v", gitconfig+":/mnt/claude-config/.gitconfig:ro")
	}

	// Extension mounts, env vars, capabilities (from YAML declarations).
	var firewallDomains []string
	for _, ext := range a.Extensions {
		if !ext.Enabled {
			continue
		}

		// Mounts from YAML.
		for _, m := range ext.ExpandedMounts(home) {
			mountStr := m.Src + ":" + m.Dst
			if m.Mode != "" {
				mountStr += ":" + m.Mode
			}
			args = append(args, "-v", mountStr)
			for k, v := range m.Env {
				args = append(args, "-e", k+"="+v)
			}
		}

		// Env vars from YAML.
		for k, v := range ext.Env {
			args = append(args, "-e", k+"="+v)
		}

		// Capabilities from YAML.
		for _, capability := range ext.Capabilities {
			args = append(args, "--cap-add", capability)
		}

		// Firewall domains from YAML.
		firewallDomains = append(firewallDomains, ext.FirewallDomains()...)
	}

	// Add provider, resolver-contributed docker args and firewall domains.
	firewallDomains = append(firewallDomains, a.Provider.FirewallDomains...)
	args = append(args, resolverArgs...)
	firewallDomains = append(firewallDomains, resolverFirewall...)

	if len(firewallDomains) > 0 {
		args = append(args, "-e", "MITTENS_FIREWALL_EXTRA="+strings.Join(firewallDomains, ","))
	}

	// Security hardening.
	if a.DinD {
		args = append(args, "--privileged")
		args = append(args, "-v", a.ContainerName+"-docker:/var/lib/docker")
		logWarn("Docker-in-Docker enabled (--privileged)")
	} else {
		args = append(args,
			"--cap-drop", "ALL",
			"--cap-add", "SETUID",
			"--cap-add", "SETGID",
			"--security-opt", "no-new-privileges",
		)
	}

	// Network mode.
	if a.NetworkHost {
		args = append(args, "--network", "host")
	}

	// Clipboard image sync (macOS only).
	if runtime.GOOS == "darwin" {
		dir, err := os.MkdirTemp("", "mittens-clipboard.*")
		if err == nil {
			a.clipboardDir = dir
			syncScript := filepath.Join(containerDir(), "clipboard-sync.sh")
			cmd := exec.Command("bash", syncScript, dir)
			if err := cmd.Start(); err == nil {
				a.clipboardPID = cmd.Process.Pid
				args = append(args, "-v", dir+":/tmp/mittens-clipboard:ro")
				logInfo("Clipboard image sync: enabled")
			}
		}
	}

	return args
}

// runContainer runs docker run with the assembled args.
func (a *App) runContainer(dockerArgs []string) error {
	code, err := RunContainer(dockerArgs, a.ImageName, a.ImageTag, a.Shell, a.Provider.Binary, a.ClaudeArgs)
	if err != nil {
		return fmt.Errorf("docker run failed: %w", err)
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// createWorktree creates a detached-HEAD git worktree as a sibling directory.
func (a *App) createWorktree(repoRoot, suffix string) (string, error) {
	headSHA, err := captureCommand("git", "-C", repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}

	wtPath := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"."+suffix)
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "--detach", wtPath, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w: %s", err, out)
	}

	a.worktreeDirs = append(a.worktreeDirs, wtPath)
	a.worktreeOrigins[wtPath] = headSHA
	a.worktreeRepos[wtPath] = repoRoot
	return wtPath, nil
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// notifyOnHost sends a native desktop notification.
func notifyOnHost(container, event, message, displayName string) {
	var title, body string
	switch event {
	case "stop":
		title = "mittens"
		body = container + ": " + displayName + " finished"
	case "notification":
		title = "mittens"
		if message != "" {
			body = container + ": " + message
		} else {
			body = container + ": needs attention"
		}
	default:
		title = "mittens"
		body = container + ": " + event
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		appPath := ensureNotifierApp()
		if appPath != "" {
			cmd = exec.Command("open", "-g", "-a", appPath, "--args", title, body)
		} else {
			cmd = exec.Command("osascript", "-e",
				fmt.Sprintf(`display notification %q with title %q sound name "Glass"`, body, title))
		}
	} else {
		cmd = exec.Command("notify-send", title, body)
	}
	_ = cmd.Start()
}

// ensureNotifierApp creates a minimal macOS .app bundle at ~/.mittens/Mittens.app
// so that notifications are attributed to "Mittens" instead of "Script Editor".
func ensureNotifierApp() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	appPath := filepath.Join(home, ".mittens", "Mittens.app")
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")

	// Already built — verify bundle ID is ours, not Script Editor's.
	if _, err := os.Stat(plistPath); err == nil {
		if data, err := os.ReadFile(plistPath); err == nil {
			if strings.Contains(string(data), "com.mittens.notifier") {
				return appPath
			}
			// Stale applet from before plist patching — rebuild.
			logInfo("Rebuilding Mittens.app (stale bundle ID)")
			_ = os.RemoveAll(appPath)
		}
	}

	// Build the applet with osacompile.
	script := `on run argv
	set theTitle to item 1 of argv
	set theBody to item 2 of argv
	display notification theBody with title theTitle sound name "Glass"
end run`
	cmd := exec.Command("osacompile", "-o", appPath, "-e", script)
	if err := cmd.Run(); err != nil {
		return ""
	}

	// Patch CFBundleIdentifier so macOS attributes notifications to "Mittens"
	// instead of grouping them under Script Editor.
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return ""
	}
	plist := strings.Replace(string(data),
		"com.apple.ScriptEditor.id.Mittens", "com.mittens.notifier", 1)
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return ""
	}

	// Ad-hoc codesign the modified bundle so macOS accepts it.
	_ = exec.Command("codesign", "--force", "--sign", "-", appPath).Run()

	return appPath
}

// openOnHost opens a URL in the host's default browser.
func openOnHost(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		logWarn("Failed to open URL on host: %v", err)
	}
}

var validContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func isValidContainerName(name string) bool {
	return validContainerName.MatchString(name)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func ensureDir(path string) {
	_ = os.MkdirAll(path, 0o755)
}

// ---------------------------------------------------------------------------
// Help text
// ---------------------------------------------------------------------------

func printHelp(exts []*registry.Extension) {
	fmt.Println(`mittens - Run Claude Code in an isolated Docker container

Usage: mittens [flags] [-- claude-args...]

Commands:
  init              Interactive project setup wizard
  logs [-f]         Show broker logs (-f to follow)
  clean [--dry-run] [--images]  Remove stopped mittens containers

Core flags:
  --verbose, -v     Show the docker command being run
  --no-config       Skip project config file loading
  --no-history      Disable session persistence (fully ephemeral)
  --no-resume       Start a new session (default: continue last)
  --no-build        Skip the Docker image build step
  --dind            Enable Docker-in-Docker (--privileged)
  --yolo            Skip all permission prompts
  --no-notify       Disable desktop notifications
  --network-host    Use host networking (default: bridge)
  --worktree        Git worktree isolation per invocation
  --shell           Start a bash shell instead of Claude
  --name NAME       Name this instance (default: PID-based)
  --dir PATH        Mount an additional directory (repeatable)
  --provider NAME   AI provider to use (claude, codex; default: claude)
  --extensions      List loaded extensions and their flags
  --help, -h        Show this help message`)

	if len(exts) > 0 {
		fmt.Println("\nExtension flags:")
		for _, ext := range exts {
			for _, f := range ext.Flags {
				desc := f.Description
				if desc == "" {
					desc = ext.Description
				}
				fmt.Printf("  %-18s %s\n", f.Name, desc)
			}
		}
	}
}

func printExtensions(exts []*registry.Extension) {
	if len(exts) == 0 {
		fmt.Println("No extensions loaded")
		return
	}
	fmt.Println("Loaded extensions:")
	fmt.Println()
	for _, ext := range exts {
		fmt.Printf("  %s: %s\n", ext.Name, ext.Description)
		for _, f := range ext.Flags {
			desc := f.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Printf("    %-16s %s\n", f.Name, desc)
		}
	}
}

// jsonCapsOutput is the structured output for --json-caps.
type jsonCapsOutput struct {
	Extensions []jsonCapsExtension `json:"extensions"`
	CoreFlags  []jsonCapsFlag      `json:"coreFlags"`
}

type jsonCapsExtension struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	DefaultOn   bool           `json:"defaultOn"`
	Flags       []jsonCapsFlag `json:"flags"`
}

type jsonCapsFlag struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ArgType     string   `json:"argType,omitempty"`
	EnumValues  []string `json:"enumValues,omitempty"`
	Multi       bool     `json:"multi,omitempty"`
}

func printJSONCaps(exts []*registry.Extension) {
	out := jsonCapsOutput{
		Extensions: make([]jsonCapsExtension, 0, len(exts)),
		CoreFlags: []jsonCapsFlag{
			{Name: "--dind", Description: "Enable Docker-in-Docker (--privileged)"},
			{Name: "--worktree", Description: "Git worktree isolation per invocation"},
			{Name: "--yolo", Description: "Skip all permission prompts"},
			{Name: "--no-notify", Description: "Disable desktop notifications"},
			{Name: "--network-host", Description: "Use host networking (default: bridge)"},
			{Name: "--no-history", Description: "Disable session persistence (fully ephemeral)"},
			{Name: "--no-build", Description: "Skip the Docker image build step"},
			{Name: "--no-resume", Description: "Start a new session (default: continue last)"},
			{Name: "--name", Description: "Name this instance (default: PID-based)", ArgType: "string"},
			{Name: "--shell", Description: "Start a bash shell instead of Claude"},
			{Name: "--provider", Description: "AI provider (claude, codex)", ArgType: "string"},
		},
	}

	for _, ext := range exts {
		je := jsonCapsExtension{
			Name:        ext.Name,
			Description: ext.Description,
			DefaultOn:   ext.DefaultOn,
			Flags:       make([]jsonCapsFlag, 0, len(ext.Flags)),
		}
		for _, f := range ext.Flags {
			jf := jsonCapsFlag{
				Name:        f.Name,
				Description: f.Description,
				ArgType:     f.Arg,
				EnumValues:  f.EnumValues,
				Multi:       f.Multi,
			}
			if jf.ArgType == "" {
				jf.ArgType = "none"
			}
			je.Flags = append(je.Flags, jf)
		}
		out.Extensions = append(out.Extensions, je)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
