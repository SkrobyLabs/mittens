//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func init() {
	platformStartBroker = func(a *App) {
		startBrokerTCP(a)
	}

	platformCheckNotifications = func() {
		checkWindowsNotifications("powershell")
	}

	platformNotify = func(title, body string, _ TerminalFocus, log func(string, ...interface{})) {
		escTitle := strings.ReplaceAll(title, "'", "''")
		escBody := strings.ReplaceAll(body, "'", "''")
		script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; $n = New-Object System.Windows.Forms.NotifyIcon; $n.Icon = [System.Drawing.SystemIcons]::Information; $n.BalloonTipTitle = '%s'; $n.BalloonTipText = '%s'; $n.Visible = $true; $n.ShowBalloonTip(5000); Start-Sleep -Milliseconds 500; $n.Dispose()`, escTitle, escBody)
		cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
		if err := cmd.Start(); err != nil {
			log("notify: powershell failed: %v", err)
		} else {
			log("notify: sent via powershell %q", body)
		}
	}
}
