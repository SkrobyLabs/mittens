package main

import (
	"fmt"
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
// On WSL, delegates to notifyWSL for Windows toast notifications.
func notifyDefault(title, body string, _ TerminalFocus, log func(string, ...interface{}), _ bool) {
	if isWSL() {
		notifyWSL(title, body, log)
		return
	}
	cmd := exec.Command("notify-send", title, body)
	if err := cmd.Start(); err != nil {
		log("notify: failed to start: %v", err)
	} else {
		log("notify: sent %q", body)
	}
}

// notifyWSL sends a Windows toast notification via PowerShell BalloonTip.
// Uses powershell.exe (with .exe suffix, required for WSL interop).
func notifyWSL(title, body string, log func(string, ...interface{})) {
	escTitle := strings.ReplaceAll(title, "'", "''")
	escBody := strings.ReplaceAll(body, "'", "''")
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; $n = New-Object System.Windows.Forms.NotifyIcon; $n.Icon = [System.Drawing.SystemIcons]::Information; $n.BalloonTipTitle = '%s'; $n.BalloonTipText = '%s'; $n.Visible = $true; $n.ShowBalloonTip(5000); Start-Sleep -Milliseconds 500; $n.Dispose()`, escTitle, escBody)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
	if err := cmd.Start(); err != nil {
		log("notify: wsl powershell failed: %v", err)
	} else {
		log("notify: sent via powershell %q", body)
	}
}

// checkNotificationsDefault checks Windows notification settings on WSL.
// On non-WSL Linux, this is a no-op.
func checkNotificationsDefault() {
	if isWSL() {
		checkWindowsNotifications("powershell.exe")
	}
}

// checkWindowsNotifications queries the Windows registry to warn if desktop
// notifications are globally disabled. Uses powershell.exe on WSL, powershell
// on native Windows. Called at startup when notifications are enabled.
func checkWindowsNotifications(ps string) {
	out, err := exec.Command(ps, "-NoProfile", "-Command",
		`(Get-ItemProperty -Path 'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\PushNotifications' -Name 'ToastEnabled' -ErrorAction SilentlyContinue).ToastEnabled`).Output()
	if err != nil {
		return // can't check, don't warn
	}
	if strings.TrimSpace(string(out)) == "0" {
		logWarn("Windows notifications are disabled — mittens alerts will be silent")
		logWarn("  Enable in: Windows Settings → System → Notifications")
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
