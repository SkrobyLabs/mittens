//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"time"
)

// setChildProcessGroup puts the child in its own process group so the whole
// tree (npx -> node -> workers) can be signalled together.
func setChildProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killChildGroup terminates the child's process group: SIGTERM now, then SIGKILL
// after a 5s grace period. Signalling -pgid kills grandchildren too. Reaping is
// left to child.wait() (the single reaper); a late SIGKILL to an already-reaped
// group is a harmless no-op.
func killChildGroup(child *mcpChild) {
	if child == nil || child.pgid <= 0 {
		return
	}
	pgid := child.pgid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		time.Sleep(5 * time.Second)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
}
