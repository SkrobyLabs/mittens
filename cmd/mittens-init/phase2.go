package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

// runPhase2 performs user-level setup: config staging, trust dirs, hooks,
// credential sync, then execs the AI CLI.
func runPhase2(cfg *config) error {
	os.MkdirAll(cfg.AIDir, 0755)

	// Source extension environments (Go, .NET, etc.).
	sourceProfileD()

	// Ensure ~/.local/bin/<binary> exists.
	setupBinaryLink(cfg)

	// X11 clipboard bridge for Codex image paste.
	if cfg.EnableX11Clipboard {
		startX11Clipboard(cfg)
	}

	// WSL clipboard keybinding.
	if cfg.WSLClipboard {
		setupWSLClipboard(cfg)
	}

	// Copy read-only credential staging mounts into writable home.
	copyCredStagingDirs(cfg)

	// Copy read-only config into writable home.
	copyConfigFiles(cfg)

	// Pre-trust directories.
	setupTrustedDirs(cfg)

	// Write trusted dirs file (for providers that use a separate file).
	setupTrustedDirsFile(cfg)

	// Auto-accept yolo permission prompt.
	if cfg.Yolo && cfg.AIYoloKey != "" && cfg.AISettingsFormat == "json" {
		setJSONKey(cfg.settingsFilePath(), cfg.AIYoloKey, true)
	}

	// Provider-specific settings init.
	if cfg.AIInitSettingsJQ != "" && cfg.AISettingsFormat == "json" {
		applyInitSettings(cfg)
	}

	// Copy git config.
	copyGitConfig(cfg)

	// Copy user preferences.
	copyUserPrefs(cfg)

	// Write OAuth credentials.
	setupCredentials(cfg)

	// Inform AI about extra directories.
	appendExtraDirsInfo(cfg)

	// Inform AI about firewall.
	appendFirewallInfo(cfg)

	// Inject notification hooks.
	setupNotificationHooks(cfg)

	// Start credential sync daemon as a forked child process.
	// Must be a separate process because syscall.Exec (below) kills goroutines.
	if cfg.hasBroker() {
		if err := forkCredSync(); err != nil {
			logWarn("credential sync: %v", err)
		}
	}

	// cd to host workspace path (always the real path with identity mount).
	if cfg.HostWorkspace != "" {
		os.Chdir(cfg.HostWorkspace)
	}

	// Worker containers run the worker agent instead of the AI CLI.
	if os.Getenv("MITTENS_WORKER_ID") != "" {
		runWorkerAgent(cfg)
		return nil
	}

	// Exec the remaining args (typically the AI CLI binary).
	return execArgs(cfg.AIBinary)
}

func sourceProfileD() {
	entries, err := filepath.Glob("/etc/profile.d/*.sh")
	if err != nil || len(entries) == 0 {
		return
	}
	// Source all profile.d scripts in a single shell and capture the resulting env.
	var scripts []string
	for _, f := range entries {
		scripts = append(scripts, fmt.Sprintf(". %s", f))
	}
	cmd := exec.Command("bash", "-c", strings.Join(scripts, "; ")+"; env")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.IndexByte(line, '='); idx > 0 {
			os.Setenv(line[:idx], line[idx+1:])
		}
	}
}

func setupBinaryLink(cfg *config) {
	localBin := cfg.AIHome + "/.local/bin"
	os.MkdirAll(localBin, 0755)
	linkPath := localBin + "/" + cfg.AIBinary
	if _, err := os.Stat(linkPath); err != nil {
		if binPath, err := exec.LookPath(cfg.AIBinary); err == nil {
			os.Symlink(binPath, linkPath)
		}
	}
	os.Setenv("PATH", localBin+":"+os.Getenv("PATH"))
}

