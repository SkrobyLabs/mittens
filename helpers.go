package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Colors
var (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
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

// containerDir returns the path to the container/ subdirectory
// relative to the binary location.
func containerDir() string {
	return filepath.Join(scriptDir(), "container")
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

// homeDir returns the current user's home directory.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	if runtime.GOOS == "windows" {
		return os.Getenv("USERPROFILE")
	}
	return os.Getenv("HOME")
}
