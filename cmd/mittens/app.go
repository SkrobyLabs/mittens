package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	firewallext "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/firewall"
	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/credutil"
	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

// Platform function variables — defaults in platform_default.go,
// overridden via init() in app_darwin.go / app_windows.go.
var (
	platformStartBroker        = startBrokerDefault
	platformOpenURL            = openURLDefault
	platformNotify             = notifyDefault
	platformClipboardSync      = clipboardSyncDefault
	platformCheckNotifications = checkNotificationsDefault
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
	Rebuild       bool
	Yolo          bool
	NoNotify      bool
	NetworkHost   bool
	NoSSHEgress   bool
	FirewallLearn bool
	Worktree      bool
	Shell         bool
	Profile       string // model profile name (e.g. "planner", "fast")
	ImagePasteKey string // "ctrl+v" or "meta+v"
	HostBridge    HostBridgeConfig
	PathTranslate bool
	ExtraDirs     []string
	FirewallExtra []string
	InstanceName  string // user-provided name via --name
	ClaudeArgs    []string

	// Computed state
	Workspace          string // git root or cwd
	EffectiveWorkspace string // worktree path if --worktree, else same as Workspace
	WorkspaceMountSrc  string // what actually gets bind-mounted into the container (identity mount)
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

	// Worktree suffix (computed once per Run)
	worktreeSuffix string

	// Credential staging dirs ("staging_path:target_dir") from extension resolvers
	credStagingDirs []string

	// Launch boundary summary, populated while assembling docker args.
	launchSummary LaunchSummary

	// Drop zone for drag-and-drop file translation
	dropDir string

	// Terminal focus (for click-to-focus notifications)
	terminalFocus TerminalFocus

	// Host broker (credentials, URLs, notifications, OAuth)
	broker      *HostBroker
	brokerPort  int
	brokerToken string
	brokerSock  string // Unix socket path (Linux mode)

	hostPolicyConfigured bool
}

// coreFlags maps flag names to a setter function on *App.
var coreFlags = map[string]func(*App){
	"--verbose":        func(a *App) { a.Verbose = true },
	"-v":               func(a *App) { a.Verbose = true },
	"--no-config":      func(a *App) { a.NoConfig = true },
	"--no-history":     func(a *App) { a.NoHistory = true },
	"--no-build":       func(a *App) { a.NoBuild = true },
	"--rebuild":        func(a *App) { a.Rebuild = true },
	"--no-yolo":        func(a *App) { a.Yolo = false },
	"--no-notify":      func(a *App) { a.NoNotify = true },
	"--network-host":   func(a *App) { a.NetworkHost = true },
	"--worktree":       func(a *App) { a.Worktree = true },
	"--shell":          func(a *App) { a.Shell = true },
	"--firewall-learn": func(a *App) { a.FirewallLearn = true },
	"--worker":         func(a *App) {}, // legacy, ignored //legacy-delete-after:2026-04-21
	"--planner":        func(a *App) {}, // legacy, ignored //legacy-delete-after:2026-04-21
	"--firewall-dev":   func(a *App) { firewallext.DevMode = true },
}

// coreFlagsWithArg maps flag names that consume the next argument.
var coreFlagsWithArg = map[string]func(*App, string){
	"--dir":             func(a *App, val string) { a.ExtraDirs = append(a.ExtraDirs, val) },
	"--dir-ro":          func(a *App, val string) { a.ExtraDirs = append(a.ExtraDirs, "ro:"+val) },
	"--name":            func(a *App, val string) { a.InstanceName = val },
	"--provider":        func(a *App, val string) {}, // already applied in main.go pre-scan
	"--image-paste-key": func(a *App, val string) { a.ImagePasteKey = val },
	"--profile":         func(a *App, val string) { a.Profile = val },
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

		// Info flags are handled in runMain before config loading. Keep this
		// fallback for direct tests or embedded callers that invoke ParseFlags.
		if arg == "--extensions" {
			printExtensions(a.Extensions)
			os.Exit(0)
		}
		if arg == "--json-caps" {
			printJSONCaps(a.Extensions)
			os.Exit(0)
		}

		// Unrecognised flag or positional arg -- forward to the selected provider.
		// Providers accept their own flags like --model, --print, etc.
		a.ClaudeArgs = append(a.ClaudeArgs, arg)
		i++
	}
	return nil
}

