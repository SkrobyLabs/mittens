package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// Manager manages all active sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	store    *Store

	// MittensBin is the path to the mittens binary.
	MittensBin string

	// HubFactory creates an OutputHub for a new session.
	HubFactory func(sessionID string) OutputHub

	// ChannelDir is the base directory for channel sockets.
	ChannelDir string
}

// NewManager creates a new session manager.
func NewManager(mittensBin string, store *Store) *Manager {
	return &Manager{
		sessions:   make(map[string]*Session),
		store:      store,
		MittensBin: mittensBin,
	}
}

// Create starts a new mittens session.
func (m *Manager) Create(name string, cfg Config) (*Session, error) {
	id := uuid.New().String()[:8]
	if name == "" {
		name = "session-" + id
	}

	s := &Session{
		ID:        id,
		Name:      name,
		Config:    cfg,
		State:     StateRunning,
		CreatedAt: time.Now(),
		scrollbuf: NewRingBuffer(256 * 1024), // 256KB scrollback
	}

	// Build mittens command args, injecting channel socket before the -- separator.
	var channelSock string
	if m.ChannelDir != "" {
		sockDir := filepath.Join(m.ChannelDir, id)
		_ = os.MkdirAll(sockDir, 0o755)
		channelSock = filepath.Join(sockDir, "channel.sock")
	}
	args := m.buildArgs(cfg, channelSock)
	cmd := exec.Command(m.MittensBin, args...)
	cmd.Dir = cfg.WorkDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Start in a PTY.
	ph, err := StartPty(cmd, 24, 80)
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}
	s.ptyFd = ph
	s.PID = cmd.Process.Pid

	// Set up output hub if factory is available.
	if m.HubFactory != nil {
		hub := m.HubFactory(id)
		s.SetHub(hub)
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	// Persist state.
	m.persistState()

	// Start read loop in background.
	go m.readLoop(s)

	return s, nil
}

// readLoop reads PTY output and waits for process exit.
func (m *Manager) readLoop(s *Session) {
	_ = s.ptyFd.ReadLoop(s)

	// Process exited — wait for exit code.
	var code int
	if err := s.ptyFd.Cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}

	s.mu.Lock()
	s.State = StateStopped
	s.ExitCode = code
	s.StoppedAt = time.Now()
	hub := s.hub
	s.mu.Unlock()

	_ = s.ptyFd.Close()

	if hub != nil {
		hub.Exited(code)
		hub.StateChanged(StateStopped)
	}

	m.persistState()
}

// List returns all sessions.
func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	return list
}

// Get returns a session by ID.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Terminate stops a session.
func (m *Manager) Terminate(id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	s.mu.Lock()
	if s.State != StateRunning {
		s.mu.Unlock()
		return fmt.Errorf("session %s is not running", id)
	}
	pid := s.PID
	s.mu.Unlock()

	// SIGTERM first, then SIGKILL after grace period.
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = proc.Signal(syscall.SIGKILL)
			<-done
		}
	}

	return nil
}

// Resize resizes the PTY for a session.
func (m *Manager) Resize(id string, rows, cols uint16) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptyFd == nil {
		return fmt.Errorf("session %s has no PTY", id)
	}
	return s.ptyFd.Resize(rows, cols)
}

// WriteInput writes input to a session's PTY.
func (m *Manager) WriteInput(id string, data []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptyFd == nil || s.State != StateRunning {
		return fmt.Errorf("session %s is not running", id)
	}
	_, err := s.ptyFd.WriteInput(data)
	return err
}

// Relaunch terminates an existing session and starts a new one with merged config.
func (m *Manager) Relaunch(id string, cfg Config) (*Session, error) {
	old, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}

	// Terminate if still running.
	if old.State == StateRunning {
		if err := m.Terminate(id); err != nil {
			return nil, fmt.Errorf("terminate for relaunch: %w", err)
		}
		// Wait briefly for state to update.
		time.Sleep(500 * time.Millisecond)
	}

	// Remove old session so it doesn't show as a duplicate.
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()

	// Add --continue to claude args if not already present.
	hasContinue := false
	for _, arg := range cfg.ClaudeArgs {
		if arg == "--continue" || arg == "-c" {
			hasContinue = true
			break
		}
	}
	if !hasContinue {
		cfg.ClaudeArgs = append([]string{"--continue"}, cfg.ClaudeArgs...)
	}

	return m.Create(old.Name, cfg)
}

// Remove deletes a stopped session from the manager.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	if s.State == StateRunning {
		return fmt.Errorf("cannot remove running session")
	}

	delete(m.sessions, id)
	m.persistStateLocked()
	return nil
}

// Recover loads sessions from the store and checks their status.
func (m *Manager) Recover() {
	if m.store == nil {
		return
	}
	entries := m.store.Load()
	for _, e := range entries {
		s := &Session{
			ID:        e.ID,
			Name:      e.Name,
			Config:    e.Config,
			State:     e.State,
			PID:       e.PID,
			ExitCode:  e.ExitCode,
			CreatedAt: e.CreatedAt,
			StoppedAt: e.StoppedAt,
			scrollbuf: NewRingBuffer(256 * 1024),
		}

		// Check if PID is still alive.
		if e.State == StateRunning && e.PID > 0 {
			proc, err := os.FindProcess(e.PID)
			if err != nil || proc.Signal(syscall.Signal(0)) != nil {
				// Process is dead — mark as orphaned.
				s.State = StateOrphaned
				s.StoppedAt = time.Now()
			}
		}

		m.mu.Lock()
		m.sessions[s.ID] = s
		m.mu.Unlock()
	}
}

// buildArgs constructs the mittens CLI arguments from session config.
func (m *Manager) buildArgs(cfg Config, channelSock string) []string {
	var args []string

	for _, ext := range cfg.Extensions {
		args = append(args, "--"+ext)
	}
	for _, flag := range cfg.Flags {
		args = append(args, flag)
	}
	for _, dir := range cfg.ExtraDirs {
		args = append(args, "--dir", dir)
	}
	if channelSock != "" {
		args = append(args, "--channel-sock", channelSock)
	}

	// Separator for claude args — must come last.
	if len(cfg.ClaudeArgs) > 0 {
		args = append(args, "--")
		args = append(args, cfg.ClaudeArgs...)
	}

	return args
}

func (m *Manager) persistState() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.persistStateLocked()
}

func (m *Manager) persistStateLocked() {
	if m.store == nil {
		return
	}
	var entries []StoreEntry
	for _, s := range m.sessions {
		entries = append(entries, StoreEntry{
			ID:        s.ID,
			Name:      s.Name,
			Config:    s.Config,
			State:     s.State,
			PID:       s.PID,
			ExitCode:  s.ExitCode,
			CreatedAt: s.CreatedAt,
			StoppedAt: s.StoppedAt,
		})
	}
	m.store.Save(entries)
}
