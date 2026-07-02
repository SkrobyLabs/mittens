//go:build windows

package main

import "os/exec"

// setChildProcessGroup is a no-op on Windows. The mittens host binary only ever
// runs the broker on macOS/Linux; Windows is WSL-shim only.
func setChildProcessGroup(cmd *exec.Cmd) {}

// killChildGroup best-effort kills the child on Windows.
func killChildGroup(child *mcpChild) {
	if child == nil || child.cmd == nil || child.cmd.Process == nil {
		return
	}
	_ = child.cmd.Process.Kill()
}
