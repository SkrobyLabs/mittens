package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestRuntimeMuxSpawnWorkerRoutesByProviderAlias(t *testing.T) {
	calls := make(map[string]int)
	claudeSocket, closeClaude := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workers" {
			t.Fatalf("claude path = %s, want /v1/workers", r.URL.Path)
		}
		calls["claude"]++
		_ = json.NewEncoder(w).Encode(spawnResp{ContainerName: "claude-worker", ContainerID: "claude-1"})
	}))
	defer closeClaude()
	codexSocket, closeCodex := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workers" {
			t.Fatalf("codex path = %s, want /v1/workers", r.URL.Path)
		}
		calls["codex"]++
		_ = json.NewEncoder(w).Encode(spawnResp{ContainerName: "codex-worker", ContainerID: "codex-1"})
	}))
	defer closeCodex()

	mux := newRuntimeMux(map[string]pool.RuntimeAPI{
		"claude": newRuntimeClient(claudeSocket, "broker", "pool"),
		"codex":  newRuntimeClient(codexSocket, "broker", "pool"),
	})

	name, id, err := mux.SpawnWorker(context.Background(), pool.WorkerSpec{ID: "w-1", Provider: "openai"})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if name != "codex-worker" || id != "codex-1" {
		t.Fatalf("spawn = %q %q, want codex-worker codex-1", name, id)
	}
	if calls["codex"] != 1 || calls["claude"] != 0 {
		t.Fatalf("calls = %+v, want codex only", calls)
	}
}

func TestRuntimeMuxListContainersAggregatesAcrossProviders(t *testing.T) {
	claudeSocket, closeClaude := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workers": []pool.RuntimeWorker{{ID: "w-claude", ContainerID: "c-1", Status: "running"}},
		})
	}))
	defer closeClaude()
	codexSocket, closeCodex := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workers": []pool.RuntimeWorker{{ID: "w-codex", ContainerID: "c-2", Status: "running"}},
		})
	}))
	defer closeCodex()

	mux := newRuntimeMux(map[string]pool.RuntimeAPI{
		"claude": newRuntimeClient(claudeSocket, "broker", "pool"),
		"codex":  newRuntimeClient(codexSocket, "broker", "pool"),
	})

	containers, err := mux.ListContainers(context.Background(), "kitchen-test")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("containers = %+v, want 2 entries", containers)
	}
}

func TestRuntimeMuxSubscribeEventsFansInRuntimeStreams(t *testing.T) {
	claudeSocket, closeClaude := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"worker_spawned\",\"workerId\":\"w-claude\",\"timestamp\":\"%s\"}\n\n", time.Now().UTC().Format(time.RFC3339Nano))
		flusher.Flush()
	}))
	defer closeClaude()
	codexSocket, closeCodex := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"worker_spawned\",\"workerId\":\"w-codex\",\"timestamp\":\"%s\"}\n\n", time.Now().UTC().Format(time.RFC3339Nano))
		flusher.Flush()
	}))
	defer closeCodex()

	mux := newRuntimeMux(map[string]pool.RuntimeAPI{
		"claude": newRuntimeClient(claudeSocket, "broker", "pool"),
		"codex":  newRuntimeClient(codexSocket, "broker", "pool"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := mux.SubscribeEvents(ctx)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}
	seen := make(map[string]bool)
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case event := <-events:
			seen[event.WorkerID] = true
		case <-deadline:
			t.Fatalf("timed out waiting for multiplexed events, saw %v", seen)
		}
	}
}
