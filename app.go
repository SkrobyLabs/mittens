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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	firewallext "github.com/Skroby/mittens/extensions/firewall"
	"github.com/Skroby/mittens/extensions/registry"
)

// App holds all state for a single mittens invocation.
type App struct {
	// Provider configuration (AI assistant identity, paths, keys).
	Provider *Provider

	// Core flags
	Verbose       bool
	NoConfig      bool
	NoHistory     bool
	NoBuild       bool
	Yolo          bool
	NoNotify      bool
	NetworkHost   bool
	Worktree      bool
	Shell         bool
	ResumeSession string // "" = fresh, "latest" = continue last, other = passthrough ID
	Role          string // "worker", "planner", or ""
	ExtraDirs     []string
	InstanceName  string // user-provided name via --name
	ClaudeArgs    []string

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
	clipboardDir    string
	clipboardReg    string

	// Drop zone for drag-and-drop file translation
	dropDir string

	// Terminal focus (for click-to-focus notifications)
	terminalFocus TerminalFocus

	// Host broker (credentials, URLs, notifications, OAuth)
	broker      *HostBroker
	brokerPort  int
	brokerToken string
	brokerSock  string // Unix socket path (Linux mode)
}

// coreFlags maps flag names to a setter function on *App.
var coreFlags = map[string]func(*App){
	"--verbose":      func(a *App) { a.Verbose = true },
	"-v":             func(a *App) { a.Verbose = true },
	"--no-config":    func(a *App) { a.NoConfig = true },
	"--no-history":   func(a *App) { a.NoHistory = true },
	"--no-build":     func(a *App) { a.NoBuild = true },
	"--yolo":         func(a *App) { a.Yolo = true },
	"--no-notify":    func(a *App) { a.NoNotify = true },
	"--network-host": func(a *App) { a.NetworkHost = true },
	"--worktree":     func(a *App) { a.Worktree = true },
	"--shell":        func(a *App) { a.Shell = true },
	"--worker":       func(a *App) { a.Role = "worker" },
	"--planner":      func(a *App) { a.Role = "planner" },
	// --resume is handled specially in ParseFlags (optional argument).
	"--init":         func(a *App) {}, // legacy bash flag, ignored (use `mittens init`)
	"--firewall-dev": func(a *App) { firewallext.DevMode = true },
}

