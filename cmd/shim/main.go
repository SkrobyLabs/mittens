// Command mittens-shim is a thin Windows wrapper that transparently
// delegates to the real (Linux) mittens binary running inside WSL.
//
// It converts Windows paths in --dir / --dir-ro arguments and the
// working directory, then execs: wsl <linux-binary> <translated-args>
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	colorYellow = "\033[33m"
	colorReset  = "\033[0m"
)

// linuxBinaryPath returns the WSL path to the Linux mittens binary,
// which is expected to sit next to this .exe as "mittens-linux".
func linuxBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	winPath := filepath.Join(dir, "mittens-linux")
	return wslPath(winPath)
}

// wslPath converts a Windows path to a WSL path.
//
// Drive-letter and UNC WSL paths are converted mechanically to avoid
// wslpath resolving junctions/symlinks through Docker Desktop's internal
// bind-mount tree (/mnt/wsl/docker-desktop-bind-mounts/...).
func wslPath(winPath string) (string, error) {
	cleaned := strings.ReplaceAll(winPath, `\`, `/`)

	// Drive letter: C:/foo → /mnt/c/foo
	if len(cleaned) >= 2 && cleaned[1] == ':' &&
		((cleaned[0] >= 'A' && cleaned[0] <= 'Z') || (cleaned[0] >= 'a' && cleaned[0] <= 'z')) {
		drive := strings.ToLower(string(cleaned[0]))
		return "/mnt/" + drive + cleaned[2:], nil
	}

	// UNC WSL path: //wsl$/Ubuntu/home/user → /home/user
	//               //wsl.localhost/Ubuntu/home/user → /home/user
	lower := strings.ToLower(cleaned)
	for _, prefix := range []string{"//wsl$/", "//wsl.localhost/"} {
		if strings.HasPrefix(lower, prefix) {
			// Skip prefix + distro name
			rest := cleaned[len(prefix):]
			if idx := strings.Index(rest, "/"); idx >= 0 {
				return rest[idx:], nil
			}
			return "/", nil
		}
	}

	// Fallback to wslpath for unknown formats.
	out, err := exec.Command("wsl", "wslpath", "-u", cleaned).Output()
	if err != nil {
		return "", fmt.Errorf("wslpath failed for %q: %w", winPath, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// flagsWithPathArg lists flags whose next argument is a Windows path
// that needs translation.
var flagsWithPathArg = map[string]bool{
	"--dir":    true,
	"--dir-ro": true,
}

// relocateSelf renames the running .exe so the original path is unlocked
// for in-place updates while a session is active.  A fresh copy is placed
// back at the original path for new invocations.  The returned function
// cleans up the renamed file and should be deferred.
func relocateSelf() func() {
	exe, err := os.Executable()
	if err != nil {
		return func() {}
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return func() {}
	}

	// Use a per-PID suffix so multiple concurrent sessions don't collide.
	oldPath := fmt.Sprintf("%s.%d", exe, os.Getpid())

	// Clean up stale relocated files from previous runs that didn't get to
	// their deferred cleanup (e.g. crash, forced kill).
	if matches, _ := filepath.Glob(exe + ".[0-9]*"); len(matches) > 0 {
		for _, m := range matches {
			os.Remove(m) // fails silently for files still locked by other sessions
		}
	}
	// Also clean up legacy .old files from prior versions. //legacy-delete-after:2026-04-21
	if matches, _ := filepath.Glob(exe + ".*.old"); len(matches) > 0 {
		for _, m := range matches {
			os.Remove(m)
		}
	}

	if err := os.Rename(exe, oldPath); err != nil {
		return func() {} // can't rename — continue without relocation
	}

	// Copy the renamed file back to the original path so new invocations
	// still work.  The new copy is not locked by this process.
	if err := copyExe(oldPath, exe); err != nil {
		os.Rename(oldPath, exe) // rollback
		return func() {}
	}

	return func() { os.Remove(oldPath) }
}

func copyExe(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// readUserDefaultsPasteKey reads ~/.mittens/defaults via WSL and returns the
// --image-paste-key value, or empty string if not set.
func readUserDefaultsPasteKey() string {
	out, err := exec.Command("wsl", "bash", "-c", "cat ~/.mittens/defaults 2>/dev/null").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "--image-paste-key" {
			return fields[1]
		}
	}
	return ""
}

// mittens-managed action lines injected into WT settings.json.
const mittensMarker = "// __mittens"

var mittensActions = []string{
	`        { "command": "paste", "keys": "alt+v" }, ` + mittensMarker,
	`        { "command": "unbound", "keys": "ctrl+v" }, ` + mittensMarker,
}

// mittensLineRe matches lines tagged with the mittens marker comment.
var mittensLineRe = regexp.MustCompile(`(?i)//\s*__mittens`)

// ensureWTKeybindings modifies Windows Terminal's settings.json to rebind
// paste from Ctrl+V to Alt+V (freeing Ctrl+V for the app), or reverts the
// change when paste key is meta+v.
//
// WT settings.json is JSONC (JSON with comments), so we use line-based
// manipulation instead of json.Unmarshal to preserve comments and formatting.
func ensureWTKeybindings(pasteKey string) {
	settingsPath := findWTSettings()
	if settingsPath == "" {
		return
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Step 1: Remove any existing mittens-managed lines.
	var cleaned []string
	for _, line := range lines {
		if mittensLineRe.MatchString(line) {
			continue
		}
		cleaned = append(cleaned, line)
	}

	// Step 2a: If meta+v (default) mode, warn if paste has been moved off ctrl+v
	// (e.g. leftover from a previous ctrl+v config or manual change).
	if pasteKey != "ctrl+v" && !wtHasCtrlVPaste(content) {
		fmt.Fprintln(os.Stderr, colorYellow+"[mittens] Warning: Windows Terminal paste is not on Ctrl+V."+colorReset)
		fmt.Fprintln(os.Stderr, colorYellow+"[mittens]   Image paste is set to Alt+V, but WT may be using Alt+V for text paste."+colorReset)
		fmt.Fprintln(os.Stderr, colorYellow+"[mittens]   Restore Ctrl+V as paste in Windows Terminal settings, or run: mittens init --defaults"+colorReset)
	}

	// Step 2b: If ctrl+v mode, inject mittens actions into the "actions" array.
	if pasteKey == "ctrl+v" {
		// If ctrl+v is already not bound to paste (mittens-managed, manually
		// rebound, or never bound), there's nothing to do.
		if !wtHasCtrlVPaste(content) {
			return
		}
		injected := injectMittensActions(cleaned)
		if injected != nil {
			cleaned = injected
		} else {
			printWTManualInstructions()
			return
		}
	}

	result := strings.Join(cleaned, "\n")
	if result == content {
		return // no changes needed
	}

	if err := os.WriteFile(settingsPath, []byte(result), 0o644); err != nil {
		printWTManualInstructions()
	}
}

// injectMittensActions finds the "actions" array in settings.json lines and
// inserts the mittens keybinding entries after the opening bracket.
// Returns nil if the actions array cannot be found.
func injectMittensActions(lines []string) []string {
	// Find the "actions": [ line.
	actionsRe := regexp.MustCompile(`^\s*"actions"\s*:\s*\[`)
	for i, line := range lines {
		if actionsRe.MatchString(line) {
			// Insert mittens actions right after the opening bracket.
			var result []string
			result = append(result, lines[:i+1]...)
			result = append(result, mittensActions...)
			result = append(result, lines[i+1:]...)
			return result
		}
	}
	return nil
}

// findWTSettings returns the path to Windows Terminal's settings.json,
// checking the Store install first, then the standalone install.
func findWTSettings() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return ""
	}

	// Store install (most common).
	candidates := []string{
		filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbwe", "LocalState", "settings.json"),
		// Preview/Canary variants.
		filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe", "LocalState", "settings.json"),
		filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminalCanary_8wekyb3d8bbwe", "LocalState", "settings.json"),
		// Standalone (scoop, GitHub release, etc.).
		filepath.Join(localAppData, "Microsoft", "Windows Terminal", "settings.json"),
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// wtHasCtrlVPaste returns true if Windows Terminal still has ctrl+v bound to
// paste (the default). It returns false if ctrl+v has been explicitly unbound,
// rebound to something else, or if paste has been moved to another key.
func wtHasCtrlVPaste(content string) bool {
	// If ctrl+v appears with "unbound", mittens or the user already freed it.
	// If ctrl+v doesn't appear at all in keybindings/actions AND paste is
	// bound to another key (like alt+v), ctrl+v is free by WT default override.
	// The simplest heuristic: if "ctrl+v" doesn't appear anywhere in the
	// settings, check if paste has been rebound to another key.
	hasCtrlV := strings.Contains(strings.ToLower(content), `"ctrl+v"`)
	if hasCtrlV {
		// ctrl+v is explicitly mentioned — check if it's unbound.
		return !strings.Contains(strings.ToLower(content), `"unbound"`)
	}
	// ctrl+v not mentioned. If paste is rebound to another key, ctrl+v
	// reverts to WT default (paste). Check if PasteFromClipboard or paste
	// action is bound to a non-ctrl+v key.
	lower := strings.ToLower(content)
	if strings.Contains(lower, "pastefromclipboard") || strings.Contains(lower, `"paste"`) {
		// Paste is explicitly configured on some other key — ctrl+v is free.
		return false
	}
	// No paste override at all — WT default has ctrl+v as paste.
	return true
}

// printWTManualInstructions prints fallback instructions if settings.json write fails.
func printWTManualInstructions() {
	fmt.Fprintln(os.Stderr, colorYellow+"[mittens] Warning: Could not update Windows Terminal settings automatically."+colorReset)
	fmt.Fprintln(os.Stderr, colorYellow+"[mittens]   To use Ctrl+V for image paste, add these to your WT settings.json actions array:"+colorReset)
	fmt.Fprintln(os.Stderr, colorYellow+`[mittens]   {"command":"paste","keys":"alt+v"}, {"command":"unbound","keys":"ctrl+v"}`+colorReset)
}

func main() {
	// Capture the exe directory BEFORE relocateSelf renames the binary,
	// so we get the original Windows path (not a .old or resolved path).
	shimExe, _ := os.Executable()
	shimDir := filepath.Dir(shimExe)

	cleanup := relocateSelf()
	defer cleanup()

	// Sync Windows Terminal keybindings based on user defaults.
	pasteKey := readUserDefaultsPasteKey()
	ensureWTKeybindings(pasteKey)
	// Clean up old fragment directory from previous versions.
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		oldFragment := filepath.Join(localAppData, "Microsoft", "Windows Terminal", "Fragments", "mittens")
		os.RemoveAll(oldFragment)
	}

	// Translate working directory.
	cwd, err := os.Getwd()
	if err != nil {
		fatal("cannot get working directory: %v", err)
	}
	wslCwd, err := wslPath(cwd)
	if err != nil {
		fatal("cannot translate working directory: %v", err)
	}

	// Locate the Linux binary next to this .exe.
	binPath, err := linuxBinaryPath()
	if err != nil {
		fatal("cannot locate mittens-linux binary: %v", err)
	}

	// Build translated argument list.
	args := os.Args[1:]
	var translated []string
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Translate path argument for known flags.
		if flagsWithPathArg[arg] && i+1 < len(args) {
			translated = append(translated, arg)
			i++
			p, err := wslPath(args[i])
			if err != nil {
				fatal("cannot translate path %q: %v", args[i], err)
			}
			translated = append(translated, p)
			continue
		}

		translated = append(translated, arg)
	}

	// Locate the clipboard helper next to the shim so the WSL-side broker can
	// invoke it on demand (no background daemon — reads clipboard only when pasted).
	clipboardHelper := locateClipboardHelper(shimDir)

	// Build: wsl --cd <wsl-cwd> env MITTENS_WSL_CWD=<path>
	//   MITTENS_CLIPBOARD_HELPER=<path> <linux-binary> <args...>
	// The env prefix is needed because WSL does not reliably forward
	// environment variables set on the Windows side.
	envVars := []string{
		"MITTENS_WSL_CWD=" + wslCwd,
	}
	if clipboardHelper != "" {
		// Convert Windows path to WSL path so exec.Command works in Linux.
		wslHelper, err := wslPath(clipboardHelper)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mittens] cannot translate clipboard helper path: %v\n", err)
		} else {
			envVars = append(envVars, "MITTENS_CLIPBOARD_HELPER="+wslHelper)
		}
	}
	wslArgs := []string{"--cd", wslCwd, "env"}
	wslArgs = append(wslArgs, envVars...)
	wslArgs = append(wslArgs, binPath)
	wslArgs = append(wslArgs, translated...)

	cmd := exec.Command("wsl", wslArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fatal("%v", err)
	}
}

// locateClipboardHelper returns the Windows path to mittens-clipboard-helper.exe
// if it exists next to the shim, or empty string otherwise.
func locateClipboardHelper(shimDir string) string {
	exePath := filepath.Join(shimDir, "mittens-clipboard-helper.exe")
	if _, err := os.Stat(exePath); err != nil {
		fmt.Fprintf(os.Stderr, "[mittens] mittens-clipboard-helper.exe not found at %s\n", exePath)
		return ""
	}
	return exePath
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[mittens] "+format+"\n", a...)
	os.Exit(1)
}
