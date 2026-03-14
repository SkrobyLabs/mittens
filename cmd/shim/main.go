// Command mittens-shim is a thin Windows wrapper that transparently
// delegates to the real (Linux) mittens binary running inside WSL.
//
// It converts Windows paths in --dir / --dir-ro arguments and the
// working directory, then execs: wsl <linux-binary> <translated-args>
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// wslPath converts a Windows path to a WSL path using wslpath.
func wslPath(winPath string) (string, error) {
	// Convert backslashes to forward slashes so wslpath doesn't choke.
	cleaned := strings.ReplaceAll(winPath, `\`, `/`)
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

func main() {
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

	// Build: wsl --cd <wsl-cwd> <linux-binary> <args...>
	wslArgs := []string{"--cd", wslCwd, binPath}
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

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[mittens] "+format+"\n", a...)
	os.Exit(1)
}
