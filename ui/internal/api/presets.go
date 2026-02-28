package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// PresetHandler handles wizard configuration preset endpoints.
type PresetHandler struct {
	path string // path to presets.json
	mu   sync.Mutex
}

// NewPresetHandler creates a handler that persists presets to dir/presets.json.
func NewPresetHandler(dir string) *PresetHandler {
	return &PresetHandler{path: filepath.Join(dir, "presets.json")}
}

// Preset is a saved wizard configuration.
type Preset struct {
	Name       string          `json:"name"`
	WorkDir    string          `json:"workDir,omitempty"`
	Extensions json.RawMessage `json:"extensions,omitempty"` // ExtensionToggle[]
	Flags      json.RawMessage `json:"options,omitempty"`    // OptionsTabState
	ExtraDirs  []string        `json:"extraDirs,omitempty"`
	ClaudeArgs string          `json:"claudeArgs,omitempty"`
}

// List returns all saved presets.
func (h *PresetHandler) List(w http.ResponseWriter, r *http.Request) {
	presets := h.load()
	if presets == nil {
		presets = []Preset{}
	}
	writeJSON(w, http.StatusOK, presets)
}

// Save creates or updates a preset (matched by name).
func (h *PresetHandler) Save(w http.ResponseWriter, r *http.Request) {
	var p Preset
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	presets := h.loadLocked()
	// Upsert by name.
	found := false
	for i, existing := range presets {
		if existing.Name == p.Name {
			presets[i] = p
			found = true
			break
		}
	}
	if !found {
		presets = append(presets, p)
	}
	h.saveLocked(presets)
	writeJSON(w, http.StatusOK, p)
}

// Delete removes a preset by name.
func (h *PresetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	presets := h.loadLocked()
	filtered := presets[:0]
	for _, p := range presets {
		if p.Name != name {
			filtered = append(filtered, p)
		}
	}
	h.saveLocked(filtered)
	w.WriteHeader(http.StatusNoContent)
}

func (h *PresetHandler) load() []Preset {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loadLocked()
}

func (h *PresetHandler) loadLocked() []Preset {
	data, err := os.ReadFile(h.path)
	if err != nil {
		return nil
	}
	var presets []Preset
	if err := json.Unmarshal(data, &presets); err != nil {
		return nil
	}
	return presets
}

func (h *PresetHandler) saveLocked(presets []Preset) {
	data, err := json.MarshalIndent(presets, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(h.path, data, 0o644)
}
