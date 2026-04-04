package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type runtimeWorkersResp struct {
	Workers []pool.RuntimeWorker `json:"workers"`
}

type poolSpawnResp struct {
	ContainerName string `json:"containerName"`
	ContainerID   string `json:"containerId"`
}

type runtimeActivityResp struct {
	Activity   *pool.WorkerActivity        `json:"activity,omitempty"`
	Transcript []pool.WorkerActivityRecord `json:"transcript,omitempty"`
}

func (b *HostBroker) handleRuntimeSpawnWorker(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "runtime spawn not configured", http.StatusServiceUnavailable)
		return
	}

	containerName, containerID, err := b.OnPoolSpawn(spec)
	if err != nil {
		b.blog("RUNTIME SPAWN → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	b.sendRuntimeEvent(pool.RuntimeEvent{
		Type:     "worker_spawned",
		WorkerID: spec.ID,
		Message:  containerName,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(poolSpawnResp{
		ContainerName: containerName,
		ContainerID:   containerID,
	})
}

func (b *HostBroker) handleRuntimeKillWorker(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodDelete) {
		return
	}
	if b.OnPoolKill == nil {
		http.Error(w, "runtime kill not configured", http.StatusServiceUnavailable)
		return
	}
	workerID := r.PathValue("id")
	if workerID == "" {
		http.Error(w, "missing worker id", http.StatusBadRequest)
		return
	}
	if err := b.OnPoolKill(workerID); err != nil {
		b.blog("RUNTIME KILL → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.sendRuntimeEvent(pool.RuntimeEvent{
		Type:     "worker_killed",
		WorkerID: workerID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (b *HostBroker) handleRuntimeWorkers(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}
	if b.OnRuntimeListWorkers == nil {
		http.Error(w, "runtime worker listing not configured", http.StatusServiceUnavailable)
		return
	}
	workers, err := b.OnRuntimeListWorkers()
	if err != nil {
		b.blog("RUNTIME WORKERS → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runtimeWorkersResp{Workers: workers})
}

func (b *HostBroker) handleRuntimeWorker(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}
	if b.OnRuntimeGetWorker == nil {
		http.Error(w, "runtime worker status not configured", http.StatusServiceUnavailable)
		return
	}
	workerID := r.PathValue("id")
	worker, err := b.OnRuntimeGetWorker(workerID)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "worker not found", http.StatusNotFound)
			return
		}
		b.blog("RUNTIME WORKER → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(worker)
}

func (b *HostBroker) handleRuntimeRecycleWorker(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	if b.OnRuntimeRecycleWorker == nil {
		http.Error(w, "runtime recycle not configured", http.StatusServiceUnavailable)
		return
	}
	workerID := r.PathValue("id")
	if err := b.OnRuntimeRecycleWorker(workerID); err != nil {
		b.blog("RUNTIME RECYCLE → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.sendRuntimeEvent(pool.RuntimeEvent{
		Type:     "worker_recycled",
		WorkerID: workerID,
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"recycled"}`)
}

func (b *HostBroker) handleRuntimeWorkerActivity(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}
	if b.OnRuntimeGetWorkerActivity == nil {
		http.Error(w, "runtime activity not configured", http.StatusServiceUnavailable)
		return
	}
	workerID := r.PathValue("id")
	activity, transcript, err := b.OnRuntimeGetWorkerActivity(workerID)
	if err != nil {
		b.blog("RUNTIME ACTIVITY → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runtimeActivityResp{
		Activity:   activity,
		Transcript: transcript,
	})
}

func (b *HostBroker) handleRuntimeWorkerAssignment(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}
	if b.OnRuntimeSubmitAssignment == nil {
		http.Error(w, "runtime assignments not configured", http.StatusServiceUnavailable)
		return
	}
	workerID := r.PathValue("id")
	body, ok := b.readBody(w, r, 64*1024)
	if !ok {
		return
	}
	var assignment pool.Assignment
	if err := json.Unmarshal(body, &assignment); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := b.OnRuntimeSubmitAssignment(workerID, assignment); err != nil {
		b.blog("RUNTIME ASSIGNMENT → error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b.sendRuntimeEvent(pool.RuntimeEvent{
		Type:         "assignment_submitted",
		WorkerID:     workerID,
		AssignmentID: assignment.ID,
		Message:      assignment.Type,
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"accepted"}`)
}

func (b *HostBroker) handleRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	events, cancel := b.subscribeRuntimeEvents(32)
	defer cancel()

	fmt.Fprintf(w, ": runtime events\n\n")
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\n", event.Type)
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
