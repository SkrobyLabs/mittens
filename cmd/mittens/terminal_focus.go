package main

import (
	"fmt"
	"os"
	"strings"
)

// TerminalFocus captures which terminal launched mittens and how to re-focus it.
type TerminalFocus struct {
	Kind     string // "iterm2", "wezterm", "kitty", "terminal_app", "x11", "none"
	ID       string // session/pane/window ID
	BundleID string // macOS bundle identifier (e.g. "com.googlecode.iterm2")
}

// DetectTerminalFocus reads standard env vars to identify the terminal.
func DetectTerminalFocus() TerminalFocus {
	if id := os.Getenv("ITERM_SESSION_ID"); id != "" {
		return TerminalFocus{Kind: "iterm2", ID: id, BundleID: "com.googlecode.iterm2"}
	}
	if id := os.Getenv("WEZTERM_PANE"); id != "" {
		return TerminalFocus{Kind: "wezterm", ID: id, BundleID: "com.github.wez.wezterm"}
	}
	if id := os.Getenv("KITTY_WINDOW_ID"); id != "" {
		return TerminalFocus{Kind: "kitty", ID: id, BundleID: "net.kovidgoyal.kitty"}
	}
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return TerminalFocus{Kind: "terminal_app", BundleID: "com.apple.Terminal"}
	}
	if id := os.Getenv("WINDOWID"); id != "" {
		return TerminalFocus{Kind: "x11", ID: id}
	}
	return TerminalFocus{Kind: "none"}
}

// FocusCommand returns the shell command to focus this terminal's specific
// tab/pane/window. Returns nil if no focus mechanism is available.
func (f TerminalFocus) FocusCommand() []string {
	switch f.Kind {
	case "iterm2":
		// ITERM_SESSION_ID format: "w0t2p1:GUID" — extract the GUID.
		guid := f.ID
		if i := strings.Index(guid, ":"); i >= 0 {
			guid = guid[i+1:]
		}
		// Select the specific session by its unique ID.
		script := fmt.Sprintf(`tell application "iTerm2"
set found to false
repeat with w in windows
repeat with t in tabs of w
repeat with s in sessions of t
if unique ID of s is "%s" then
select t
set found to true
exit repeat
end if
end repeat
if found then exit repeat
end repeat
if found then
set index of w to 1
exit repeat
end if
end repeat
end tell`, guid)
		return []string{"osascript", "-e", script}
	case "wezterm":
		return []string{"wezterm", "cli", "activate-pane", "--pane-id", f.ID}
	case "kitty":
		return []string{"kitty", "@", "focus-window", "--match", "id:" + f.ID}
	case "terminal_app":
		return []string{"open", "-a", "Terminal"}
	case "x11":
		return []string{"xdotool", "windowactivate", "--sync", f.ID}
	}
	return nil
}
