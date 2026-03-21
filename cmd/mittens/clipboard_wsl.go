package main

import (
	"os"
	"os/exec"
	"strings"
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

// readWSLClipboardImage runs the Windows clipboard helper exe and returns PNG
// bytes, or nil if no image is available. The helper path is passed via the
// MITTENS_CLIPBOARD_HELPER env var set by the Windows shim.
func readWSLClipboardImage() []byte {
	helperPath := os.Getenv("MITTENS_CLIPBOARD_HELPER")
	if helperPath == "" {
		return nil
	}

	cmd := exec.Command(helperPath)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return out
}
