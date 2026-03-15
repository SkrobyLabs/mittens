package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
// On WSL, sets up the shared clipboard directory from the Windows helper.
// On native Linux, clipboard sync is not supported (no-op).
func clipboardSyncDefault(a *App) []string {
	if !isWSL() {
		return nil
	}
	sharedDir, err := ensureWSLClipboardSync()
	if err != nil {
		logWarn("Clipboard image sync: disabled: %v", err)
		return nil
	}
	extraArgs := []string{
		"-v", sharedDir + ":/tmp/mittens-clipboard:ro",
		"-e", "DISPLAY=:0",
		"-e", "MITTENS_WSL_CLIPBOARD=true",
	}
	hbFile := filepath.Join(sharedDir, "clients", fmt.Sprintf("%d.heartbeat", os.Getpid()))
	a.clipboardClientHB = hbFile
	writeClipboardClientHeartbeat(hbFile)
	go func() {
		for {
			time.Sleep(30 * time.Second)
			if a.clipboardClientHB == "" {
				return
			}
			writeClipboardClientHeartbeat(hbFile)
		}
	}()
	logInfo("Clipboard image sync: enabled via %s (WSL)", sharedDir)
	return extraArgs
}