// coreFlagsWithArg maps flag names that consume the next argument.
var coreFlagsWithArg = map[string]func(*App, string){
	"--dir":      func(a *App, val string) { a.ExtraDirs = append(a.ExtraDirs, val) },
	"--dir-ro":   func(a *App, val string) { a.ExtraDirs = append(a.ExtraDirs, "ro:"+val) },
	"--name":     func(a *App, val string) { a.InstanceName = val },
	"--provider": func(a *App, val string) {}, // already applied in main.go pre-scan
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

		// --resume [ID] — optional argument.
		if arg == "--resume" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				a.ResumeSession = args[i+1]
				i += 2
			} else {
				a.ResumeSession = "latest"
				i++
			}
			continue
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
		if a.Provider.HistoryMountsWholeConfig {
			ensureDir(a.Provider.HostConfigDir(home))
		} else {
			a.HostProjectDir = ProjectDir(a.Workspace)
			ensureDir(filepath.Join(a.Provider.HostConfigDir(home), "projects", a.HostProjectDir))
		}
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

	// Container name (must be set before resolvers so SetupContext carries it).
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
	logTag = a.Provider.Name + " " + a.ContainerName
	logVerbose(a.Verbose, "Container: %s", a.ContainerName)

	// Capture terminal identity for click-to-focus notifications.
	a.terminalFocus = DetectTerminalFocus()

	// Log enabled extensions.
	{
		var names []string
		for _, ext := range a.Extensions {
			if ext.Enabled {
				names = append(names, ext.Name)
			}
		}
		if len(names) > 0 {
			logVerbose(a.Verbose, "Enabled extensions: %s", strings.Join(names, ", "))
		}
	}

	// Run setup resolvers for enabled extensions.
	var resolverDockerArgs []string
	var resolverFirewallExtra []string
	for _, ext := range a.Extensions {
		if !ext.Enabled {
			continue
		}
		logVerbose(a.Verbose, "Setting up extension: %s", ext.Name)
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
			ContainerName: a.ContainerName,
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
	if a.Credentials.TmpFile() != "" {
		logVerbose(a.Verbose, "Credentials staged: %s", a.Credentials.TmpFile())
	} else {
		logVerbose(a.Verbose, "No credentials found")
	}

	// Start broker (credential sync + host URL opening via TCP).
	{
		var seed string
		if a.Credentials.TmpFile() != "" {
			data, _ := os.ReadFile(a.Credentials.TmpFile())
			seed = string(data)
		}
		a.broker = NewHostBroker("", seed, a.Credentials.Stores())
		a.broker.Name = a.Provider.Name
		if token, err := randomHex(16); err == nil {
			a.brokerToken = token
			a.broker.AuthToken = token
		} else {
			return fmt.Errorf("generate broker token: %w", err)
		}
		a.broker.OnOpen = openOnHost
		if !a.NoNotify {
			displayName := a.Provider.DisplayName
			focus := a.terminalFocus
			blogFn := a.broker.blog
			a.broker.OnNotify = func(container, event, message string) {
				notifyOnHost(container, event, message, displayName, focus, blogFn)
			}
		}

		// Persistent broker log for debugging.
		logDir := filepath.Join(home, ".mittens", "logs")
		ensureDir(logDir)
		rotateBrokerLog(filepath.Join(logDir, "broker.log"))
		if lf, err := os.OpenFile(filepath.Join(logDir, "broker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			a.broker.LogFile = lf
		}

		if runtime.GOOS == "linux" {
			// On Linux, use a Unix socket mounted into the container.
			// This avoids host firewall issues (UFW/iptables with default INPUT DROP
			// blocks traffic from the Docker bridge subnet to host ports).
			sockDir, err := os.MkdirTemp("", "mittens-broker.*")
			if err == nil {
				a.tempDirs = append(a.tempDirs, sockDir)
				sockPath := filepath.Join(sockDir, "broker.sock")
				a.broker.sockPath = sockPath
				go func() {
					if err := a.broker.Serve(); err != nil && err != http.ErrServerClosed {
						logWarn("Broker: %v", err)
					}
				}()
				a.brokerSock = sockPath
				logInfo("Broker: started on unix socket")
			} else {
				logWarn("Broker: failed to create socket dir: %v", err)
				a.broker = nil
			}
		} else {
			// On macOS/Windows, Docker Desktop provides transparent host→container
			// networking via host.docker.internal, so TCP works reliably.
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
	}

	logVerbose(a.Verbose, "Image tag: %s:%s", a.ImageName, a.ImageTag)

	// Resolve role preset before Docker build so interactive choice happens first.
	if err := a.maybeApplyRolePreset(); err != nil {
		if err == huh.ErrUserAborted {
			fmt.Fprintln(os.Stderr, "\nCancelled.")
			return nil
		}
		return err
	}

	// Build Docker image.
	if !a.NoBuild {
		if err := a.buildImage(); err != nil {
			return err
		}
	}

	// Resume session if --resume was given.
	a.maybeApplyResumeArgs()

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
		logInfo("Command: docker run %s", strings.Join(sanitizeDockerArgsForLog(dockerArgs), " "))
	}

	return a.runContainer(dockerArgs)
}

func (a *App) maybeApplyResumeArgs() {
	if a.ResumeSession == "" || a.Shell {
		return
	}
	for _, arg := range a.ClaudeArgs {
		if a.Provider.IsResumeFlag(arg) {
			return
		}
	}
	if a.ResumeSession == "latest" {
		a.ClaudeArgs = append(a.Provider.ContinueArgs, a.ClaudeArgs...)
		return
	}
	a.ClaudeArgs = append([]string{"--resume", a.ResumeSession}, a.ClaudeArgs...)
}

func (a *App) maybeApplyRolePreset() error {
	if a.Role == "" {
		if a.Shell {
			return nil
		}
		if argExists(a.ClaudeArgs, "--print") {
			return nil
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil
		}

		role, err := promptForRole()
		if err != nil {
			return err
		}
		if role == "custom" {
			return nil
		}
		a.Role = role
	}

	preset, ok := rolePreset(a.Workspace, a.Provider, a.Role)
	if !ok {
		return nil
	}

	extras := make([]string, 0, 4)

	if a.Provider.ModelFlag != "" && preset.Model != "" && !argExists(a.ClaudeArgs, a.Provider.ModelFlag) {
		extras = append(extras, a.Provider.ModelFlag, preset.Model)
	}
	if effortEnabled(a.Provider) && preset.Effort != "" && !effortArgExists(a.Provider, a.ClaudeArgs) {
		extras = append(extras, effortArgs(a.Provider, preset.Effort)...)
	}
	if len(extras) > 0 {
		a.ClaudeArgs = append(extras, a.ClaudeArgs...)
	}
	return nil
}

func promptForRole() (string, error) {
	var role string
	if err := createRolePrompt(&role); err != nil {
		return "", err
	}

	switch role {
	case "Worker":
		return "worker", nil
	case "Planner":
		return "planner", nil
	default:
		return "custom", nil
	}
}

func createRolePrompt(role *string) error {
	return huh.NewSelect[string]().
		Title("Planner, Worker, or Custom (no preset)").
		Options(
			huh.NewOption("Planner", "Planner"),
			huh.NewOption("Worker", "Worker"),
			huh.NewOption("Custom (no preset)", "Custom (no preset)"),
		).
		Value(role).
		Run()
}

func rolePreset(workspace string, provider *Provider, role string) (RolePreset, bool) {
	if provider == nil {
		return RolePreset{}, false
	}
	base, ok := provider.RoleDefaults[role]
	if !ok {
		return RolePreset{}, false
	}

	if workspace == "" {
		return base, true
	}

	rc, err := LoadRoleConfig(workspace)
	if err != nil {
		return base, true
	}

	if rc == nil || len(rc.Roles) == 0 {
		return base, true
	}

	if providerRoles, ok := rc.Roles[provider.Name]; ok {
		if override, ok := providerRoles[role]; ok {
			preset := base
			if override.Model != "" {
				preset.Model = override.Model
			}
			if override.Effort != "" {
				preset.Effort = override.Effort
			}
			return preset, true
		}
	}

	return base, true
}

func effortEnabled(p *Provider) bool {
	return p != nil && (p.EffortFlag != "" || p.EffortTemplate != "")
}

func effortArgExists(p *Provider, args []string) bool {
	if p == nil {
		return false
	}

	if p.EffortFlag != "" && argExists(args, p.EffortFlag) {
		return true
	}

	key := effortTemplateKey(p)
	if key == "" {
		return false
	}
	for i, arg := range args {
		if strings.HasPrefix(arg, key+"=") {
			return true
		}
		if arg == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], key+"=") {
			return true
		}
	}
	return false
}

