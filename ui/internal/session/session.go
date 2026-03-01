package session

import (
	"sync"
	"time"
)

// State represents the lifecycle state of a session.
type State string

const (
	StateRunning    State = "running"
	StateStopped    State = "stopped"
	StateTerminated State = "terminated"
	StateOrphaned   State = "orphaned"
)

// Config holds the configuration used to launch a mittens session.
type Config struct {
	WorkDir    string   `json:"workDir"`
	Extensions []string `json:"extensions,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	ClaudeArgs []string `json:"claudeArgs,omitempty"`
	ExtraDirs  []string `json:"extraDirs,omitempty"`
	Shell      bool     `json:"shell,omitempty"`
}

// Session represents a single mittens subprocess with its PTY.
type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Config    Config    `json:"config"`
	State     State     `json:"state"`
	PID       int       `json:"pid"`
	ExitCode  int       `json:"exitCode"`
	CreatedAt time.Time `json:"createdAt"`
	StoppedAt time.Time `json:"stoppedAt,omitempty"`
	TmuxName  string    `json:"tmuxName,omitempty"`

	mu           sync.Mutex
	ptyFd        *PtyHandle
	scrollbuf    *RingBuffer
	hub          OutputHub
	paneExitCode int
	paneExitSet  bool
}

// OutputHub broadcasts terminal output to connected WebSocket clients.
type OutputHub interface {
	Broadcast(data []byte)
	StateChanged(state State)
	Exited(code int)
}

// SetHub sets the output hub for broadcasting terminal data.
func (s *Session) SetHub(h OutputHub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub = h
}

// Scrollback returns a copy of the scrollback buffer contents.
func (s *Session) Scrollback() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scrollbuf == nil {
		return nil
	}
	return s.scrollbuf.Bytes()
}

// Write implements io.Writer — called from the PTY read loop.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	hub := s.hub
	buf := s.scrollbuf
	s.mu.Unlock()

	if buf != nil {
		buf.Write(p)
	}
	if hub != nil {
		hub.Broadcast(p)
	}
	return len(p), nil
}

// RingBuffer is a fixed-size circular buffer for scrollback.
type RingBuffer struct {
	mu   sync.Mutex
	data []byte
	size int
	pos  int
	full bool
}

// NewRingBuffer creates a ring buffer of the given capacity.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{data: make([]byte, size), size: size}
}

// Write appends data to the ring buffer.
func (r *RingBuffer) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, b := range p {
		r.data[r.pos] = b
		r.pos = (r.pos + 1) % r.size
		if r.pos == 0 {
			r.full = true
		}
	}
}

// Reset clears all data in the ring buffer.
func (r *RingBuffer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pos = 0
	r.full = false
}

// Bytes returns the contents of the ring buffer in order.
func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		out := make([]byte, r.pos)
		copy(out, r.data[:r.pos])
		return out
	}
	out := make([]byte, r.size)
	n := copy(out, r.data[r.pos:])
	copy(out[n:], r.data[:r.pos])
	return out
}