func startX11Clipboard(cfg *config) {
	display := envOr("DISPLAY", ":99")
	os.Setenv("DISPLAY", display)

	clipImage := cfg.X11ClipboardImage
	if clipImage == "" {
		clipImage = "/tmp/mittens-clipboard/clipboard.png"
	}

	// Start Xvfb.
	xvfb := exec.Command("Xvfb", display, "-screen", "0", "1024x768x24", "-nolisten", "tcp")
	xvfbLog, _ := os.Create("/tmp/mittens-xvfb.log")
	if xvfbLog != nil {
		xvfb.Stdout = xvfbLog
		xvfb.Stderr = xvfbLog
	}
	_ = xvfb.Start()

	// Start X11 clipboard sync as a background process.
	// (Uses the busybox-style dispatch in mittens-init itself.)
	sync := exec.Command(os.Args[0], clipImage)
	syncLog, _ := os.Create("/tmp/mittens-x11-clipboard.log")
	if syncLog != nil {
		sync.Stdout = syncLog
		sync.Stderr = syncLog
	}
	// Create a symlink so the busybox-style argv[0] dispatch works.
	syncLink := "/tmp/clipboard-x11-sync.sh"
	os.Remove(syncLink)
	os.Symlink("/usr/local/bin/mittens-init", syncLink)
	sync = exec.Command(syncLink, clipImage)
	if syncLog != nil {
		sync.Stdout = syncLog
		sync.Stderr = syncLog
	}
	_ = sync.Start()
}

func setupWSLClipboard(cfg *config) {
	kbFile := cfg.AIDir + "/keybindings.json"
	if _, err := os.Stat(kbFile); err != nil {
		content := fmt.Sprintf(`{"bindings":[{"context":"Chat","bindings":{"%s":"chat:imagePaste"}}]}`, cfg.ImagePasteKey)
		os.WriteFile(kbFile, []byte(content), 0644)
	}
}

// copyCredStagingDirs copies read-only credential staging mounts into writable
// home directories. Each entry is "staging_path:target_dir" where target_dir is
// relative to the user's home (e.g. ".azure", ".aws").
func copyCredStagingDirs(cfg *config) {
	for _, entry := range cfg.CredStagingDirs {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			continue
		}
		staging, targetDir := parts[0], parts[1]
		if info, err := os.Stat(staging); err != nil || !info.IsDir() {
			continue
		}
		dst := cfg.AIHome + "/" + targetDir
		os.MkdirAll(dst, 0755)
		if err := fileutil.CopyDir(staging, dst); err != nil {
			logWarn("credential staging copy %s -> %s: %v", staging, dst, err)
		}
	}
}

func copyConfigFiles(cfg *config) {
	staging := cfg.stagingConfigDir()
	if info, err := os.Stat(staging); err != nil || !info.IsDir() {
		return
	}
	// Check if staging and target are the same (bind-mounted directly).
	if staging == cfg.AIDir {
		return
	}

	// Config subdirectories.
	for _, item := range cfg.AIConfigSubdirs {
		srcDir := staging + "/" + item
		if info, err := os.Stat(srcDir); err == nil && info.IsDir() {
			dstDir := cfg.AIDir + "/" + item
			os.MkdirAll(dstDir, 0755)
			fileutil.CopyDir(srcDir, dstDir)
		}
	}

	// Plugin config files.
	if cfg.AIPluginDir != "" {
		srcPluginDir := staging + "/" + cfg.AIPluginDir
		if info, err := os.Stat(srcPluginDir); err == nil && info.IsDir() {
			dstPluginDir := cfg.AIDir + "/" + cfg.AIPluginDir
			os.MkdirAll(dstPluginDir, 0755)
			for _, file := range cfg.AIPluginFiles {
				copyIfExists(srcPluginDir+"/"+file, dstPluginDir+"/"+file)
			}
			// Marketplaces directory.
			mktDir := srcPluginDir + "/marketplaces"
			if info, err := os.Stat(mktDir); err == nil && info.IsDir() {
				dstMktDir := dstPluginDir + "/marketplaces"
				os.MkdirAll(dstMktDir, 0755)
				fileutil.CopyDir(mktDir, dstMktDir)
			}
		}
	}

	// Config files.
	for _, file := range []string{cfg.AISettingsFile, "settings.local.json", cfg.AIProjectFile, "statusline.sh"} {
		copyIfExists(staging+"/"+file, cfg.AIDir+"/"+file)
	}
	if cfg.AISettingsFormat == "json" {
		normalizeEmptyJSONObjectFile(cfg.AIDir + "/" + cfg.AISettingsFile)
	}

	// Make statusline executable if copied.
	if _, err := os.Stat(cfg.AIDir + "/statusline.sh"); err == nil {
		os.Chmod(cfg.AIDir+"/statusline.sh", 0755)
	}

	// Persist files.
	for _, file := range cfg.AIPersistFiles {
		copyIfExists(staging+"/"+file, cfg.AIDir+"/"+file)
	}
	for _, dir := range cfg.AIPersistDirs {
		copyDirIfExists(staging+"/"+dir, cfg.AIDir+"/"+dir)
	}
	for _, pattern := range cfg.AIPersistGlobs {
		copyGlobIfExists(staging, cfg.AIDir, pattern)
	}
}

