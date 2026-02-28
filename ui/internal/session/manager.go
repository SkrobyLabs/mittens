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

// Create starts a new mittens session inside a tmux session.
func (m *Manager) Create(name string, cfg Config) (*Session, error) {
	id := uuid.New().String()[:8]
	if name == "" {
		name = "session-" + id
	}

	tmuxName := TmuxSessionName(id)

	s := &Session{
		ID:        id,
		Name:      name,
		Config:    cfg,
		State:     StateRunning,
		CreatedAt: time.Now(),
		TmuxName:  tmuxName,
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

	// Full command: cd to workdir, set TERM, exec mittens.
	cmdArgs := []string{m.MittensBin}
	cmdArgs = append(cmdArgs, args...)
	fullCmd := fmt.Sprintf("cd %s && TERM=xterm-256color exec %s",
		shellQuoteArgs([]string{cfg.WorkDir}),
		shellQuoteArgs(cmdArgs),
	)

	// Create the tmux session (detached, runs mittens inside).
	if err := TmuxCreate(tmuxName, 80, 24, []string{"sh", "-c", fullCmd}); err != nil {
		return nil, fmt.Errorf("tmux create: %w", err)
	}

	// Attach to the tmux session via a PTY for I/O.
	ph, err := TmuxAttach(tmuxName, 24, 80)
	if err != nil {
		_ = TmuxKillSession(tmuxName)
		return nil, fmt.Errorf("tmux attach: %w", err)
	}
	s.ptyFd = ph
	s.PID = ph.Cmd.Process.Pid

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

	// The attach process ended — wait for its exit code.
	var code int
	if err := s.ptyFd.Cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	_ = s.ptyFd.Close()

	// If tmux-managed, check if the underlying tmux session is still alive.
	// If the attach was severed (e.g. server shutdown) but mittens is still
	// running inside tmux, don't mark as stopped — recovery will re-attach.
	s.mu.Lock()
	tmuxName := s.TmuxName
	s.mu.Unlock()

	if tmuxName != "" && TmuxHasSession(tmuxName) {
		// tmux session still alive — attach was severed, not a real exit.
		return
	}

	s.mu.Lock()
	s.State = StateStopped
	s.ExitCode = code
	s.StoppedAt = time.Now()
	hub := s.hub
	s.mu.Unlock()

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

// Terminate stops a session by killing its tmux session.
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
	tmuxName := s.TmuxName
	s.mu.Unlock()

	if tmuxName != "" {
		// Kill the tmux session — this terminates mittens and the attach
		// process. The readLoop will detect EOF and handle state transition.
		return TmuxKillSession(tmuxName)
	}

	// Fallback for legacy non-tmux sessions.
	s.mu.Lock()
	pid := s.PID
	s.mu.Unlock()

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

// Resize resizes the PTY for a session and the underlying tmux window.
func (m *Manager) Resize(id string, rows, cols uint16) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	s.mu.Lock()
	ptyFd := s.ptyFd
	tmuxName := s.TmuxName
	s.mu.Unlock()

	if ptyFd == nil {
		return fmt.Errorf("session %s has no PTY", id)
	}
	if err := ptyFd.Resize(rows, cols); err != nil {
		return err
	}

	// Also resize the tmux window so the inner process sees the new size.
	if tmuxName != "" {
		_ = TmuxResizeWindow(tmuxName, cols, rows)
	}

	return nil
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

	// Strip terminal response sequences (DA1/DA2) that xterm.js generates
	// in reply to tmux capability queries. Without this filter, responses
	// like "\e[>0;276;0c" leak into the tmux pane as visible garbage.
	if s.TmuxName != "" {
		data = FilterTerminalResponses(data)
		if len(data) == 0 {
			return nil
		}
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

// Recover loads sessions from the store and re-attaches to live tmux sessions.
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
			TmuxName:  e.TmuxName,
			scrollbuf: NewRingBuffer(256 * 1024),
		}

		if e.State == StateRunning {
			if e.TmuxName != "" && TmuxHasSession(e.TmuxName) {
				// tmux session alive — capture scrollback and re-attach.
				if captured, err := TmuxCapturePane(e.TmuxName); err == nil && len(captured) > 0 {
					s.scrollbuf.Write(captured)
				}
				ph, err := TmuxAttach(e.TmuxName, 24, 80)
				if err != nil {
					// Can't re-attach — mark orphaned.
					s.State = StateOrphaned
					s.StoppedAt = time.Now()
				} else {
					s.ptyFd = ph
					s.PID = ph.Cmd.Process.Pid

					// Wire up the output hub.
					if m.HubFactory != nil {
						hub := m.HubFactory(s.ID)
						s.SetHub(hub)
					}

					m.mu.Lock()
					m.sessions[s.ID] = s
					m.mu.Unlock()

					go m.readLoop(s)
					continue
				}
			} else {
				// No tmux session (legacy entry or tmux session died).
				s.State = StateStopped
				s.StoppedAt = time.Now()
			}
		}

		m.mu.Lock()
		m.sessions[s.ID] = s
		m.mu.Unlock()
	}

	m.persistState()
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
			TmuxName:  s.TmuxName,
		})
	}
	m.store.Save(entries)
}
