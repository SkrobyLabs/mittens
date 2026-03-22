package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Colors
var (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"
)

// colorRed wraps s in red ANSI escape codes.
func colorRed(s string) string {
	return "\033[31m" + s + colorReset
}

// logTag is set early in Run() to "[mittens:provider container-name]" so all
// subsequent log lines identify both the provider and the container instance.
var logTag = "mittens"

func logInfo(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, colorCyan+"["+logTag+"]"+colorReset+" "+format+"\n", args...)
}

func logWarn(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, colorYellow+"["+logTag+"] Warning: "+colorReset+format+"\n", args...)
}

func logError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, colorRed("["+logTag+"] Error: ")+format+"\n", args...)
}

func logVerbose(verbose bool, format string, args ...interface{}) {
	if verbose {
		fmt.Fprintf(os.Stderr, colorDim+"["+logTag+"] "+format+colorReset+"\n", args...)
	}
}

const maxBrokerLogSize = 1 << 20 // 1 MB

func rotateBrokerLog(logPath string) {
	fi, err := os.Stat(logPath)
	if err != nil || fi.Size() < maxBrokerLogSize {
		return
	}
	_ = os.Rename(logPath, logPath+".1")
}

// scriptDir returns the directory containing the running binary.
// Used to locate container files (Dockerfile, etc.) relative to the binary.
func scriptDir() string {
	exe, err := os.Executable()
	if err != nil {
		// Fallback to working directory
		wd, _ := os.Getwd()
		return wd
	}
	// Resolve symlinks
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// runtimeRoot returns the directory that contains the container/ and
// extensions/ subdirectories needed at runtime.
// In a dist/install layout these sit next to the binary; in a dev build
// from the repo root they live under cmd/mittens/.
func runtimeRoot() string {
	dir := scriptDir()
	// Dist / install layout: container/ is next to the binary.
	if _, err := os.Stat(filepath.Join(dir, "container", "Dockerfile")); err == nil {
		return dir
	}
	// Dev layout: binary is at repo root, files are in cmd/mittens/.
	dev := filepath.Join(dir, "cmd", "mittens")
	if _, err := os.Stat(filepath.Join(dev, "container", "Dockerfile")); err == nil {
		return dev
	}
	return dir // fall back for error reporting
}

// containerDir returns the path to the container/ subdirectory.
func containerDir() string {
	return filepath.Join(runtimeRoot(), "container")
}

// execCommand runs a command, connecting stdin/stdout/stderr.
func execCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// captureCommand runs a command and returns its stdout.
func captureCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// platformHomeDirFallbackEnv is the environment variable to use as a fallback
// when os.UserHomeDir() fails. Overridden to "USERPROFILE" on Windows.
var platformHomeDirFallbackEnv = "HOME"

// homeDir returns the current user's home directory.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv(platformHomeDirFallbackEnv)
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func sanitizeDockerArgsForLog(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out)-1; i++ {
		if out[i] != "-e" {
			continue
		}
		key, _, ok := strings.Cut(out[i+1], "=")
		if !ok {
			continue
		}
		if isSensitiveEnvKey(key) {
			out[i+1] = key + "=REDACTED"
		}
	}
	return out
}

func isSensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