func setupTrustedDirs(cfg *config) {
	if cfg.AITrustedDirsKey == "" || cfg.AISettingsFormat != "json" {
		return
	}

	dirs := []string{cfg.HostWorkspace}
	dirs = append(dirs, cfg.ExtraDirs...)

	settingsFile := cfg.settingsFilePath()
	settings := readJSONFile(settingsFile)

	// Get existing trusted dirs.
	var existing []string
	if raw, ok := settings[cfg.AITrustedDirsKey]; ok {
		var arr []string
		if b, err := json.Marshal(raw); err == nil {
			json.Unmarshal(b, &arr)
		}
		existing = arr
	}

	// Merge and deduplicate.
	merged := append(existing, dirs...)
	settings[cfg.AITrustedDirsKey] = dedup(merged)

	writeJSONFile(settingsFile, settings)
}

func setupTrustedDirsFile(cfg *config) {
	if cfg.AITrustedDirsFile == "" {
		return
	}

	trust := map[string]string{}
	if cfg.HostWorkspace != "" {
		trust[cfg.HostWorkspace] = "TRUST_FOLDER"
	}
	for _, d := range cfg.ExtraDirs {
		if d != "" {
			trust[d] = "TRUST_FOLDER"
		}
	}

	data, _ := json.MarshalIndent(trust, "", "  ")
	os.WriteFile(cfg.AIDir+"/"+cfg.AITrustedDirsFile, data, 0644)
}

func applyInitSettings(cfg *config) {
	// The InitSettingsJQ contains jq-style expressions like:
	//   .general.enableAutoUpdate = false | .general.enableAutoUpdateNotification = false
	// We need to apply these in Go. Parse the simple "path = value" assignments.
	settingsFile := cfg.settingsFilePath()
	ensureJSONFile(settingsFile)

	settings := readJSONFile(settingsFile)
	for _, expr := range strings.Split(cfg.AIInitSettingsJQ, "|") {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		applyJQAssignment(settings, expr)
	}
	writeJSONFile(settingsFile, settings)
}

func copyGitConfig(cfg *config) {
	copyIfExists(cfg.ConfigMount+"/.gitconfig", cfg.AIHome+"/.gitconfig")
	exec.Command("git", "config", "--global", "--add", "safe.directory", "*").Run()
}

func copyUserPrefs(cfg *config) {
	if cfg.AIPrefsFile != "" {
		dst := cfg.AIHome + "/" + cfg.AIPrefsFile
		copyIfExists(cfg.ConfigMount+"/"+cfg.AIPrefsFile, dst)
		normalizeEmptyJSONObjectFile(dst)
	}
}

