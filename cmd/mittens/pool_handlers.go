package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

// OnPoolSpawn is called when a leader container requests a worker spawn.
// Returns containerName and containerID.
// Set this callback on HostBroker before Serve().

// OnPoolKill is called when a leader container requests a worker kill.
// Set this callback on HostBroker before Serve().

type poolSpawnResp struct {
	ContainerName string `json:"containerName"`
	ContainerID   string `json:"containerId"`
}

func (b *HostBroker) handlePoolSpawn(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	body, ok := b.readBody(w, r, 64*1024)
	if !ok {
		return
	}

	var spec pool.WorkerSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if b.OnPoolSpawn == nil {
		http.Error(w, "pool spawn not configured", http.StatusServiceUnavailable)
		return
	}

	containerName, containerID, err := b.OnPoolSpawn(spec)
	if err != nil {
		b.blog("POOL SPAWN → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	b.blog("POOL SPAWN → %s (%s)", containerName, containerID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(poolSpawnResp{
		ContainerName: containerName,
		ContainerID:   containerID,
	})
}

func (b *HostBroker) handlePoolContainers(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing required sessionId parameter", http.StatusBadRequest)
		return
	}
	if pool.ValidateID(sessionID) != nil {
		http.Error(w, "invalid sessionId", http.StatusBadRequest)
		return
	}

	out, err := captureCommand("docker", "ps",
		"--filter", "label=mittens.pool="+sessionID,
		"--format", `{{.ID}}\t{{.Label "mittens.worker_id"}}\t{{.Status}}`)
	if err != nil {
		b.blog("POOL CONTAINERS → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var containers []pool.ContainerInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		containers = append(containers, pool.ContainerInfo{
			ContainerID: parts[0],
			WorkerID:    parts[1],
			Status:      parts[2],
		})
	}

	b.blog("POOL CONTAINERS → %d containers for session %s", len(containers), sessionID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(containers)
}

func (b *HostBroker) handleSessionAlive(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing required sessionId parameter", http.StatusBadRequest)
		return
	}
	if pool.ValidateID(sessionID) != nil {
		http.Error(w, "invalid sessionId", http.StatusBadRequest)
		return
	}

	out, err := captureCommand("docker", "ps", "-q",
		"--filter", "label=mittens.pool="+sessionID,
		"--filter", "label=mittens.role=leader")
	if err != nil {
		b.blog("SESSION ALIVE → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	containerID := strings.TrimSpace(out)
	alive := containerID != ""
	b.blog("SESSION ALIVE → session=%s alive=%v", sessionID, alive)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"alive": alive, "containerId": containerID})
}

func (b *HostBroker) handlePoolKill(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	body, ok := b.readBody(w, r, 4096)
	if !ok {
		return
	}

	var req struct {
		WorkerID string `json:"workerId"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.WorkerID == "" {
		http.Error(w, "invalid request: need workerId", http.StatusBadRequest)
		return
	}

	if b.OnPoolKill == nil {
		http.Error(w, "pool kill not configured", http.StatusServiceUnavailable)
		return
	}

	if err := b.OnPoolKill(req.WorkerID); err != nil {
		b.blog("POOL KILL → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	b.blog("POOL KILL → %s", req.WorkerID)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"ok":true}`)
}
