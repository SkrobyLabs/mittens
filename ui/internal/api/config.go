package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"sync"
)

// ConfigHandler handles configuration endpoints.
type ConfigHandler struct {
	MittensBin string

	mu       sync.Mutex
	capsData json.RawMessage
}

// ListExtensions returns the full capabilities JSON from mittens --json-caps.
func (h *ConfigHandler) ListExtensions(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.capsData == nil {
		cmd := exec.Command(h.MittensBin, "--json-caps")
		out, err := cmd.Output()
		if err != nil {
			log.Printf("mittens --json-caps failed (bin=%s): %v", h.MittensBin, err)
		} else {
			h.capsData = json.RawMessage(out)
		}
	}
	data := h.capsData
	h.mu.Unlock()

	if data == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"extensions": []interface{}{},
			"coreFlags":  []interface{}{},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