func effortTemplateKey(p *Provider) string {
	if p == nil || p.EffortTemplate == "" {
		return ""
	}

	template := strings.ReplaceAll(p.EffortTemplate, "%s", "")
	for _, arg := range strings.Fields(template) {
		if eq := strings.Index(arg, "="); eq > -1 {
			return arg[:eq]
		}
	}
	return ""
}

func effortArgs(p *Provider, effort string) []string {
	if p == nil {
		return nil
	}
	if p.EffortTemplate != "" {
		return strings.Fields(fmt.Sprintf(p.EffortTemplate, effort))
	}
	if p.EffortFlag != "" {
		return []string{p.EffortFlag, effort}
	}
	return nil
}

func argExists(args []string, val string) bool {
	for _, arg := range args {
		if arg == val {
			return true
		}
	}
	return false
}

// Cleanup extracts credentials, removes the container, cleans temp state.
func (a *App) Cleanup() {
	if a.clipboardReg != "" {
		_ = os.Remove(a.clipboardReg)
	}
	if a.clipboardDir != "" {
		_ = os.RemoveAll(a.clipboardDir)
	}

	// Extract refreshed credentials from the stopped container.
	if a.ContainerName != "" {
		if a.Credentials != nil {
			finalCreds := ""
			if a.broker != nil {
				finalCreds = a.broker.Credentials()
				_ = a.broker.Close()
			}
			if finalCreds != "" {
				a.Credentials.PersistAll(finalCreds)
			} else {
				// Broker had nothing (e.g. provider has no cred-sync daemon) — docker cp.
				_ = a.Credentials.PersistFromContainer(a.ContainerName, a.Provider.ContainerCredentialPath())
			}
		}
		// Copy back provider persist files (e.g. Gemini google_accounts.json, installation_id).
		if len(a.Provider.PersistFiles) > 0 {
			home := os.Getenv("HOME")
			hostConfigDir := a.Provider.HostConfigDir(home)
			for _, file := range a.Provider.PersistFiles {
				src := a.Provider.ContainerConfigDir() + "/" + file
				dst := filepath.Join(hostConfigDir, file)
				if err := exec.Command("docker", "cp", a.ContainerName+":"+src, dst).Run(); err != nil {
					logVerbose(a.Verbose, "Persist %s: not found in container", file)
				} else {
					logInfo("Persisted %s", file)
				}
			}
		}

		RemoveContainer(a.ContainerName)

		// Remove per-container DinD volume (no-op if it doesn't exist).
		_ = exec.Command("docker", "volume", "rm", a.ContainerName+"-docker").Run()
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

func sharedClipboardDir() string {
	return filepath.Join(os.TempDir(), "mittens-clipboard-shared")
}

func sharedClipboardPIDFile(dir string) string {
	return filepath.Join(dir, "clipboard-sync.pid")
}

func sharedClipboardHeartbeatFile(dir string) string {
	return filepath.Join(dir, "clipboard.heartbeat")
}

func sharedClipboardLockFile(dir string) string {
	return filepath.Join(dir, "clipboard-sync.lock")
}

func sharedClipboardLogFile(dir string) string {
	return filepath.Join(dir, "clipboard-sync.log")
}

func sharedClipboardClientsDir(dir string) string {
	return filepath.Join(dir, "clients")
}

func sharedClipboardStateFile(dir string) string {
	return filepath.Join(dir, "clipboard.state")
}

func sharedClipboardUpdatedAtFile(dir string) string {
	return filepath.Join(dir, "clipboard.updated_at")
}

func sharedClipboardImageFile(dir string) string {
	return filepath.Join(dir, "clipboard.png")
}

func sharedClipboardErrorFile(dir string) string {
	return filepath.Join(dir, "clipboard.error")
}

func sharedClipboardPID(dir string) (int, error) {
	data, err := os.ReadFile(sharedClipboardPIDFile(dir))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func sharedClipboardSyncHealthy(dir string) bool {
	pid, err := sharedClipboardPID(dir)
	if err != nil || pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}

	heartbeatInfo, err := os.Stat(sharedClipboardHeartbeatFile(dir))
	if err != nil {
		pidInfo, pidErr := os.Stat(sharedClipboardPIDFile(dir))
		if pidErr != nil {
			return false
		}
		return time.Since(pidInfo.ModTime()) <= 5*time.Second
	}
	return time.Since(heartbeatInfo.ModTime()) <= 5*time.Second
}

func startSharedClipboardSync(dir string) error {
	logFile, err := os.OpenFile(sharedClipboardLogFile(dir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	syncScript := filepath.Join(containerDir(), "clipboard-sync.sh")
	cmd := exec.Command("bash", syncScript, dir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func staleClipboardLock(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if err != nil {
		return false
	}

	data, err := os.ReadFile(lockPath)
	if err == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
			if err := syscall.Kill(pid, 0); err == nil {
				return false
			}
			return true
		}
	}

	return time.Since(info.ModTime()) > 5*time.Second
}

func ensureSharedClipboardSync() (string, error) {
	dir := sharedClipboardDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(sharedClipboardClientsDir(dir), 0o700); err != nil {
		return "", err
	}

	if sharedClipboardSyncHealthy(dir) {
		return dir, nil
	}

	lockPath := sharedClipboardLockFile(dir)
	for attempt := 0; attempt < 50; attempt++ {
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := lockFile.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
				_ = lockFile.Close()
				_ = os.Remove(lockPath)
				return "", err
			}
			_ = lockFile.Close()
			defer os.Remove(lockPath)

			if sharedClipboardSyncHealthy(dir) {
				return dir, nil
			}
			if err := startSharedClipboardSync(dir); err != nil {
				return "", err
			}

			for wait := 0; wait < 20; wait++ {
				if sharedClipboardSyncHealthy(dir) {
					return dir, nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return "", fmt.Errorf("clipboard sync did not become healthy")
		}
		if !os.IsExist(err) {
			return "", err
		}
		if staleClipboardLock(lockPath) {
			_ = os.Remove(lockPath)
			continue
		}
		time.Sleep(100 * time.Millisecond)
		if sharedClipboardSyncHealthy(dir) {
			return dir, nil
		}
	}

	if sharedClipboardSyncHealthy(dir) {
		return dir, nil
	}
	return "", fmt.Errorf("timed out waiting for shared clipboard sync")
}

func copyFileAtomic(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if info, err := os.Stat(src); err == nil {
		_ = os.Chmod(tmpName, info.Mode().Perm())
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func copySharedClipboardSnapshot(sharedDir, clientDir string) error {
	optionalFiles := [][2]string{
		{sharedClipboardStateFile(sharedDir), sharedClipboardStateFile(clientDir)},
		{sharedClipboardUpdatedAtFile(sharedDir), sharedClipboardUpdatedAtFile(clientDir)},
		{sharedClipboardImageFile(sharedDir), sharedClipboardImageFile(clientDir)},
		{sharedClipboardErrorFile(sharedDir), sharedClipboardErrorFile(clientDir)},
	}

	for _, pair := range optionalFiles {
		if _, err := os.Stat(pair[0]); err == nil {
			if err := copyFileAtomic(pair[0], pair[1]); err != nil {
				return err
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(pair[1])
	}

	return nil
}

func registerClipboardClient(sharedDir, clientDir string) (string, error) {
	regFile, err := os.CreateTemp(sharedClipboardClientsDir(sharedDir), "client-*.path")
	if err != nil {
		return "", err
	}
	if _, err := regFile.WriteString(clientDir + "\n"); err != nil {
		_ = regFile.Close()
		_ = os.Remove(regFile.Name())
		return "", err
	}
	if err := regFile.Close(); err != nil {
		_ = os.Remove(regFile.Name())
		return "", err
	}
	if err := copySharedClipboardSnapshot(sharedDir, clientDir); err != nil {
		_ = os.Remove(regFile.Name())
		return "", err
	}
	return regFile.Name(), nil
}

// buildImage runs docker build.
func (a *App) buildImage() error {

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
			"AI_BINARY":      a.Provider.Binary,
			"AI_INSTALL_CMD": a.Provider.InstallCmd,
			"AI_CONFIG_DIR":  a.Provider.ConfigDir,
		},
		Verbose: a.Verbose,
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
	if a.Provider.ContainerHostname != "" {
		args = append(args, "--hostname", a.Provider.ContainerHostname)
	}

	// Primary workspace mount.
	args = append(args, "-v", a.WorkspaceMountSrc+":/workspace")

	// AI config staging (read-only). Providers that mount the whole config
	// directory for session persistence should not also mount the same host
	// path into the staging location.
	if a.NoHistory || !a.Provider.HistoryMountsWholeConfig {
		args = append(args, "-v", a.Provider.HostConfigDir(home)+":"+a.Provider.StagingConfigDir()+":ro")
	}

	// Environment variables.
	if a.Provider.APIKeyEnv != "" {
		args = append(args, "-e", a.Provider.APIKeyEnv+"="+os.Getenv(a.Provider.APIKeyEnv))
	}
	args = append(args, "-e", "TERM="+envOrDefault("TERM", "xterm-256color"))
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
	if a.Credentials != nil && a.Credentials.TmpFile() != "" && a.Provider.CredentialFile != "" {
		args = append(args, "-v", a.Credentials.TmpFile()+":"+a.Provider.StagingCredentialPath()+":ro")
	}

	// Provider env vars — passed into the container for entrypoint.sh.
	// Note: AI_USERNAME is baked into the image via Dockerfile ENV, not passed at runtime.
	args = append(args,
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
		"-e", "MITTENS_AI_TRUSTED_DIRS_FILE="+a.Provider.TrustedDirsFile,
		"-e", "MITTENS_AI_INIT_SETTINGS_JQ="+a.Provider.InitSettingsJQ,
		"-e", "MITTENS_AI_STOP_HOOK_EVENT="+a.Provider.StopHookEvent,
		"-e", "MITTENS_AI_PERSIST_FILES="+strings.Join(a.Provider.PersistFiles, ","),
	)

	// Provider-specific extra env vars (e.g. DISPLAY for Gemini browser-open detection).
	// An empty value explicitly overrides the image's ENV (e.g. DEBIAN_FRONTEND=noninteractive).
	for k, v := range a.Provider.ContainerEnv {
		args = append(args, "-e", k+"="+v)
	}

	// Credential broker.
	if a.brokerSock != "" {
		// Unix socket mode (Linux): mount the socket directory into the container.
		sockDir := filepath.Dir(a.brokerSock)
		containerSockDir := "/tmp/mittens-broker"
		containerSockPath := containerSockDir + "/broker.sock"
		args = append(args, "-v", sockDir+":"+containerSockDir)
		args = append(args, "-e", "MITTENS_BROKER_SOCK="+containerSockPath)
	} else if a.brokerPort > 0 {
		// TCP mode (macOS/Windows): use host.docker.internal.
		args = append(args, "-e", fmt.Sprintf("MITTENS_BROKER_PORT=%d", a.brokerPort))
		if a.brokerToken != "" {
			args = append(args, "-e", "MITTENS_BROKER_TOKEN="+a.brokerToken)
		}
		args = append(args, "--add-host=host.docker.internal:host-gateway")
	}

	// Session persistence mounts.
	if !a.NoHistory && a.Provider.HistoryMountsWholeConfig {
		hostConfigDir := a.Provider.HostConfigDir(home)
		containerConfigDir := a.Provider.ContainerConfigDir()
		ensureDir(hostConfigDir)
		args = append(args, "-v", hostConfigDir+":"+containerConfigDir)
	} else if !a.NoHistory && a.HostProjectDir != "" {
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

		// Collect stat info for dedup (os.SameFile handles case-insensitive filesystems).
		type mountedDir struct {
			info os.FileInfo
			path string
		}
		var mountedDirs []mountedDir
		if wsInfo, err := os.Stat(a.WorkspaceMountSrc); err == nil {
			mountedDirs = append(mountedDirs, mountedDir{info: wsInfo, path: a.WorkspaceMountSrc})
		}

		for _, dirSpec := range a.ExtraDirs {
			spec := parseExtraDirSpec(dirSpec)
			resolved, err := filepath.Abs(spec.Path)
			if err != nil {
				logWarn("--dir path not resolvable: %s", spec.Path)
				continue
			}
			resolvedInfo, err := os.Stat(resolved)
			if err != nil {
				logError("--dir path does not exist: %s", spec.Path)
				continue
			}

			// Deduplicate against workspace and previously added extra dirs.
			duplicate := false
			for _, m := range mountedDirs {
				if os.SameFile(m.info, resolvedInfo) {
					logVerbose(a.Verbose, "Skipping duplicate mount: %s (same as %s)", spec.Path, m.path)
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			mountedDirs = append(mountedDirs, mountedDir{info: resolvedInfo, path: resolved})

			var extraPath string
			mountMode := ""
			if spec.ReadOnly {
				mountMode = ":ro"
			}
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
						args = append(args, "-v", wtPath+":"+wtPath+mountMode)
						logInfo("Extra directory worktree: %s", wtPath)
					} else {
						logWarn("Failed to create worktree for %s, mounting shared", gitRoot)
						extraPath = resolved
						args = append(args, "-v", resolved+":"+resolved+mountMode)
					}
				} else {
					extraPath = resolved
					args = append(args, "-v", resolved+":"+resolved+mountMode)
				}
			} else {
				extraPath = resolved
				args = append(args, "-v", resolved+":"+resolved+mountMode)
			}
			extraPaths = append(extraPaths, extraPath)
			if spec.ReadOnly {
				logInfo("Extra directory (read-only): %s", extraPath)
			} else {
				logInfo("Extra directory: %s", extraPath)
			}
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
		logVerbose(a.Verbose, "Firewall domains: %d extra", len(firewallDomains))
		args = append(args, "-e", "MITTENS_FIREWALL_EXTRA="+strings.Join(firewallDomains, ","))
	}

	// Security hardening: apply unless a resolver (e.g. docker dind) already requested --privileged.
	isPrivileged := false
	for _, arg := range resolverArgs {
		if arg == "--privileged" {
			isPrivileged = true
			break
		}
	}
	if !isPrivileged {
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
		sharedDir, err := ensureSharedClipboardSync()
		if err == nil {
			clientDir, clientErr := os.MkdirTemp("", "mittens-clipboard.*")
			if clientErr != nil {
				logWarn("Clipboard image sync: disabled: %v", clientErr)
			} else {
				regFile, regErr := registerClipboardClient(sharedDir, clientDir)
				if regErr != nil {
					_ = os.RemoveAll(clientDir)
					logWarn("Clipboard image sync: disabled: %v", regErr)
				} else {
					a.clipboardDir = clientDir
					a.clipboardReg = regFile
					args = append(args, "-v", clientDir+":/tmp/mittens-clipboard:ro")
					logInfo("Clipboard image sync: enabled via %s", sharedDir)
				}
			}
			if a.clipboardDir != "" && a.Provider != nil && a.Provider.Name == "codex" {
				args = append(args,
					"-e", "MITTENS_ENABLE_X11_CLIPBOARD=true",
					"-e", "MITTENS_X11_CLIPBOARD_IMAGE=/tmp/mittens-clipboard/clipboard.png",
					"-e", "MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS=5",
					"-e", "DISPLAY=:99",
				)
				logInfo("Codex X11 clipboard bridge: enabled")
			}
		} else {
			logWarn("Clipboard image sync: disabled: %v", err)
		}
	}

	// Drop zone for drag-and-drop path translation.
	if dir, err := os.MkdirTemp("", "mittens-drop.*"); err == nil {
		a.dropDir = dir
		a.tempDirs = append(a.tempDirs, dir)
		args = append(args, "-v", dir+":/tmp/mittens-drops:ro")
		logInfo("Drag-and-drop path translation: enabled")
	}

	return args
}

// runContainer runs docker run with the assembled args.
func (a *App) runContainer(dockerArgs []string) error {
	stdin, cleanup := a.newStdinProxy()
	if cleanup != nil {
		defer cleanup()
	}

	code, err := RunContainer(dockerArgs, a.ImageName, a.ImageTag, a.Shell, a.Provider.Binary, a.ClaudeArgs, stdin)
	if err != nil {
		return fmt.Errorf("docker run failed: %w", err)
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// newStdinProxy builds a PTY-based stdin proxy that translates host paths in
// pasted content while preserving the TTY for Docker's -it flags.
// Returns (nil, nil) if the drop zone was not created or PTY setup fails.
func (a *App) newStdinProxy() (stdin *os.File, cleanup func()) {
	if a.dropDir == "" {
		return nil, nil
	}

	mapper := &PathMapper{
		dropDir:          a.dropDir,
		containerDropDir: "/tmp/mittens-drops",
	}

	// Primary workspace mapping.
	if a.WorkspaceMountSrc != "" {
		mapper.mappings = append(mapper.mappings, pathMapping{
			hostPrefix:      a.WorkspaceMountSrc,
			containerPrefix: "/workspace",
		})
	}

	// EffectiveWorkspace (worktree) if different and mounted at its own path.
	if a.EffectiveWorkspace != "" && a.EffectiveWorkspace != a.WorkspaceMountSrc {
		mapper.mappings = append(mapper.mappings, pathMapping{
			hostPrefix:      a.EffectiveWorkspace,
			containerPrefix: a.EffectiveWorkspace,
		})
	}

	// Extra directory mappings (mounted at their own absolute path).
	for _, dirSpec := range a.ExtraDirs {
		spec := parseExtraDirSpec(dirSpec)
		resolved, err := filepath.Abs(spec.Path)
		if err != nil {
			continue
		}
		mapper.mappings = append(mapper.mappings, pathMapping{
			hostPrefix:      resolved,
			containerPrefix: resolved,
		})
	}

	proxy := NewDropProxy(os.Stdin, mapper)
	slave, cleanupFn, err := StartPTYProxy(proxy)
	if err != nil {
		logVerbose(a.Verbose, "PTY proxy setup failed, using direct stdin: %v", err)
		return nil, nil
	}

	return slave, cleanupFn
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
// On macOS, if terminal-notifier is installed, clicking the notification
// will focus the originating terminal session.
// logFn is an optional logger (broker.blog); pass nil to skip logging.
func notifyOnHost(container, event, message, displayName string, focus TerminalFocus, logFn func(string, ...interface{})) {
	log := func(format string, args ...interface{}) {
		if logFn != nil {
			logFn(format, args...)
		}
	}

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

	log("notify: event=%s terminal=%s/%s", event, focus.Kind, focus.ID)

	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		if path, err := exec.LookPath("terminal-notifier"); err == nil {
			args := []string{"-title", title, "-message", body, "-sound", "Glass"}
			if focus.BundleID != "" {
				args = append(args, "-activate", focus.BundleID)
				log("notify: terminal-notifier -activate %s", focus.BundleID)
			}
			if focusCmd := focus.FocusCommand(); focusCmd != nil {
				executeStr := shellJoin(focusCmd)
				args = append(args, "-execute", executeStr)
				log("notify: terminal-notifier -execute %s", executeStr)
			}
			cmd = exec.Command(path, args...)
		} else {
			log("notify: terminal-notifier not found, using osascript via stdin")
			// Pass script via stdin to avoid Script Editor activation.
			script := fmt.Sprintf(`display notification %q with title %q sound name "Glass"`, body, title)
			cmd = exec.Command("osascript")
			cmd.Stdin = strings.NewReader(script)
		}
	} else {
		cmd = exec.Command("notify-send", title, body)
	}

	if cmd != nil {
		if err := cmd.Start(); err != nil {
			log("notify: failed to start: %v", err)
		} else {
			log("notify: sent %q", body)
		}
	}
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

type extraDirSpec struct {
	Path     string
	ReadOnly bool
}

func parseExtraDirSpec(s string) extraDirSpec {
	if strings.HasPrefix(s, "ro:") {
		return extraDirSpec{
			Path:     strings.TrimPrefix(s, "ro:"),
			ReadOnly: true,
		}
	}
	return extraDirSpec{Path: s}
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
  --verbose, -v     Show detailed output (Docker build, extension setup)
  --no-config       Skip project config file loading
  --no-history      Disable session persistence (fully ephemeral)
  --resume [ID]     Resume last session, or a specific session by ID
  --no-build        Skip the Docker image build step
  --firewall-dev    Developer-friendly firewall (adds cloud APIs, apt repos)
  --docker MODE     Docker engine: dind (isolated daemon) or host (share host socket)
  --yolo            Skip all permission prompts
  --no-notify       Disable desktop notifications
  --network-host    Use host networking (default: bridge)
  --worktree        Git worktree isolation per invocation
  --shell           Start a bash shell instead of Claude
  --name NAME       Name this instance (default: PID-based)
  --dir PATH        Mount an additional directory (repeatable)
  --dir-ro PATH     Mount an additional directory as read-only (repeatable)
  --worker          Use worker role preset (model + effort)
  --planner         Use planner role preset (model + effort)
  --provider NAME   AI provider to use (claude, codex, gemini; default: claude)
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
			{Name: "--docker", Description: "Docker engine mode: dind or host", ArgType: "string"},
			{Name: "--worktree", Description: "Git worktree isolation per invocation"},
			{Name: "--yolo", Description: "Skip all permission prompts"},
			{Name: "--no-notify", Description: "Disable desktop notifications"},
			{Name: "--network-host", Description: "Use host networking (default: bridge)"},
			{Name: "--no-history", Description: "Disable session persistence (fully ephemeral)"},
			{Name: "--no-build", Description: "Skip the Docker image build step"},
			{Name: "--resume", Description: "Resume last session, or a specific session by ID", ArgType: "string"},
			{Name: "--name", Description: "Name this instance (default: PID-based)", ArgType: "string"},
			{Name: "--shell", Description: "Start a bash shell instead of Claude"},
			{Name: "--dir", Description: "Mount an additional directory (repeatable)", ArgType: "path"},
			{Name: "--dir-ro", Description: "Mount an additional directory read-only (repeatable)", ArgType: "path"},
			{Name: "--worker", Description: "Use worker role preset (model + effort)"},
			{Name: "--planner", Description: "Use planner role preset (model + effort)"},
			{Name: "--provider", Description: "AI provider (claude, codex, gemini)", ArgType: "string"},
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