func setupCredentials(cfg *config) {
	credSrc := cfg.ConfigMount + "/" + cfg.AICredFile
	credDst := cfg.AIDir + "/" + cfg.AICredFile
	if _, err := os.Stat(credSrc); err == nil {
		fileutil.CopyFile(credSrc, credDst)
		os.Chmod(credDst, 0600)
	}
}

func appendExtraDirsInfo(cfg *config) {
	if len(cfg.ExtraDirs) == 0 {
		return
	}
	projectFile := cfg.AIDir + "/" + cfg.AIProjectFile
	f, err := os.OpenFile(projectFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	fmt.Fprintln(f)
	fmt.Fprintln(f, "# Additional Workspace Directories")
	fmt.Fprintln(f, "These directories are mounted read-write and trusted. You can read, edit, and search files in them.")
	for _, d := range cfg.ExtraDirs {
		if d != "" {
			fmt.Fprintf(f, "- %s\n", d)
		}
	}
}

func appendFirewallInfo(cfg *config) {
	if !cfg.Firewall {
		return
	}
	// Read the whitelist that was written during phase 1.
	// Re-parse firewall.conf to include the domain list in the AI project file.
	domains, err := parseWhitelistFile(cfg.FirewallConf)
	if err != nil || len(domains) == 0 {
		return
	}

	// Add extra domains.
	domains = append(domains, cfg.FirewallExtra...)
	domains = dedup(domains)

	projectFile := cfg.AIDir + "/" + cfg.AIProjectFile
	f, err := os.OpenFile(projectFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	fmt.Fprintln(f)
	fmt.Fprintln(f, "# Network Firewall")
	fmt.Fprintln(f, "This container runs behind an outbound network firewall (proxy + iptables).")
	fmt.Fprintln(f, "Only the domains listed below are reachable over HTTP/HTTPS.")
	fmt.Fprintln(f, "Requests to any other FQDN will **time out or be refused** by the proxy — do not retry, the domain is blocked by policy.")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "If a tool or package manager fails with a network error, check whether the target domain is in this list before troubleshooting further.")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Whitelisted domains")
	fmt.Fprintln(f, "```")
	for _, d := range domains {
		fmt.Fprintln(f, d)
	}
	fmt.Fprintln(f, "```")
}

func setupNotificationHooks(cfg *config) {
	if cfg.AISettingsFormat != "json" {
		return
	}

	settingsFile := cfg.settingsFilePath()
	ensureJSONFile(settingsFile)

	settings := readJSONFile(settingsFile)
	stripNotificationHooks(settings)

	if !cfg.hasBroker() || cfg.NoNotify {
		writeJSONFile(settingsFile, settings)
		return
	}

	notifyCmd := `MSG=$(jq -r '.message // "needs attention"'); /usr/local/bin/notify.sh notification "$MSG"`

	// Merge notification hooks into existing hooks (preserve user/plugin hooks).
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	hooks["Notification"] = []interface{}{
		map[string]interface{}{
			"hooks": []interface{}{
				map[string]interface{}{
					"type":    "command",
					"command": notifyCmd,
				},
			},
		},
	}

	if cfg.AIStopHookEvent != "" {
		hooks[cfg.AIStopHookEvent] = []interface{}{
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"type":    "command",
						"command": "/usr/local/bin/notify.sh stop",
					},
				},
			},
		}
	}

	settings["hooks"] = hooks
	writeJSONFile(settingsFile, settings)
}

