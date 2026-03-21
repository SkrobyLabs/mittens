package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// startBrokerDefault is the Linux default broker setup.
// Uses a Unix socket to avoid host firewall issues (UFW/iptables).
// On WSL, falls back to TCP since Docker Desktop provides host.docker.internal.
func startBrokerDefault(a *App) {
	if isWSL() {
		startBrokerTCP(a)
		return
	}
	startBrokerUnix(a)
}

// startBrokerUnix starts the broker on a Unix socket.
func startBrokerUnix(a *App) {
	sockDir, err := os.MkdirTemp("", "mittens-broker.*")
	if err != nil {
		logWarn("Broker: failed to create socket dir: %v", err)
		a.broker = nil
		return
	}
	a.tempDirs = append(a.tempDirs, sockDir)
	sockPath := filepath.Join(sockDir, "broker.sock")
	a.broker.sockPath = sockPath
	go func() {
		if err := a.broker.Serve(); err != nil && err != http.ErrServerClosed {
			logWarn("Broker: %v", err)
		}
	}()
	a.brokerSock = sockPath
	logInfo("Broker: started on unix socket")
}

// startBrokerTCP starts the broker in TCP mode.
// Used by Darwin, Windows, and WSL.
func startBrokerTCP(a *App) {
	port, err := a.broker.ListenTCP()
	if err != nil {
		logWarn("Broker: failed to start: %v", err)
		a.broker = nil
		return
	}
	a.brokerPort = port
	go func() {
		if err := a.broker.Serve(); err != nil && err != http.ErrServerClosed {
			logWarn("Broker: %v", err)
		}
	}()
	logInfo("Broker: started on port %d", port)
}

// openURLDefault is the Linux default URL opener.
// On WSL, tries wslview then powershell.exe; otherwise xdg-open.
func openURLDefault(url string) *exec.Cmd {
	if isWSL() {
		if _, err := exec.LookPath("wslview"); err == nil {
			return exec.Command("wslview", url)
		}
		escaped := strings.ReplaceAll(url, "'", "''")
		return exec.Command("powershell.exe", "-NoProfile", "-Command", "Start-Process '"+escaped+"'")
	}
	return exec.Command("xdg-open", url)
}

// notifyDefault is the Linux default notification sender (notify-send).
func notifyDefault(title, body string, _ TerminalFocus, log func(string, ...interface{})) {
	cmd := exec.Command("notify-send", title, body)
	if err := cmd.Start(); err != nil {
		log("notify: failed to start: %v", err)
	} else {
		log("notify: sent %q", body)
	}
}

// clipboardSyncDefault is the Linux default clipboard sync.
// On WSL, wires up on-demand clipboard reading via the broker.
// On native Linux, clipboard sync is not supported (no-op).
func clipboardSyncDefault(a *App) []string {
	if !isWSL() {
		return nil
	}
	helperPath := os.Getenv("MITTENS_CLIPBOARD_HELPER")
	if helperPath == "" {
		logWarn("Clipboard image sync: disabled: MITTENS_CLIPBOARD_HELPER not set")
		return nil
	}

	// Wire up the broker to read the Windows clipboard on demand.
	if a.broker != nil {
		a.broker.OnClipboardRead = readWSLClipboardImage
	}

	pasteKey := a.ImagePasteKey
	if pasteKey == "" {
		pasteKey = "meta+v"
	}
	if pasteKey == "ctrl+v" {
		logWarn("Image paste: Ctrl+V — Windows Terminal must rebind paste to another key (e.g. Alt+V). Use --image-paste-key meta+v if paste doesn't work.")
	} else {
		logInfo("Image paste: Alt+V (use --image-paste-key ctrl+v to change)")
	}
	envArgs := []string{"-e", "MITTENS_WSL_CLIPBOARD=true"}
	if a.ImagePasteKey != "" {
		envArgs = append(envArgs, "-e", "MITTENS_IMAGE_PASTE_KEY="+a.ImagePasteKey)
	}
	return envArgs
}
