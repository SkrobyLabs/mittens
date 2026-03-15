package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// isWSL returns true if the process is running inside Windows Subsystem for Linux.
func isWSL() bool {
	// Fast path: the Windows shim sets this env var.
	if os.Getenv("MITTENS_WSL_CWD") != "" {
		return true
	}
	// Fallback: check /proc/version for Microsoft/WSL signature.
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// wslClipboardSyncHealthy checks if the Windows clipboard-sync.exe daemon is
// running and healthy by inspecting the heartbeat file.
func wslClipboardSyncHealthy(dir string) bool {
	heartbeatInfo, err := os.Stat(sharedClipboardHeartbeatFile(dir))
	if err != nil {
		pidInfo, pidErr := os.Stat(sharedClipboardPIDFile(dir))
		if pidErr != nil {
			return false
		}
		return time.Since(pidInfo.ModTime()) <= 5*time.Second
	}
	return time.Since(heartbeatInfo.ModTime()) <= 5*time.Second
}

// ensureWSLClipboardSync returns the WSL path to the shared clipboard directory.
// The Windows shim starts clipboard-sync.exe natively and passes the shared
// directory via MITTENS_CLIPBOARD_DIR. This function just validates the daemon
// is healthy.
func ensureWSLClipboardSync() (string, error) {
	sharedDir := os.Getenv("MITTENS_CLIPBOARD_DIR")
	if sharedDir == "" {
		return "", fmt.Errorf("MITTENS_CLIPBOARD_DIR not set (clipboard-sync.exe may not be installed)")
	}

	// Wait briefly for the daemon to become healthy (it was started by the
	// shim moments ago and may still be writing its first heartbeat).
	for i := 0; i < 30; i++ {
		if wslClipboardSyncHealthy(sharedDir) {
			return sharedDir, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return "", fmt.Errorf("clipboard-sync.exe daemon is not healthy at %s", sharedDir)
}