func stripNotificationHooks(settings map[string]interface{}) {
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return
	}
	for event, rawEntries := range hooks {
		entries, ok := rawEntries.([]interface{})
		if !ok {
			continue
		}
		var keptEntries []interface{}
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]interface{})
			if !ok {
				keptEntries = append(keptEntries, rawEntry)
				continue
			}
			rawHookList, ok := entry["hooks"].([]interface{})
			if !ok {
				keptEntries = append(keptEntries, rawEntry)
				continue
			}
			var keptHookList []interface{}
			for _, rawHook := range rawHookList {
				hook, ok := rawHook.(map[string]interface{})
				if !ok {
					keptHookList = append(keptHookList, rawHook)
					continue
				}
				command, _ := hook["command"].(string)
				if strings.Contains(strings.TrimSpace(command), "/usr/local/bin/notify.sh") {
					continue
				}
				keptHookList = append(keptHookList, rawHook)
			}
			if len(keptHookList) == 0 {
				continue
			}
			entry["hooks"] = keptHookList
			keptEntries = append(keptEntries, entry)
		}
		if len(keptEntries) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = keptEntries
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
		return
	}
	settings["hooks"] = hooks
}

func execArgs(defaultBinary string) error {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{defaultBinary}
	}

	binary, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("command not found: %s", args[0])
	}

	return syscall.Exec(binary, args, os.Environ())
}

// --- File helpers ---

func copyIfExists(src, dst string) {
	if _, err := os.Stat(src); err == nil {
		fileutil.CopyFile(src, dst)
	}
}

func copyDirIfExists(src, dst string) {
	if info, err := os.Stat(src); err == nil && info.IsDir() {
		_ = os.RemoveAll(dst)
		if err := fileutil.CopyDir(src, dst); err != nil {
			logWarn("copy dir %s -> %s: %v", src, dst, err)
		}
	}
}

func copyGlobIfExists(srcRoot, dstRoot, pattern string) {
	matches, err := filepath.Glob(filepath.Join(srcRoot, pattern))
	if err != nil {
		logWarn("copy glob %s: %v", pattern, err)
		return
	}
	for _, src := range matches {
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		rel, err := filepath.Rel(srcRoot, src)
		if err != nil {
			continue
		}
		dst := filepath.Join(dstRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := fileutil.CopyFile(src, dst); err != nil {
			logWarn("copy file %s -> %s: %v", src, dst, err)
		}
	}
}

// --- JSON helpers (replace jq) ---

func readJSONFile(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]interface{})
	}
	var obj map[string]interface{}
	if json.Unmarshal(data, &obj) != nil {
		return make(map[string]interface{})
	}
	return obj
}

func writeJSONFile(path string, obj map[string]interface{}) {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, append(data, '\n'), 0644)
}

func ensureJSONFile(path string) {
	if _, err := os.Stat(path); err != nil {
		os.WriteFile(path, []byte("{}\n"), 0644)
	}
}

func normalizeEmptyJSONObjectFile(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) != "" {
		return
	}
	_ = os.WriteFile(path, []byte("{}\n"), 0644)
}

// setJSONKey sets a top-level key in a JSON settings file.
func setJSONKey(path, key string, value interface{}) {
	settings := readJSONFile(path)
	settings[key] = value
	writeJSONFile(path, settings)
}

// applyJQAssignment applies a simple ".path.to.key = value" jq expression.
func applyJQAssignment(obj map[string]interface{}, expr string) {
	parts := strings.SplitN(expr, "=", 2)
	if len(parts) != 2 {
		return
	}
	path := strings.TrimSpace(parts[0])
	valStr := strings.TrimSpace(parts[1])

	// Parse the value.
	var value interface{}
	if err := json.Unmarshal([]byte(valStr), &value); err != nil {
		value = valStr // treat as string
	}

	// Parse the path (e.g. ".general.enableAutoUpdate").
	path = strings.TrimPrefix(path, ".")
	keys := strings.Split(path, ".")
	if len(keys) == 0 {
		return
	}

	// Navigate to the parent, creating intermediate objects as needed.
	current := obj
	for _, key := range keys[:len(keys)-1] {
		child, ok := current[key]
		if !ok {
			child = make(map[string]interface{})
			current[key] = child
		}
		if childMap, ok := child.(map[string]interface{}); ok {
			current = childMap
		} else {
			// Can't navigate further; the intermediate isn't an object.
			return
		}
	}
	current[keys[len(keys)-1]] = value
}
