package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const runtimeLockFileName = "team-mcp.runtime.lock"

type runtimeLockOwner struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	AcquiredAt string `json:"acquiredAt"`
}

type runtimeLock struct {
	file  *os.File
	owner runtimeLockOwner
}

func logActiveRuntimeOwner(stderr io.Writer, owner runtimeLockOwner) {
	if stderr != nil {
		fmt.Fprintf(stderr, "startup: active runtime owner %s\n", formatRuntimeLockOwner(owner))
	}
}

func acquireRuntimeLock(stateDir, sessionID string, stderr io.Writer) (*runtimeLock, error) {
	lockPath := filepath.Join(stateDir, runtimeLockFileName)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if stderr != nil {
			fmt.Fprintf(stderr, "startup: session runtime lock open failed at %s: %v\n", lockPath, err)
		}
		return nil, fmt.Errorf("open session runtime lock: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		owner, ownerErr := readRuntimeLockOwner(lockPath)
		_ = f.Close()
		if ownerErr == nil {
			logActiveRuntimeOwner(stderr, owner)
			return nil, fmt.Errorf("session runtime lock busy: %s", formatRuntimeLockOwner(owner))
		}
		if stderr != nil {
			fmt.Fprintf(stderr, "startup: session runtime lock busy at %s (owner unknown: %v)\n", lockPath, ownerErr)
		}
		return nil, fmt.Errorf("session runtime lock busy: owner unknown: %w", ownerErr)
	}

	lock := &runtimeLock{
		file: f,
		owner: runtimeLockOwner{
			PID:        os.Getpid(),
			SessionID:  sessionID,
			AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	if err := lock.writeOwner(); err != nil {
		_ = lock.Close()
		if stderr != nil {
			fmt.Fprintf(stderr, "startup: session runtime lock write failed at %s: %v\n", lockPath, err)
		}
		return nil, fmt.Errorf("write session runtime lock owner: %w", err)
	}

	logActiveRuntimeOwner(stderr, lock.owner)
	return lock, nil
}

func readRuntimeLockOwner(lockPath string) (runtimeLockOwner, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return runtimeLockOwner{}, err
	}
	var owner runtimeLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return runtimeLockOwner{}, err
	}
	if owner.PID <= 0 {
		return runtimeLockOwner{}, fmt.Errorf("invalid pid in %s", lockPath)
	}
	return owner, nil
}

func formatRuntimeLockOwner(owner runtimeLockOwner) string {
	if owner.AcquiredAt == "" {
		return fmt.Sprintf("pid=%d session=%s", owner.PID, owner.SessionID)
	}
	return fmt.Sprintf("pid=%d session=%s acquired=%s", owner.PID, owner.SessionID, owner.AcquiredAt)
}

func (l *runtimeLock) writeOwner() error {
	if _, err := l.file.Seek(0, 0); err != nil {
		return err
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	data, err := json.Marshal(l.owner)
	if err != nil {
		return err
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return l.file.Sync()
}

func (l *runtimeLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
