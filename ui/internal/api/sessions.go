package api

import (
	"encoding/json"
	"net/http"

	"github.com/Skroby/mittens/ui/internal/session"
)

// SessionHandler handles REST endpoints for sessions.
type SessionHandler struct {
	Sessions *session.Manager
}

// CreateRequest is the JSON body for creating a session.
type CreateRequest struct {
	Name       string   `json:"name"`
	WorkDir    string   `json:"workDir"`
	Extensions []string `json:"extensions,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	ClaudeArgs []string `json:"claudeArgs,omitempty"`
	ExtraDirs  []string `json:"extraDirs,omitempty"`
}

// ResizeRequest is the JSON body for resizing a session.
type ResizeRequest struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// RelaunchRequest is the JSON body for relaunching a session.
type RelaunchRequest struct {
	WorkDir    string   `json:"workDir,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	ClaudeArgs []string `json:"claudeArgs,omitempty"`
	ExtraDirs  []string `json:"extraDirs,omitempty"`
}

// SessionResponse is the JSON response for a session.
type SessionResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Config    session.Config `json:"config"`
	State     session.State  `json:"state"`
	PID       int            `json:"pid"`
	ExitCode  int            `json:"exitCode"`
	CreatedAt string         `json:"createdAt"`
	StoppedAt string         `json:"stoppedAt,omitempty"`
}

func sessionToResponse(s *session.Session) SessionResponse {
	resp := SessionResponse{
		ID:        s.ID,
		Name:      s.Name,
		Config:    s.Config,
		State:     s.State,
		PID:       s.PID,
		ExitCode:  s.ExitCode,
		CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if !s.StoppedAt.IsZero() {
		resp.StoppedAt = s.StoppedAt.Format("2006-01-02T15:04:05Z")
	}
	return resp
}

// List returns all sessions.
func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	sessions := h.Sessions.List()
	var resp []SessionResponse
	for _, s := range sessions {
		resp = append(resp, sessionToResponse(s))
	}
	if resp == nil {
		resp = []SessionResponse{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// Create creates a new session.
func (h *SessionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.WorkDir == "" {
		writeError(w, http.StatusBadRequest, "workDir is required")
		return
	}

	cfg := session.Config{
		WorkDir:    req.WorkDir,
		Extensions: req.Extensions,
		Flags:      req.Flags,
		ClaudeArgs: req.ClaudeArgs,
		ExtraDirs:  req.ExtraDirs,
	}

	s, err := h.Sessions.Create(req.Name, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, sessionToResponse(s))
}

// Get returns a single session.
func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.Sessions.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sessionToResponse(s))
}

// Terminate stops a running session, or removes a stopped one.
func (h *SessionHandler) Terminate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s, ok := h.Sessions.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if s.State == session.StateRunning {
		if err := h.Sessions.Terminate(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Remove non-running sessions from the list.
	_ = h.Sessions.Remove(id)
	w.WriteHeader(http.StatusNoContent)
}

// Relaunch terminates and restarts a session with new config.
func (h *SessionHandler) Relaunch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req RelaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Get existing session for defaults.
	old, ok := h.Sessions.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	cfg := old.Config
	if req.WorkDir != "" {
		cfg.WorkDir = req.WorkDir
	}
	if req.Extensions != nil {
		cfg.Extensions = req.Extensions
	}
	if req.Flags != nil {
		cfg.Flags = req.Flags
	}
	if req.ClaudeArgs != nil {
		cfg.ClaudeArgs = req.ClaudeArgs
	}
	if req.ExtraDirs != nil {
		cfg.ExtraDirs = req.ExtraDirs
	}

	s, err := h.Sessions.Relaunch(id, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, sessionToResponse(s))
}

// Resize changes the PTY size for a session.
func (h *SessionHandler) Resize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req ResizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.Sessions.Resize(id, uint16(req.Rows), uint16(req.Cols)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Scrollback returns the scrollback buffer for a session.
func (h *SessionHandler) Scrollback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.Sessions.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	data := s.Scrollback()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
