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
	"strconv"
	"strings"
	"time"
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
	oldPath := fmt.Sprintf("%s.%d.old", exe, os.Getpid())

	// Clean up stale .old files from previous runs that didn't get to
	// their deferred cleanup (e.g. crash, forced kill).
	if matches, _ := filepath.Glob(exe + ".*.old"); len(matches) > 0 {
		for _, m := range matches {
			os.Remove(m) // fails silently for files still locked by other sessions
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

func main() {
	// Capture the exe directory BEFORE relocateSelf renames the binary,
	// so we get the original Windows path (not a .old or resolved path).
	shimExe, _ := os.Executable()
	shimDir := filepath.Dir(shimExe)

	cleanup := relocateSelf()
	defer cleanup()

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

	// Start mittens-clipboard-helper.exe on the Windows side (can't run .exe
	// from WSL when Docker Desktop's bind-mounts break binfmt_misc PE handling).
	clipboardSharedDir := ensureClipboardSync(shimDir)

	// Build: wsl --cd <wsl-cwd> env MITTENS_WSL_CWD=<path>
	//   MITTENS_CLIPBOARD_DIR=<path> <linux-binary> <args...>
	// The env prefix is needed because WSL does not reliably forward
	// environment variables set on the Windows side.
	envVars := []string{
		"MITTENS_WSL_CWD=" + wslCwd,
	}
	if clipboardSharedDir != "" {
		envVars = append(envVars, "MITTENS_CLIPBOARD_DIR="+clipboardSharedDir)
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

// ensureClipboardSync starts mittens-clipboard-helper.exe if it exists next to
// the shim and no healthy daemon is already running. Returns the WSL-side shared
// directory path or empty string on failure.
func ensureClipboardSync(shimDir string) string {
	exePath := filepath.Join(shimDir, "mittens-clipboard-helper.exe")
	if _, err := os.Stat(exePath); err != nil {
		fmt.Fprintf(os.Stderr, "[mittens] mittens-clipboard-helper.exe not found at %s\n", exePath)
		return ""
	}

	// Use a UNC path into the WSL filesystem so that:
	// 1. The Windows exe can write to it (via \\wsl.localhost\<distro>\...)
	// 2. The WSL mittens binary can read it (at /tmp/...)
	// 3. Docker Desktop can mount it into a container
	// Using Windows %TEMP% doesn't work because Docker Desktop's WSL
	// integration only exposes specific /mnt/c/ paths, not the full drive.
	distro := wslDistroName()
	if distro == "" {
		fmt.Fprintf(os.Stderr, "[mittens] cannot determine WSL distro name for clipboard sync\n")
		return ""
	}
	sharedDir := `\\wsl.localhost\` + distro + `\tmp\mittens-clipboard-shared`
	if err := os.MkdirAll(filepath.Join(sharedDir, "clients"), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "[mittens] cannot create clipboard dir: %v\n", err)
		return ""
	}

	// The WSL-side path for the env var (the mittens binary reads from here).
	wslSharedDir := "/tmp/mittens-clipboard-shared"

	// Check if already healthy (heartbeat file fresh).
	if clipboardSyncHealthy(sharedDir) {
		return wslSharedDir
	}

	// Start the daemon.
	logPath := filepath.Join(sharedDir, "clipboard-sync.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mittens] cannot open clipboard log: %v\n", err)
		return ""
	}

	cmd := exec.Command(exePath, sharedDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "[mittens] cannot start mittens-clipboard-helper.exe: %v\n", err)
		logFile.Close()
		return ""
	}
	logFile.Close()

	// Wait for healthy.
	for i := 0; i < 50; i++ {
		if clipboardSyncHealthy(sharedDir) {
			return wslSharedDir
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Read log for diagnostics.
	if logData, err := os.ReadFile(logPath); err == nil && len(logData) > 0 {
		tail := string(logData)
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		fmt.Fprintf(os.Stderr, "[mittens] mittens-clipboard-helper.exe not healthy (log: %s)\n", strings.TrimSpace(tail))
	} else {
		fmt.Fprintf(os.Stderr, "[mittens] mittens-clipboard-helper.exe not healthy (no log output)\n")
	}
	return ""
}

// clipboardSyncHealthy checks if the clipboard-sync daemon is running
// by verifying the heartbeat file is fresh.
func clipboardSyncHealthy(dir string) bool {
	hbPath := filepath.Join(dir, "clipboard.heartbeat")
	info, err := os.Stat(hbPath)
	if err != nil {
		// No heartbeat — check PID file age.
		pidPath := filepath.Join(dir, "clipboard-sync.pid")
		pidInfo, pidErr := os.Stat(pidPath)
		if pidErr != nil {
			return false
		}
		pidData, _ := os.ReadFile(pidPath)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil && pid > 0 {
			proc, err := os.FindProcess(pid)
			if err != nil {
				return false
			}
			_ = proc
		}
		return time.Since(pidInfo.ModTime()) <= 5*time.Second
	}
	return time.Since(info.ModTime()) <= 5*time.Second
}

// wslDistroName returns the name of the default WSL distribution.
func wslDistroName() string {
	out, err := exec.Command("wsl", "-e", "bash", "-c", "echo $WSL_DISTRO_NAME").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[mittens] "+format+"\n", a...)
	os.Exit(1)
}
