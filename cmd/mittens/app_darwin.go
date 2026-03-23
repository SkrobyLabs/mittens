//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// shellJoin quotes and joins args into a single shell-safe command string.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func init() {
	platformStartBroker = func(a *App) {
		startBrokerTCP(a)
	}

	platformOpenURL = func(url string) *exec.Cmd {
		return exec.Command("open", url)
	}

	platformNotify = func(title, body string, focus TerminalFocus, log func(string, ...interface{})) {
		var cmd *exec.Cmd
		if path, err := exec.LookPath("terminal-notifier"); err == nil {
			args := []string{"-title", title, "-message", body, "-sound", "Glass"}
			if focus.BundleID != "" {
				args = append(args, "-activate", focus.BundleID)
				log("notify: terminal-notifier -activate %s", focus.BundleID)
			}
			if focusCmd := focus.FocusCommand(); focusCmd != nil {
				executeStr := shellJoin(focusCmd)
				args = append(args, "-execute", executeStr)
				log("notify: terminal-notifier -execute %s", executeStr)
			}
			cmd = exec.Command(path, args...)
		} else {
			log("notify: terminal-notifier not found, using osascript via stdin")
			script := fmt.Sprintf(`display notification %q with title %q sound name "Glass"`, body, title)
			cmd = exec.Command("osascript")
			cmd.Stdin = strings.NewReader(script)
		}
		if err := cmd.Start(); err != nil {
			log("notify: failed to start: %v", err)
		} else {
			log("notify: sent %q", body)
		}
	}

	platformClipboardSync = func(a *App) []string {
		sharedDir, err := ensureSharedClipboardSync()
		if err != nil {
			logWarn("Clipboard image sync: disabled: %v", err)
			return nil
		}
		clientDir, clientErr := os.MkdirTemp("", "mittens-clipboard.*")
		if clientErr != nil {
			logWarn("Clipboard image sync: disabled: %v", clientErr)
			return nil
		}
		regFile, regErr := registerClipboardClient(sharedDir, clientDir)
		if regErr != nil {
			_ = os.RemoveAll(clientDir)
			logWarn("Clipboard image sync: disabled: %v", regErr)
			return nil
		}
		a.clipboardDir = clientDir
		a.clipboardReg = regFile
		var extraArgs []string
		extraArgs = append(extraArgs, "-v", clientDir+":/tmp/mittens-clipboard:ro")

		// Wire up on-demand clipboard reading via the broker so the container's
		// xclip shim (GET /clipboard) returns the latest PNG from the shared dir.
		shared := newClipboardPathsAt(sharedDir)
		if a.broker != nil {
			a.broker.OnClipboardRead = func() []byte {
				data, err := os.ReadFile(shared.imageFile())
				if err != nil {
					return nil
				}
				return data
			}
		}

		logInfo("Clipboard image sync: enabled via %s", sharedDir)

		if a.clipboardDir != "" && a.Provider != nil && a.Provider.Name == "codex" {
			extraArgs = append(extraArgs,
				"-e", "MITTENS_ENABLE_X11_CLIPBOARD=true",
				"-e", "MITTENS_X11_CLIPBOARD_IMAGE=/tmp/mittens-clipboard/clipboard.png",
				"-e", "MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS=5",
				"-e", "DISPLAY=:99",
			)
			logInfo("Codex X11 clipboard bridge: enabled")
		}
		return extraArgs
	}
}
