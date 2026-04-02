package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

func TestHostAPIClient_SpawnWorker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/pool/spawn" {
			t.Errorf("path = %s, want /pool/spawn", r.URL.Path)
		}
		if r.Header.Get("X-Mittens-Token") != "tok123" {
			t.Errorf("token = %q", r.Header.Get("X-Mittens-Token"))
		}

		var spec pool.WorkerSpec
		json.NewDecoder(r.Body).Decode(&spec)
		if spec.ID != "w-1" {
			t.Errorf("spec.ID = %q, want w-1", spec.ID)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(spawnResp{
			ContainerName: "mittens-pool-w1",
			ContainerID:   "sha256:abc",
		})
	}))
	defer srv.Close()

	// Extract host:port from the test server URL.
	client := &hostAPIClient{
		baseURL: srv.URL,
		token:   "tok123",
		client:  srv.Client(),
	}

	name, id, err := client.SpawnWorker(context.Background(), pool.WorkerSpec{ID: "w-1", Role: "impl"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if name != "mittens-pool-w1" {
		t.Errorf("containerName = %q", name)
	}
	if id != "sha256:abc" {
		t.Errorf("containerID = %q", id)
	}
}

func TestHostAPIClient_SpawnWorker_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "docker failed", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		token:   "",
		client:  srv.Client(),
	}

	_, _, err := client.SpawnWorker(context.Background(), pool.WorkerSpec{ID: "w-1"})
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestHostAPIClient_KillWorker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pool/kill" {
			t.Errorf("path = %s, want /pool/kill", r.URL.Path)
		}
		var req struct {
			WorkerID string `json:"workerId"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.WorkerID != "w-1" {
			t.Errorf("workerID = %q, want w-1", req.WorkerID)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		token:   "",
		client:  srv.Client(),
	}

	err := client.KillWorker(context.Background(), "w-1")
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
}

func TestHostAPIClient_ListContainers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/pool/containers" {
			t.Errorf("path = %s, want /pool/containers", r.URL.Path)
		}
		sid := r.URL.Query().Get("sessionId")
		if sid != "team-123" {
			t.Errorf("sessionId = %q, want team-123", sid)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]pool.ContainerInfo{
			{ContainerID: "abc", WorkerID: "w-1", State: "running", Status: "Up 5 minutes"},
			{ContainerID: "def", WorkerID: "w-2", State: "exited", Status: "Exited (0) 2 minutes ago"},
		})
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		token:   "",
		client:  srv.Client(),
	}

	containers, err := client.ListContainers(context.Background(), "team-123")
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("len = %d, want 2", len(containers))
	}
	if containers[0].WorkerID != "w-1" {
		t.Errorf("containers[0].WorkerID = %q, want w-1", containers[0].WorkerID)
	}
	if containers[0].State != "running" {
		t.Errorf("containers[0].State = %q, want running", containers[0].State)
	}
	if containers[1].State != "exited" {
		t.Errorf("containers[1].State = %q, want exited", containers[1].State)
	}
}

func TestHostAPIClient_ListContainers_AllowsDottedSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.URL.Query().Get("sessionId"); sid != "release.v1" {
			t.Fatalf("sessionId = %q, want release.v1", sid)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]pool.ContainerInfo{
			{ContainerID: "abc", WorkerID: "w-1", State: "running", Status: "Up 5 minutes"},
		})
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		client:  srv.Client(),
	}

	containers, err := client.ListContainers(context.Background(), "release.v1")
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(containers) != 1 || containers[0].WorkerID != "w-1" {
		t.Fatalf("containers = %+v, want dotted-session response", containers)
	}
}

func TestHostAPIClient_CheckSession_AllowsDottedSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.URL.Query().Get("sessionId"); sid != "release.v1" {
			t.Fatalf("sessionId = %q, want release.v1", sid)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"alive": true})
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		client:  srv.Client(),
	}

	alive, err := client.CheckSession(context.Background(), "release.v1")
	if err != nil {
		t.Fatalf("check session: %v", err)
	}
	if !alive {
		t.Fatal("alive = false, want true")
	}
}

func TestHostAPIClient_ListContainers_AllowsLeadingPunctuationSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sid := r.URL.Query().Get("sessionId"); sid != ".scratch" {
			t.Fatalf("sessionId = %q, want .scratch", sid)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]pool.ContainerInfo{
			{ContainerID: "abc", WorkerID: "w-1", State: "running", Status: "Up 5 minutes"},
		})
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		client:  srv.Client(),
	}

	containers, err := client.ListContainers(context.Background(), ".scratch")
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(containers) != 1 || containers[0].WorkerID != "w-1" {
		t.Fatalf("containers = %+v, want leading-punctuation session response", containers)
	}
}

func TestHostAPIClient_ListContainers_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "docker error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		token:   "",
		client:  srv.Client(),
	}

	_, err := client.ListContainers(context.Background(), "team-123")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestHostAPIClient_KillWorker_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := &hostAPIClient{
		baseURL: srv.URL,
		token:   "",
		client:  srv.Client(),
	}

	err := client.KillWorker(context.Background(), "w-nonexistent")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}
