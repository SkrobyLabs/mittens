package session

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// RequireTmux checks that tmux is installed and returns an error with
// install instructions if it is missing.
func RequireTmux() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		hint := "apt install tmux"
		if runtime.GOOS == "darwin" {
			hint = "brew install tmux"
		}
		return fmt.Errorf("tmux is required but not found in PATH. Install it with: %s", hint)
	}
	return nil
}

// TmuxSessionName returns the canonical tmux session name for a session ID.
func TmuxSessionName(id string) string {
	return "mittens-" + id
}

// TmuxCreate creates a new detached tmux session running the given command.
func TmuxCreate(name string, cols, rows uint16, cmdArgs []string) error {
	// Build the shell command string to run inside tmux.
	shellCmd := shellQuoteArgs(cmdArgs)

	args := []string{
		"new-session", "-d",
		"-s", name,
		"-x", strconv.Itoa(int(cols)),
		"-y", strconv.Itoa(int(rows)),
		"--", "sh", "-c", shellCmd,
	}
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, bytes.TrimSpace(out))
	}

	// Set a large scrollback history.
	setArgs := []string{"set-option", "-t", name, "history-limit", "50000"}
	setCmd := exec.Command("tmux", setArgs...)
	_ = setCmd.Run()

	return nil
}

// TmuxAttach starts a tmux attach-session inside a PTY and returns the handle.
// The caller should start a readLoop on the returned PtyHandle.
func TmuxAttach(name string, rows, cols uint16) (*PtyHandle, error) {
	cmd := exec.Command("tmux", "attach-session", "-t", name)
	return StartPty(cmd, rows, cols)
}

// TmuxHasSession returns true if a tmux session with the given name exists.
func TmuxHasSession(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

// TmuxKillSession kills a tmux session by name.
func TmuxKillSession(name string) error {
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux kill-session: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// TmuxCapturePane captures the full scrollback of a tmux pane with ANSI escapes.
func TmuxCapturePane(name string) ([]byte, error) {
	cmd := exec.Command("tmux", "capture-pane", "-t", name, "-p", "-S", "-", "-e")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tmux capture-pane: %w", err)
	}
	return out, nil
}

// TmuxResizeWindow resizes the tmux window for the given session.
func TmuxResizeWindow(name string, cols, rows uint16) error {
	cmd := exec.Command("tmux", "resize-window",
		"-t", name,
		"-x", strconv.Itoa(int(cols)),
		"-y", strconv.Itoa(int(rows)),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux resize-window: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// shellQuoteArgs single-quotes each argument for safe shell execution.
func shellQuoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		// Replace single quotes with '\'' (end quote, escaped quote, start quote).
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}