// Run is the main orchestration method.
func (a *App) Run() error {
	defer a.Cleanup()
	a.ensureHostPolicyDefaults()

	// Compute worktree suffix once for consistent naming across all worktrees.
	a.worktreeSuffix = fmt.Sprintf("wt-%d", os.Getpid())

	// Precondition checks.
	if os.Getenv("HOME") == "" {
		return fmt.Errorf("HOME environment variable is not set")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is not installed or not in PATH")
	}

	home := os.Getenv("HOME")
	providerPlan := a.Provider.RuntimePlan()
	ensureDir(a.Provider.HostConfigDir(home))

	// Detect workspace.
	a.Workspace = detectWorkspace()
	a.EffectiveWorkspace = a.Workspace
	cwd := effectiveCwd()
	a.WorkspaceMountSrc = cwd

	// A one-time firewall-learn pass armed via the wizard fires exactly once.
	if !a.FirewallLearn && consumeLearnArm(a.Workspace) {
		a.FirewallLearn = true
		logInfo("Firewall learn: armed pass consumed for this run")
	}
	if a.FirewallLearn {
		a.enableFirewallForLearn()
	}

	// Session persistence setup.
	if !a.NoHistory {
		if providerPlan.HistoryMountsWholeConfig {
			ensureDir(a.Provider.HostConfigDir(home))
		} else if providerPlan.HistoryMountsProjectDirs {
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
		wtPath, err := a.createWorktree(gitRoot, a.worktreeSuffix)
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

	setupPlans, err := a.capabilitySetupPlans(home)
	if err != nil {
		return err
	}
	resolverDockerArgs, resolverFirewallExtra, credStagingDirs := flattenCapabilitySetupPlans(setupPlans)
	a.credStagingDirs = credStagingDirs

	// Collect provider and capability build state.
	a.imageTagParts = append(a.imageTagParts, providerPlan.ImageTagParts...)
	a.buildArgs = copyStringMap(providerPlan.BuildArgs)
	var installExtensions []string
	for _, plan := range capabilityRuntimePlans(a.Extensions, home, a.Provider.HomePath()) {
		if plan.Install {
			installExtensions = append(installExtensions, plan.Name)
		}
		for k, v := range plan.BuildArgs {
			a.buildArgs[k] = v
		}
		if plan.ImageTagPart != "" {
			a.imageTagParts = append(a.imageTagParts, plan.ImageTagPart)
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
	// Skip OAuth credential staging when using a custom base URL (local/third-party
	// provider) — the stored tokens belong to the original provider and will cause
	// refresh failures that block the CLI.
	if providerPlan.SkipCredentials || a.Provider.UsingCustomBaseURL() || os.Getenv("OLLAMA_HOST") != "" {
		logInfo("Local/custom provider detected, skipping OAuth credential staging")
		a.Credentials = &CredentialManager{}
	} else {
		a.Credentials = NewCredentialManager(a.Provider)
		if err := a.Credentials.Setup(); err != nil {
			logWarn("Credential setup: %v", err)
		}
		if a.Credentials.TmpFile() != "" {
			logVerbose(a.Verbose, "Credentials staged: %s", a.Credentials.TmpFile())
		} else {
			logVerbose(a.Verbose, "No credentials found")
		}
	}

	// Start broker (credential sync + host URL opening via TCP).
	{
		var seed string
		if a.Credentials.TmpFile() != "" {
			data, _ := os.ReadFile(a.Credentials.TmpFile())
			if len(data) > 0 && json.Valid(data) && !credutil.IsExpired(data, time.Now()) {
				if fields, ok := credutil.ObjectFieldCount(data); ok && fields > 0 {
					seed = string(data)
				}
			}
		}
		a.broker = NewHostBroker("", seed, a.Credentials.Stores())
		a.broker.Name = a.Provider.Name
		a.broker.Learn = a.FirewallLearn
		a.broker.Host = a.HostBridge
		a.broker.Host.Notifications = a.broker.Host.Notifications && !a.NoNotify
		if token, err := randomHex(16); err == nil {
			a.brokerToken = token
			a.broker.AuthToken = token
		} else {
			return fmt.Errorf("generate broker token: %w", err)
		}
		blogFnOpen := a.broker.blog
		a.broker.OnOpen = func(url string) {
			openOnHost(url, blogFnOpen)
		}
		containerName := a.ContainerName
		a.broker.OnLoginForward = func(port int, requestURI string) (*LoginForwardResponse, error) {
			return ForwardLoginRequest(containerName, port, requestURI)
		}
		if a.FirewallLearn {
			a.broker.OnEgressDeny = func(host string) {
				logInfo("Firewall learn: observed egress to %s (outside allowlist)", host)
			}
		} else {
			a.broker.OnEgressDeny = func(host string) {
				logWarn("Firewall blocked egress to %s (not in allowlist; allow it with 'mittens policy allow %s')", host, host)
			}
		}
		if a.broker.Host.Notifications {
			displayName := a.Provider.DisplayName
			focus := a.terminalFocus
			blogFn := a.broker.blog
			a.broker.OnNotify = func(container, event, message string) {
				notifyOnHost(container, event, message, displayName, focus, blogFn)
			}
			platformCheckNotifications()
		}

		// Persistent broker log for debugging.
		logDir := filepath.Join(home, ".mittens", "logs")
		ensureDir(logDir)
		rotateBrokerLog(filepath.Join(logDir, "broker.log"))
		if lf, err := os.OpenFile(filepath.Join(logDir, "broker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			a.broker.LogFile = lf
		}

		platformStartBroker(a)
	}

	logVerbose(a.Verbose, "Image tag: %s:%s", a.ImageName, a.ImageTag)

	// Apply model profile before Docker build so interactive setup happens first.
	if err := a.maybeApplyProfile(); err != nil {
		if err == huh.ErrUserAborted {
			fmt.Fprintln(os.Stderr, "\nCancelled.")
			return nil
		}
		return err
	}
	a.applyProviderDefaultArgs(providerPlan)

	// Build Docker image.
	if !a.NoBuild {
		if err := a.buildImage(); err != nil {
			return err
		}
	}

	// Yolo mode: prepend skip-permissions flag.
	if a.Yolo {
		a.ClaudeArgs = append([]string{a.Provider.SkipPermsFlag}, a.ClaudeArgs...)
		logWarn("YOLO mode: all permission prompts will be skipped")
	}

	// Assemble docker run args and run.
	dockerArgs := a.assembleDockerArgs(resolverDockerArgs, resolverFirewallExtra)

	fmt.Fprint(os.Stderr, a.launchSummary.Render())
	if len(a.ClaudeArgs) > 0 {
		logInfo("%s args: %s", a.Provider.DisplayName, strings.Join(a.ClaudeArgs, " "))
	}

	if a.Verbose {
		logInfo("Command: docker run %s", strings.Join(sanitizeDockerArgsForLog(dockerArgs), " "))
	}

	return a.runContainer(dockerArgs)
}

func (a *App) maybeApplyProfile() error {
	if a.Profile == "" {
		// If profiles exist and we're interactive, offer to pick one.
		if a.Shell || argExists(a.ClaudeArgs, "--print") || !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil
		}
		profile, err := a.promptProfilePicker()
		if err != nil {
			return err
		}
		if profile == "" {
			return nil
		}
		a.Profile = profile
	}

	preset, ok := loadProfile(a.Workspace, a.Provider, a.Profile)
	if !ok {
		// Profile doesn't exist — prompt to create it.
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("profile %q not found for provider %s (run: mittens init --profile %s)", a.Profile, a.Provider.Name, a.Profile)
		}
		var err error
		preset, err = promptNewProfile(a.Provider, a.Profile)
		if err != nil {
			return err
		}
		if err := saveProfile(a.Workspace, a.Provider, a.Profile, preset); err != nil {
			return err
		}
	}

	extras := make([]string, 0, 4)
	if a.Provider.ModelFlag != "" && preset.Model != "" && !argExists(a.ClaudeArgs, a.Provider.ModelFlag) {
		extras = append(extras, a.Provider.ModelFlag, preset.Model)
	}
	if effortEnabled(a.Provider) && preset.Effort != "" && !effortArgExists(a.Provider, a.ClaudeArgs) {
		extras = append(extras, effortArgs(a.Provider, preset.Effort)...)
	}
	if len(extras) > 0 {
		logInfo("Profile %q: model=%s effort=%s", a.Profile, preset.Model, preset.Effort)
		a.ClaudeArgs = append(extras, a.ClaudeArgs...)
	}
	return nil
}

// promptNewProfile interactively creates a new profile preset.
func promptNewProfile(provider *Provider, name string) (ProfilePreset, error) {
	fmt.Fprintf(os.Stderr, "\nProfile %q not found for %s — let's set it up.\n\n", name, provider.DisplayName)

	var preset ProfilePreset

	model := ""
	if err := huh.NewInput().
		Title(fmt.Sprintf("Model for %q profile (%s)", name, provider.DisplayName)).
		Placeholder("e.g. opus, haiku, sonnet").
		Value(&model).
		Run(); err != nil {
		return preset, err
	}
	preset.Model = strings.TrimSpace(model)

	if effortEnabled(provider) {
		effort := ""
		if err := huh.NewSelect[string]().
			Title(fmt.Sprintf("Effort for %q profile", name)).
			Options(
				huh.NewOption("(none)", ""),
				huh.NewOption("low", "low"),
				huh.NewOption("medium", "medium"),
				huh.NewOption("high", "high"),
				huh.NewOption("max", "max"),
			).
			Value(&effort).
			Run(); err != nil {
			return preset, err
		}
		preset.Effort = effort
	}

	return preset, nil
}

// promptProfilePicker shows a profile selection menu if profiles exist.
// Returns empty string if no profiles or user selects "Default".
func (a *App) promptProfilePicker() (string, error) {
	pc, err := LoadProfileConfig(a.Workspace)
	if err != nil {
		return "", nil
	}
	providerProfiles := pc.Profiles[a.Provider.Name]
	if len(providerProfiles) == 0 {
		return "", nil
	}

	options := []huh.Option[string]{
		huh.NewOption("Default (no profile)", ""),
	}
	for name, preset := range providerProfiles {
		label := name
		if preset.Model != "" {
			label += " — " + preset.Model
		}
		if preset.Effort != "" {
			label += " (" + preset.Effort + ")"
		}
		options = append(options, huh.NewOption(label, name))
	}

	var choice string
	if err := huh.NewSelect[string]().
		Title("Model profile").
		Options(options...).
		Value(&choice).
		Run(); err != nil {
		return "", err
	}
	return choice, nil
}

// loadProfile looks up a named profile for the given provider and workspace.
func loadProfile(workspace string, provider *Provider, name string) (ProfilePreset, bool) {
	if provider == nil {
		return ProfilePreset{}, false
	}

	pc, err := LoadProfileConfig(workspace)
	if err != nil || pc == nil || len(pc.Profiles) == 0 {
		return ProfilePreset{}, false
	}

	if providerProfiles, ok := pc.Profiles[provider.Name]; ok {
		if preset, ok := providerProfiles[name]; ok {
			return preset, true
		}
	}

	return ProfilePreset{}, false
}

// saveProfile persists a profile preset for the given provider and workspace.
func saveProfile(workspace string, provider *Provider, name string, preset ProfilePreset) error {
	pc, err := LoadProfileConfig(workspace)
	if err != nil {
		pc = &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{}}
	}
	if pc.Profiles == nil {
		pc.Profiles = map[string]map[string]ProfilePreset{}
	}
	if pc.Profiles[provider.Name] == nil {
		pc.Profiles[provider.Name] = map[string]ProfilePreset{}
	}
	pc.Profiles[provider.Name][name] = preset
	return SaveProfileConfig(workspace, pc)
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

func (a *App) applyProviderDefaultArgs(plan ProviderRuntimePlan) {
	defaults := append([]string(nil), plan.DefaultArgs...)
	if a.Provider.ModelFlag != "" && !argExists(a.ClaudeArgs, a.Provider.ModelFlag) {
		model := plan.DefaultModel
		if model == "" && plan.LocalModelSource != "" {
			model = detectLocalModel(plan.LocalModelSource)
		}
		if model != "" {
			defaults = append(defaults, a.Provider.ModelFlag, model)
		}
	}
	a.ClaudeArgs = appendMissingProviderArgs(a.ClaudeArgs, defaults)
}

func appendMissingProviderArgs(args, defaults []string) []string {
	var missing []string
	for i := 0; i < len(defaults); i++ {
		arg := defaults[i]
		if strings.HasPrefix(arg, "-") && argExists(args, arg) {
			if i+1 < len(defaults) && !strings.HasPrefix(defaults[i+1], "-") {
				i++
			}
			continue
		}
		missing = append(missing, arg)
	}
	out := append([]string(nil), missing...)
	out = append(out, args...)
	return out
}

func detectLocalModel(source string) string {
	switch source {
	case "ollama":
		return detectOllamaModel()
	default:
		return ""
	}
}

func detectOllamaModel() string {
	if model := os.Getenv("MITTENS_OLLAMA_MODEL"); model != "" {
		return model
	}
	if model := firstOllamaModelFrom("ps"); model != "" {
		return model
	}
	if model := firstOllamaModelFrom("list"); model != "" {
		return model
	}
	return ""
}

func firstOllamaModelFrom(subcmd string) string {
	out, err := exec.Command("ollama", subcmd).Output()
	if err != nil {
		return ""
	}
	return pickOllamaModel(string(out))
}

func pickOllamaModel(out string) string {
	var models []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.EqualFold(fields[0], "NAME") {
			continue
		}
		models = append(models, fields[0])
	}
	for _, model := range models {
		lower := strings.ToLower(model)
		if strings.Contains(lower, "coder") || strings.Contains(lower, "code") {
			return model
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

// Cleanup extracts credentials, removes the container, cleans temp state.
func (a *App) Cleanup() {
	if a.clipboardReg != "" {
		_ = os.Remove(a.clipboardReg)
	}
	if a.clipboardDir != "" {
		_ = os.RemoveAll(a.clipboardDir)
	}

	// Persist refreshed credentials from the broker.
	if a.ContainerName != "" {
		if a.Credentials != nil {
			if a.broker != nil {
				finalCreds, finalAction, err := a.finalContainerCredentials()
				if err != nil {
					logVerbose(a.Verbose, "Final credential snapshot: %v", err)
				}
				if finalAction == finalCredentialPersist {
					a.Credentials.PersistAll(finalCreds)
				} else if finalAction == finalCredentialClear {
					a.Credentials.DeleteAll()
				} else if brokerCreds := a.broker.Credentials(); brokerCreds != "" && !credutil.IsExpired([]byte(brokerCreds), time.Now()) {
					a.Credentials.PersistAll(brokerCreds)
				}
				_ = a.broker.Close()
			}
		}
		// Copy back provider runtime state from the container snapshot.
		providerPlan := a.Provider.RuntimePlan()
		if len(providerPlan.AI.PersistFiles) > 0 || len(providerPlan.AI.PersistDirs) > 0 || len(providerPlan.AI.PersistGlobs) > 0 {
			home := os.Getenv("HOME")
			hostConfigDir := a.Provider.HostConfigDir(home)
			if err := persistContainerConfig(a.ContainerName, a.Provider.ContainerConfigDir(), hostConfigDir, providerPlan.AI.PersistFiles, providerPlan.AI.PersistDirs, providerPlan.AI.PersistGlobs, a.Verbose); err != nil {
				logWarn("Persist provider state: %v", err)
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

type finalCredentialAction int

const (
	finalCredentialIgnore finalCredentialAction = iota
	finalCredentialPersist
	finalCredentialClear
)

// finalContainerCredentials reads the provider credential file from the stopped
// container. Missing or empty credentials are treated as logout; expired
// credentials are ignored so account metadata in the host file survives.
func (a *App) finalContainerCredentials() (string, finalCredentialAction, error) {
	if a.Provider == nil || a.Provider.CredentialFile == "" || a.ContainerName == "" {
		return "", finalCredentialIgnore, nil
	}

	tmpDir, err := os.MkdirTemp("", "mittens-final-creds.*")
	if err != nil {
		return "", finalCredentialIgnore, fmt.Errorf("create final credential temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dst := filepath.Join(tmpDir, filepath.Base(a.Provider.CredentialFile))
	src := a.ContainerName + ":" + a.Provider.ContainerCredentialPath()
	if out, err := exec.Command("docker", "cp", src, dst).CombinedOutput(); err != nil {
		if dockerCpMissingPath(out) {
			return "", finalCredentialClear, nil
		}
		return "", finalCredentialIgnore, fmt.Errorf("copy final credential file: %w: %s", err, out)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return "", finalCredentialClear, nil
		}
		return "", finalCredentialIgnore, fmt.Errorf("read final credential file: %w", err)
	}
	return classifyFinalCredentialJSON(data, time.Now())
}

func dockerCpMissingPath(out []byte) bool {
	msg := strings.ToLower(string(out))
	return strings.Contains(msg, "could not find") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "not found")
}

func classifyFinalCredentialJSON(data []byte, now time.Time) (string, finalCredentialAction, error) {
	if len(data) == 0 {
		return "", finalCredentialClear, nil
	}
	if !json.Valid(data) {
		return "", finalCredentialClear, nil
	}
	if fields, ok := credutil.ObjectFieldCount(data); !ok || fields == 0 {
		return "", finalCredentialClear, nil
	}
	if credutil.IsExpired(data, now) {
		return "", finalCredentialIgnore, nil
	}
	return string(data), finalCredentialPersist, nil
}

type clipboardPaths struct {
	dir string
}

func newClipboardPaths() clipboardPaths {
	return clipboardPaths{dir: filepath.Join(os.TempDir(), "mittens-clipboard-shared")}
}

func newClipboardPathsAt(dir string) clipboardPaths {
	return clipboardPaths{dir: dir}
}

func (cp clipboardPaths) pidFile() string       { return filepath.Join(cp.dir, "clipboard-sync.pid") }
func (cp clipboardPaths) heartbeatFile() string { return filepath.Join(cp.dir, "clipboard.heartbeat") }
func (cp clipboardPaths) lockFile() string      { return filepath.Join(cp.dir, "clipboard-sync.lock") }
func (cp clipboardPaths) logFile() string       { return filepath.Join(cp.dir, "clipboard-sync.log") }
func (cp clipboardPaths) clientsDir() string    { return filepath.Join(cp.dir, "clients") }
func (cp clipboardPaths) stateFile() string     { return filepath.Join(cp.dir, "clipboard.state") }
func (cp clipboardPaths) updatedAtFile() string { return filepath.Join(cp.dir, "clipboard.updated_at") }
func (cp clipboardPaths) imageFile() string     { return filepath.Join(cp.dir, "clipboard.png") }
func (cp clipboardPaths) errorFile() string     { return filepath.Join(cp.dir, "clipboard.error") }

func (cp clipboardPaths) pid() (int, error) {
	data, err := os.ReadFile(cp.pidFile())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func (cp clipboardPaths) syncHealthy() bool {
	pid, err := cp.pid()
	if err != nil || pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}

	heartbeatInfo, err := os.Stat(cp.heartbeatFile())
	if err != nil {
		pidInfo, pidErr := os.Stat(cp.pidFile())
		if pidErr != nil {
			return false
		}
		return time.Since(pidInfo.ModTime()) <= 5*time.Second
	}
	return time.Since(heartbeatInfo.ModTime()) <= 5*time.Second
}

func startSharedClipboardSync(cp clipboardPaths) error {
	logFile, err := os.OpenFile(cp.logFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	syncScript := filepath.Join(containerDir(), "clipboard-sync.sh")
	cmd := exec.Command("bash", syncScript, cp.dir)
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
	cp := newClipboardPaths()
	if err := os.MkdirAll(cp.dir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(cp.clientsDir(), 0o700); err != nil {
		return "", err
	}

	if cp.syncHealthy() {
		return cp.dir, nil
	}

	lockPath := cp.lockFile()
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

			if cp.syncHealthy() {
				return cp.dir, nil
			}
			if err := startSharedClipboardSync(cp); err != nil {
				return "", err
			}

			for wait := 0; wait < 20; wait++ {
				if cp.syncHealthy() {
					return cp.dir, nil
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
		if cp.syncHealthy() {
			return cp.dir, nil
		}
	}

	if cp.syncHealthy() {
		return cp.dir, nil
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
	shared := newClipboardPathsAt(sharedDir)
	client := newClipboardPathsAt(clientDir)
	optionalFiles := [][2]string{
		{shared.stateFile(), client.stateFile()},
		{shared.updatedAtFile(), client.updatedAtFile()},
		{shared.imageFile(), client.imageFile()},
		{shared.errorFile(), client.errorFile()},
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
	shared := newClipboardPathsAt(sharedDir)
	regFile, err := os.CreateTemp(shared.clientsDir(), "client-*.path")
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

func (a *App) ensureHostPolicyDefaults() {
	if a.hostPolicyConfigured {
		return
	}
	a.HostBridge = defaultHostBridgeConfig()
	a.PathTranslate = true
}

func (a *App) applyProjectPolicy(policy *ProjectPolicy) {
	if policy == nil {
		return
	}
	a.Profile = policy.Provider.Profile
	a.ExtraDirs = appendPolicyMounts(a.ExtraDirs, policy.Workspace.Mounts)
	a.FirewallExtra = append(a.FirewallExtra, policy.Network.ExtraDomains...)
	a.NetworkHost = policy.Network.Mode == "host" || policy.Execution.NetworkHost
	if policy.Network.SSHEgress != nil {
		a.NoSSHEgress = !*policy.Network.SSHEgress
	}
	a.Worktree = policy.Workspace.Mode == "worktree" || policy.Execution.Worktree
	a.Shell = policy.Execution.Shell
	if policy.Execution.Yolo != nil {
		a.Yolo = *policy.Execution.Yolo
	}
	if policy.Execution.History != nil {
		a.NoHistory = !*policy.Execution.History
	}
	if policy.Execution.Notify != nil {
		a.NoNotify = !*policy.Execution.Notify
	}
	if policy.Options != nil {
		if key := policy.Options["image_paste_key"]; key != "" {
			a.ImagePasteKey = key
		}
		if name := policy.Options["name"]; name != "" {
			a.InstanceName = name
		}
	}
	a.hostPolicyConfigured = true
	a.HostBridge = HostBridgeConfig{
		OpenURLs:        policy.Host.OpenURLs != "deny",
		Notifications:   boolValue(policy.Host.Notifications, true),
		ClipboardImages: boolValue(policy.Host.ClipboardImages, true),
	}
	a.PathTranslate = boolValue(policy.Host.PathTranslation, true)
	if !a.HostBridge.Notifications {
		a.NoNotify = true
	}
	a.applyPolicyCapabilities(policy)
	a.ClaudeArgs = append(a.ClaudeArgs, policy.ExtraArgs...)
}

func appendPolicyMounts(extraDirs []string, mounts []PolicyMount) []string {
	for _, mount := range mounts {
		if mount.Access == "ro" {
			extraDirs = append(extraDirs, "ro:"+mount.Path)
		} else {
			extraDirs = append(extraDirs, mount.Path)
		}
	}
	return extraDirs
}

// enableFirewallForLearn ensures the firewall proxy runs for a learn pass even
// when policy disabled it, so out-of-allowlist hosts can be observed. The proxy
// runs permissive-but-logging; the allowlist itself is not enforced this run.
func (a *App) enableFirewallForLearn() {
	for _, ext := range a.Extensions {
		if ext != nil && ext.Name == "firewall" {
			if !ext.Enabled {
				ext.Enabled = true
				logInfo("Firewall learn: enabled the firewall proxy in permissive mode for discovery")
			}
			return
		}
	}
}

func (a *App) applyPolicyCapabilities(policy *ProjectPolicy) {
	extByName := make(map[string]*registry.Extension, len(a.Extensions))
	for _, ext := range a.Extensions {
		if ext != nil {
			extByName[ext.Name] = ext
		}
	}

	if firewall := extByName["firewall"]; firewall != nil {
		switch policy.Network.Firewall {
		case "disabled":
			firewall.Enabled = false
		case "dev":
			firewall.Enabled = true
			firewallext.DevMode = true
		case "custom":
			firewall.Enabled = true
			firewall.RawArg = policy.Network.CustomConfig
			firewall.Args = []string{policy.Network.CustomConfig}
		default:
			firewall.Enabled = true
		}
	}

	if policy.Execution.Docker != "" {
		enablePolicyExtension(extByName["docker"], CapabilityPolicy{
			Name: "docker",
			Args: []string{policy.Execution.Docker},
		})
	}

	for _, capability := range policy.Capabilities {
		if capability.Name == "" || capability.Name == "firewall" {
			continue
		}
		enablePolicyExtension(extByName[capability.Name], capability)
	}
}

func enablePolicyExtension(ext *registry.Extension, capability CapabilityPolicy) {
	if ext == nil {
		return
	}
	ext.Enabled = true
	ext.AllMode = capability.All
	ext.Args = append([]string(nil), capability.Args...)
	if len(capability.Args) > 0 {
		ext.RawArg = strings.Join(capability.Args, ",")
	}
}

// buildImage runs docker build.
func (a *App) buildImage() error {

	projectRoot := runtimeRoot()
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

	// If external extensions have build scripts, create a temp context that
	// includes them alongside built-in extensions.
	contextDir := projectRoot
	home := homeDir()
	userExtDir := filepath.Join(home, ".mittens", "extensions")
	if tmpCtx, cleanup, err := PrepareExtendedBuildContext(projectRoot, userExtDir, enabledExts); err != nil {
		logWarn("Failed to prepare extended build context: %v", err)
	} else if tmpCtx != "" {
		contextDir = tmpCtx
		defer cleanup()
		dockerfile = filepath.Join(tmpCtx, "container", "Dockerfile")
	}

	providerPlan := a.Provider.RuntimePlan()
	return BuildImage(BuildContext{
		ContextDir:     contextDir,
		Dockerfile:     dockerfile,
		ImageName:      a.ImageName,
		ImageTag:       a.ImageTag,
		UserID:         uid,
		GroupID:        gid,
		Extensions:     enabledExts,
		ExtraBuildArgs: providerPlan.BuildArgs,
		Verbose:        a.Verbose,
		NoCache:        a.Rebuild,
	})
}

// buildInitConfig creates the ContainerConfig struct that will be
// marshaled to JSON and mounted into the container for mittens-init.
// Broker, ExtraDirs, FirewallExtra, and X11 clipboard fields are set
// later in assembleDockerArgs as they depend on further processing.
func (a *App) buildInitConfig() *initcfg.ContainerConfig {
	providerPlan := a.Provider.RuntimePlan()
	return &initcfg.ContainerConfig{
		AI: providerPlan.AI,
		Flags: initcfg.Flags{
			Verbose:       a.Verbose,
			Yolo:          a.Yolo,
			NoNotify:      a.NoNotify,
			Shell:         a.Shell,
			PrintMode:     argExists(a.ClaudeArgs, "--print"),
			NoSSHEgress:   a.NoSSHEgress,
			FirewallLearn: a.FirewallLearn,
		},
		ContainerName:   a.ContainerName,
		InstanceName:    a.InstanceName,
		HostWorkspace:   a.EffectiveWorkspace,
		ImagePasteKey:   a.ImagePasteKey,
		CredStagingDirs: a.credStagingDirs,
	}
}

// assembleDockerArgs builds the full docker run argument list.
// resolverArgs and resolverFirewall come from extension setup resolvers.
func (a *App) assembleDockerArgs(resolverArgs []string, resolverFirewall []string) []string {
	a.ensureHostPolicyDefaults()

	home := os.Getenv("HOME")
	providerPlan := a.Provider.RuntimePlan()
	args := []string{
		"-it",
		"--name", a.ContainerName,
	}
	if providerPlan.ContainerHostname != "" {
		args = append(args, "--hostname", providerPlan.ContainerHostname)
	}
	args = append(args, providerPlan.DockerArgs...)

	// Primary workspace mount (identity: host path = container path).
	args = append(args, "-v", a.WorkspaceMountSrc+":"+a.WorkspaceMountSrc)

	// AI config staging (read-only). Providers that mount the whole config
	// directory for session persistence should not also mount the same host
	// path into the staging location.
	if a.NoHistory || !providerPlan.HistoryMountsWholeConfig {
		args = append(args, "-v", a.Provider.HostConfigDir(home)+":"+a.Provider.StagingConfigDir()+":ro")
	}

	// Environment variables (actual process env, not mittens-init config).
	if a.Provider.APIKeyEnv != "" {
		args = append(args, "-e", a.Provider.APIKeyEnv+"="+os.Getenv(a.Provider.APIKeyEnv))
	}
	if a.Provider.BaseURLEnv != "" && os.Getenv(a.Provider.BaseURLEnv) != "" {
		args = append(args, "-e", a.Provider.BaseURLEnv+"="+os.Getenv(a.Provider.BaseURLEnv))
	}
	// Forward OLLAMA_HOST for --oss mode with remote Ollama servers.
	if os.Getenv("OLLAMA_HOST") != "" {
		args = append(args, "-e", "OLLAMA_HOST="+os.Getenv("OLLAMA_HOST"))
	}
	args = append(args, "-e", "TERM="+envOrDefault("TERM", "xterm-256color"))
	// Forward terminal identity so the agent CLI can detect the real terminal
	// through the PTY chain and enable terminal-specific features (e.g.
	// shift+enter newline handling).
	for _, k := range []string{"TERM_PROGRAM", "TERM_PROGRAM_VERSION"} {
		if v := os.Getenv(k); v != "" {
			args = append(args, "-e", k+"="+v)
		}
	}
	for k, v := range providerPlan.ContainerEnv {
		args = append(args, "-e", k+"="+v)
	}

	// Credential mount.
	if a.Credentials != nil && a.Credentials.TmpFile() != "" && a.Provider.CredentialFile != "" {
		args = append(args, "-v", a.Credentials.TmpFile()+":"+a.Provider.StagingCredentialPath()+":ro")
	}

	// Build mittens-init config and mount as JSON file.
	initCfg := a.buildInitConfig()

	// Credential broker — needs both config fields and docker args for mounts/networking.
	if a.brokerSock != "" {
		sockDir := filepath.Dir(a.brokerSock)
		containerSockDir := "/tmp/mittens-broker"
		containerSockPath := containerSockDir + "/broker.sock"
		args = append(args, "-v", sockDir+":"+containerSockDir)
		initCfg.Broker.Sock = containerSockPath
		initCfg.Broker.Token = a.brokerToken
	} else if a.brokerPort > 0 {
		initCfg.Broker.Port = a.brokerPort
		initCfg.Broker.Token = a.brokerToken
		args = append(args, "--add-host=host.docker.internal:host-gateway")
	}

	// Session persistence mounts.
	if !a.NoHistory && providerPlan.HistoryMountsWholeConfig {
		hostConfigDir := a.Provider.HostConfigDir(home)
		containerConfigDir := a.Provider.ContainerConfigDir()
		ensureDir(hostConfigDir)
		args = append(args, "-v", hostConfigDir+":"+containerConfigDir)
	} else if !a.NoHistory && providerPlan.HistoryMountsProjectDirs && a.HostProjectDir != "" {
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
	}
	if !a.NoHistory {
		hostConfigDir := a.Provider.HostConfigDir(home)
		containerConfigDir := a.Provider.ContainerConfigDir()
		for _, rel := range providerPlan.LiveMountFiles {
			hostPath := filepath.Join(hostConfigDir, rel)
			containerPath := filepath.Join(containerConfigDir, rel)
			ensureFile(hostPath)
			args = append(args, "-v", hostPath+":"+containerPath)
		}
		for _, rel := range providerPlan.LiveMountDirs {
			hostPath := filepath.Join(hostConfigDir, rel)
			containerPath := filepath.Join(containerConfigDir, rel)
			ensureDir(hostPath)
			args = append(args, "-v", hostPath+":"+containerPath)
		}
	}

	// Extra directory mounts.
	if len(a.ExtraDirs) > 0 {
		wtSuffix := a.worktreeSuffix
		var extraPaths []string
		var extraMounts []SummaryMount

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
			access := "rw"
			if spec.ReadOnly {
				access = "ro"
				logInfo("Extra directory (read-only): %s", extraPath)
			} else {
				logInfo("Extra directory: %s", extraPath)
			}
			extraMounts = append(extraMounts, SummaryMount{Path: extraPath, Access: access})
		}
		if len(extraPaths) > 0 {
			initCfg.ExtraDirs = extraPaths
		}
		if len(extraMounts) > 0 {
			a.launchSummary.ExtraDirs = extraMounts
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
		args = append(args, "-v", gitconfig+":"+a.Provider.StagingGitconfigPath()+":ro")
	}

	// Extension mounts, env vars, capabilities, and firewall domains from plans.
	var firewallDomains []string
	for _, plan := range capabilityRuntimePlans(a.Extensions, home, a.Provider.HomePath()) {
		for _, m := range plan.Mounts {
			mountStr := m.Src + ":" + m.Dst
			if m.Mode != "" {
				mountStr += ":" + m.Mode
			}
			args = append(args, "-v", mountStr)
			for k, v := range m.Env {
				args = append(args, "-e", k+"="+v)
			}
		}
		for k, v := range plan.Env {
			args = append(args, "-e", k+"="+v)
		}
		for _, capability := range plan.Capabilities {
			args = append(args, "--cap-add", capability)
		}
		firewallDomains = append(firewallDomains, plan.FirewallDomains...)
	}

	// Add provider, resolver-contributed docker args and firewall domains.
	// Extract MITTENS_* env vars from resolver args into the JSON config.
	firewallDomains = append(firewallDomains, a.FirewallExtra...)
	firewallDomains = append(firewallDomains, providerPlan.FirewallDomains...)
	args = filterMittensEnvArgs(args, resolverArgs, initCfg)
	firewallDomains = append(firewallDomains, resolverFirewall...)

	if len(firewallDomains) > 0 {
		logVerbose(a.Verbose, "Firewall domains: %d extra", len(firewallDomains))
		initCfg.FirewallExtra = firewallDomains
	}

	// Extension prompts: collect from enabled extensions.
	for _, ext := range a.Extensions {
		if !ext.Enabled || ext.Prompt == "" {
			continue
		}
		initCfg.ExtensionPrompts = append(initCfg.ExtensionPrompts, initcfg.ExtensionPrompt{
			Name:  ext.Name,
			Short: ext.Prompt,
			Guide: ext.PromptFile,
		})
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

	// Clipboard image sync.
	if a.HostBridge.ClipboardImages {
		if extraArgs := platformClipboardSync(a); len(extraArgs) > 0 {
			args = filterMittensEnvArgs(args, extraArgs, initCfg)
		}
	}

	// Drop zone for drag-and-drop path translation.
	if a.PathTranslate {
		if dir, err := os.MkdirTemp("", "mittens-drop.*"); err == nil {
			a.dropDir = dir
			a.tempDirs = append(a.tempDirs, dir)
			args = append(args, "-v", dir+":/tmp/mittens-drops:ro")
			logInfo("Drag-and-drop path translation: enabled")
		}
	}

	// Write mittens-init JSON config and mount it into the container.
	cfgFile, err := os.CreateTemp("", "mittens-config.*.json")
	if err == nil {
		cfgPath := cfgFile.Name()
		cfgFile.Close()
		if err := initCfg.Write(cfgPath); err == nil {
			// Ensure world-readable so the container user (uid 1000) can
			// read it after privilege drop. CreateTemp uses 0600 and
			// WriteFile preserves existing permissions on overwrite.
			os.Chmod(cfgPath, 0644)
			a.tempDirs = append(a.tempDirs, cfgPath)
			args = append(args,
				"-v", cfgPath+":"+initcfg.ConfigPath+":ro",
				"-e", "MITTENS_CONFIG="+initcfg.ConfigPath,
			)
		} else {
			logWarn("Failed to write init config: %v", err)
		}
	}

	a.launchSummary = a.buildLaunchSummary(initCfg, firewallDomains)

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
	a.maybeLearnReport()
	if code != 0 {
		if cleanup != nil {
			cleanup()
		}
		os.Exit(code)
	}
	return nil
}

// maybeLearnReport prints the firewall-learn summary after the container exits:
// the out-of-allowlist domains observed during the run, with an offer to add
// them to network.extra_domains. On a TTY it prompts; otherwise it writes a
// JSON artifact and prints the copy-pasteable command to apply later.
func (a *App) maybeLearnReport() {
	if !a.FirewallLearn || a.broker == nil {
		return
	}
	hosts := a.broker.ObservedHosts()
	fmt.Fprintln(os.Stderr)
	if len(hosts) == 0 {
		logInfo("Firewall learn: no out-of-allowlist domains were used this run")
		return
	}
	logInfo("Firewall learn: %d domain(s) used outside the allowlist:", len(hosts))
	for _, h := range hosts {
		fmt.Fprintf(os.Stderr, "    %s\n", h)
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	if interactive {
		add := false
		if err := huh.NewConfirm().
			Title("Add these to network.extra_domains for this project?").
			Affirmative("Add").
			Negative("Skip").
			Value(&add).
			Run(); err == nil && add {
			added, err := addExtraDomains(a.Workspace, a.Extensions, hosts)
			if err != nil {
				logWarn("Firewall learn: failed to update policy: %v", err)
				return
			}
			logInfo("Firewall learn: added %d domain(s) to network.extra_domains", len(added))
			return
		}
		logInfo("Firewall learn: left the allowlist unchanged")
		return
	}

	// Non-interactive: persist an artifact and print the command to apply later.
	if path, err := writeLearnArtifact(a.Workspace, hosts); err == nil {
		logInfo("Firewall learn: wrote observed domains to %s", path)
	}
	fmt.Fprintf(os.Stderr, "  Apply with: mittens policy allow %s\n", strings.Join(hosts, " "))
}

// writeLearnArtifact records the observed domains as JSON under the project dir
// so a non-interactive run's findings survive and can be applied later.
func writeLearnArtifact(workspace string, hosts []string) (string, error) {
	dir := filepath.Join(ConfigHome(), "projects", ProjectDir(workspace))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "firewall-learn.json")
	data, err := json.MarshalIndent(map[string][]string{"domains": hosts}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
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

	// Primary workspace mapping (identity mount: host path = container path).
	if a.WorkspaceMountSrc != "" {
		mapper.mappings = append(mapper.mappings, pathMapping{
			hostPrefix:      a.WorkspaceMountSrc,
			containerPrefix: a.WorkspaceMountSrc,
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

	platformNotify(title, body, focus, log)
}

// openOnHost opens a URL in the host's default browser.
// logFn is an optional logger (broker.blog); pass nil to skip logging.
func openOnHost(url string, logFn func(string, ...interface{})) {
	log := func(format string, args ...interface{}) {
		if logFn != nil {
			logFn(format, args...)
		}
	}

	cmd := platformOpenURL(url)
	log("open: %s %v", cmd.Path, cmd.Args[1:])
	if err := cmd.Start(); err != nil {
		logWarn("Failed to open URL on host: %v", err)
		log("open: start failed: %v", err)
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			logWarn("URL open command exited with error: %v (%s)", err, cmd.Path)
			log("open: %s exited: %v", filepath.Base(cmd.Path), err)
		}
	}()
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

func ensureFile(path string) {
	ensureDir(filepath.Dir(path))
	if fileExists(path) {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	_ = f.Close()
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

// filterMittensEnvArgs appends src args to dst, extracting any -e MITTENS_*
// env vars into the JSON config struct instead of keeping them as docker args.
// Non-MITTENS env vars and all other args pass through unchanged.
func filterMittensEnvArgs(dst, src []string, cfg *initcfg.ContainerConfig) []string {
	for i := 0; i < len(src); i++ {
		if src[i] == "-e" && i+1 < len(src) {
			kv := src[i+1]
			i++
			if !extractMittensEnv(cfg, kv) {
				dst = append(dst, "-e", kv)
			}
		} else {
			dst = append(dst, src[i])
		}
	}
	return dst
}

// extractMittensEnv checks if kv is a "MITTENS_*=value" env var that belongs
// in the JSON config. If so, it sets the corresponding field on cfg and returns
// true. Returns false if kv is not a recognized MITTENS config var.
func extractMittensEnv(cfg *initcfg.ContainerConfig, kv string) bool {
	eq := strings.IndexByte(kv, '=')
	if eq < 0 {
		return false
	}
	key, val := kv[:eq], kv[eq+1:]
	switch key {
	case "MITTENS_DIND":
		cfg.Flags.DinD = strings.EqualFold(val, "true")
	case "MITTENS_DOCKER_HOST":
		cfg.Flags.DockerHost = strings.EqualFold(val, "true")
	case "MITTENS_FIREWALL":
		cfg.Flags.Firewall = strings.EqualFold(val, "true")
	case "MITTENS_MCP":
		cfg.MCP = val
	case "MITTENS_WSL_CLIPBOARD":
		cfg.Flags.WSLClipboard = strings.EqualFold(val, "true")
	case "MITTENS_ENABLE_X11_CLIPBOARD":
		cfg.Flags.EnableX11Clipboard = strings.EqualFold(val, "true")
	case "MITTENS_X11_CLIPBOARD_IMAGE":
		cfg.X11ClipboardImage = val
	case "MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS":
		if v, err := strconv.Atoi(val); err == nil {
			cfg.X11ClipboardMaxAgeSecs = v
		}
	case "MITTENS_IMAGE_PASTE_KEY":
		cfg.ImagePasteKey = val
	default:
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Help text
// ---------------------------------------------------------------------------

func printHelp(exts []*registry.Extension) {
	fmt.Println(`mittens - Run AI coding agents in isolated Docker containers

Usage: mittens [flags] [-- provider-args...]
       mittens <command>

Commands:
  help                          Show this help message
  init                          Interactive project setup wizard
  init --defaults               Edit user-wide defaults (provider, firewall, paste key)
  init --profile NAME           Configure a model profile (model + effort)
  init --profile NAME --delete  Delete a model profile
  logs [-f]                     Show broker logs (-f to follow)
  clean [--dry-run] [--images]  Remove stopped mittens containers
  policy show [--json]          Show effective project policy and boundary
  doctor [--migrate-all]        Check environment and migrate legacy config
  extension list|install|remove Manage external extensions
  version [--json]              Show version information

Core flags:
  --verbose, -v     Show detailed output (Docker build, extension setup)
  --session         Tweak settings for this run only (opens wizard, doesn't save)
  --no-config       Skip config file loading (user defaults + project policy)
  --no-history      Disable session persistence for this run
  --no-build        Skip the Docker image build step
  --rebuild         Rebuild image without layer cache
  --name NAME       Name this instance (default: PID-based)
  --firewall-learn  Run once permissive-but-logging, then offer to add the
                    observed domains to network.extra_domains
  --extensions      List loaded capabilities and policy metadata
  --version, -V     Show version information

Project policy:
  Launch policy is configured with mittens init or focused mittens policy set fields.
  Legacy per-project flag config is converted to policy.yaml automatically.`)

	if len(exts) > 0 {
		fmt.Println("\nCapabilities:")
		for _, ext := range exts {
			desc := ext.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Printf("  %-18s %s\n", ext.Name, desc)
		}
	}
}

func printExtensions(exts []*registry.Extension) {
	if len(exts) == 0 {
		fmt.Println("No capabilities loaded")
		return
	}
	fmt.Println("Loaded capabilities:")
	fmt.Println()
	for _, ext := range exts {
		source := ext.Source
		if source == "" {
			source = "built-in"
		}
		fmt.Printf("  %s [%s]: %s\n", ext.Name, source, ext.Description)
		fmt.Println("    Configure with: mittens init")
		fmt.Printf("    Policy entry: capabilities[].name = %q\n", ext.Name)
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
	Source      string         `json:"source,omitempty"`
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
			{Name: "--no-build", Description: "Skip the Docker image build step"},
			{Name: "--rebuild", Description: "Rebuild image without layer cache"},
			{Name: "--no-history", Description: "Disable session persistence for this run"},
			{Name: "--name", Description: "Name this instance (default: PID-based)", ArgType: "string"},
		},
	}

	for _, ext := range exts {
		je := jsonCapsExtension{
			Name:        ext.Name,
			Description: ext.Description,
			DefaultOn:   ext.DefaultOn,
			Source:      ext.Source,
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
