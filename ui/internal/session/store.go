package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StoreEntry is the JSON-serializable representation of a session.
type StoreEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Config    Config    `json:"config"`
	State     State     `json:"state"`
	PID       int       `json:"pid"`
	ExitCode  int       `json:"exitCode"`
	CreatedAt time.Time `json:"createdAt"`
	StoppedAt time.Time `json:"stoppedAt,omitempty"`
	TmuxName  string    `json:"tmuxName,omitempty"`
}

// Store persists session metadata to a JSON file.
type Store struct {
	path string
}

// NewStore creates a store at the given directory.
func NewStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{path: filepath.Join(dir, "state.json")}
}

// Load reads all session entries from the store.
func (s *Store) Load() []StoreEntry {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var entries []StoreEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}

// Save writes all session entries to the store.
func (s *Store) Save(entries []StoreEntry) {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, data, 0o644)
}
