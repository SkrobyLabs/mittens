package main

import (
	"fmt"
	"os"
	"strings"
)

// TerminalFocus captures which terminal launched mittens and how to re-focus it.
type TerminalFocus struct {
	Kind string // "iterm2", "wezterm", "kitty", "terminal_app", "x11", "none"
	ID   string // session/pane/window ID
}

// DetectTerminalFocus reads standard env vars to identify the terminal.
func DetectTerminalFocus() TerminalFocus {
	if id := os.Getenv("ITERM_SESSION_ID"); id != "" {
		return TerminalFocus{Kind: "iterm2", ID: id}
	}
	if id := os.Getenv("WEZTERM_PANE"); id != "" {
		return TerminalFocus{Kind: "wezterm", ID: id}
	}
	if id := os.Getenv("KITTY_WINDOW_ID"); id != "" {
		return TerminalFocus{Kind: "kitty", ID: id}
	}
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return TerminalFocus{Kind: "terminal_app"}
	}
	if id := os.Getenv("WINDOWID"); id != "" {
		return TerminalFocus{Kind: "x11", ID: id}
	}
	return TerminalFocus{Kind: "none"}
}

// FocusCommand returns the shell command to focus this terminal.
// Returns nil if no focus mechanism is available.
func (f TerminalFocus) FocusCommand() []string {
	switch f.Kind {
	case "iterm2":
		script := fmt.Sprintf(
			`tell application "iTerm2" to repeat with w in windows
  repeat with s in sessions of w
    if unique ID of s is "%s" then select s
  end repeat
end repeat`, f.ID)
		return []string{"osascript", "-e", script}
	case "wezterm":
		return []string{"wezterm", "cli", "activate-pane", "--pane-id", f.ID}
	case "kitty":
		return []string{"kitty", "@", "focus-window", "--match", "id:" + f.ID}
	case "terminal_app":
		return []string{"osascript", "-e", `tell application "Terminal" to activate`}
	case "x11":
		return []string{"xdotool", "windowactivate", "--sync", f.ID}
	}
	return nil
}

// shellJoin quotes and joins args into a single shell command string.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return strings.Join(quoted, " ")
}
