package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestRuntimeClientSpawnWorker(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/workers" {
			t.Errorf("path = %s, want /v1/workers", r.URL.Path)
		}
		if r.Header.Get("X-Mittens-Token") != "tok123" {
			t.Errorf("token = %q", r.Header.Get("X-Mittens-Token"))
		}
		if r.Header.Get("X-Mittens-Pool-Token") != "pool123" {
			t.Errorf("pool token = %q", r.Header.Get("X-Mittens-Pool-Token"))
		}

		var spec pool.WorkerSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if spec.ID != "w-1" {
			t.Errorf("spec.ID = %q, want w-1", spec.ID)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(spawnResp{
			ContainerName: "mittens-runtime-w1",
			ContainerID:   "sha256:abc",
		})
	}))
	defer closeFn()

	client := newRuntimeClient(socketPath, "tok123", "pool123")
	name, id, err := client.SpawnWorker(context.Background(), pool.WorkerSpec{ID: "w-1", Role: "impl"})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if name != "mittens-runtime-w1" {
		t.Fatalf("containerName = %q, want mittens-runtime-w1", name)
	}
	if id != "sha256:abc" {
		t.Fatalf("containerID = %q, want sha256:abc", id)
	}
}

func TestRuntimeClientListContainers(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workers" {
			t.Fatalf("path = %s, want /v1/workers", r.URL.Path)
		}
		if sid := r.URL.Query().Get("sessionId"); sid != "kitchen-123" {
			t.Fatalf("sessionId = %q, want kitchen-123", sid)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workers": []pool.RuntimeWorker{{
				ID:          "w-1",
				ContainerID: "abc",
				Status:      "running",
			}},
		})
	}))
	defer closeFn()

	client := newRuntimeClient(socketPath, "tok123", "pool123")
	containers, err := client.ListContainers(context.Background(), "kitchen-123")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(containers) != 1 || containers[0].WorkerID != "w-1" {
		t.Fatalf("containers = %+v, want worker w-1", containers)
	}
}

func TestKitchenHostAPIFromEnvPrefersRuntimeSocket(t *testing.T) {
	t.Setenv("MITTENS_RUNTIME_SOCKET", "/tmp/mittens-runtime.sock")
	t.Setenv("MITTENS_BROKER_TOKEN", "broker-token")
	t.Setenv("MITTENS_POOL_TOKEN", "pool-token")

	hostAPI := kitchenHostAPIFromEnv()
	client, ok := hostAPI.(*runtimeClient)
	if !ok {
		t.Fatalf("hostAPI = %T, want *runtimeClient", hostAPI)
	}
	if client.socketPath != "/tmp/mittens-runtime.sock" {
		t.Fatalf("socketPath = %q, want /tmp/mittens-runtime.sock", client.socketPath)
	}
}

func TestKitchenHostAPIFromEnvLoadsRuntimeMetadata(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer closeFn()

	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	t.Setenv("MITTENS_RUNTIME_SOCKET", "")
	t.Setenv("MITTENS_BROKER_TOKEN", "")
	t.Setenv("MITTENS_POOL_TOKEN", "")
	if err := os.WriteFile(filepath.Join(tmpHome, "runtime.json"), []byte(fmt.Sprintf("{\n  \"socketPath\": %q,\n  \"poolToken\": \"pool-token\",\n  \"brokerToken\": \"broker-token\"\n}\n", socketPath)), 0o644); err != nil {
		t.Fatalf("WriteFile runtime metadata: %v", err)
	}

	hostAPI := kitchenHostAPIFromEnv()
	client, ok := hostAPI.(*runtimeClient)
	if !ok {
		t.Fatalf("hostAPI = %T, want *runtimeClient", hostAPI)
	}
	if client.socketPath != socketPath {
		t.Fatalf("socketPath = %q, want %q", client.socketPath, socketPath)
	}
}

func TestKitchenHostPoolFromEnvLoadsRuntimeMetadataProvider(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer closeFn()

	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)
	if err := os.WriteFile(filepath.Join(tmpHome, "runtime.json"), []byte(fmt.Sprintf("{\n  \"socketPath\": %q,\n  \"poolToken\": \"pool-token\",\n  \"brokerToken\": \"broker-token\",\n  \"provider\": \"codex\",\n  \"model\": \"gpt-5.4\"\n}\n", socketPath)), 0o644); err != nil {
		t.Fatalf("WriteFile runtime metadata: %v", err)
	}

	keys := kitchenHostPoolFromEnv()
	if len(keys) != 1 {
		t.Fatalf("host pool keys = %+v, want one entry", keys)
	}
	if keys[0].Provider != "codex" || keys[0].Model != "gpt-5.4" {
		t.Fatalf("host pool key = %+v, want codex/gpt-5.4", keys[0])
	}
}

func TestKitchenHostAPIFromEnvReturnsNilWithoutRuntimeSocketOrMetadata(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	t.Setenv("MITTENS_RUNTIME_SOCKET", "")
	t.Setenv("MITTENS_BROKER_TOKEN", "")
	t.Setenv("MITTENS_POOL_TOKEN", "")
	if hostAPI := kitchenHostAPIFromEnv(); hostAPI != nil {
		t.Fatalf("hostAPI = %T, want nil", hostAPI)
	}
}

func TestRuntimeClientGetWorkerActivity(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workers/w-1/activity" {
			t.Fatalf("path = %s, want /v1/workers/w-1/activity", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"activity": pool.WorkerActivity{
				Kind:  "tool",
				Phase: "started",
				Name:  "apply_patch",
			},
		})
	}))
	defer closeFn()

	client := newRuntimeClient(socketPath, "tok123", "pool123")
	activity, err := client.GetWorkerActivity(context.Background(), "w-1")
	if err != nil {
		t.Fatalf("GetWorkerActivity: %v", err)
	}
	if activity == nil || activity.Name != "apply_patch" {
		t.Fatalf("activity = %+v, want apply_patch", activity)
	}
}

func TestRuntimeClientSubmitAssignment(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/workers/w-1/assignments" {
			t.Fatalf("path = %s, want /v1/workers/w-1/assignments", r.URL.Path)
		}
		var assignment pool.Assignment
		if err := json.NewDecoder(r.Body).Decode(&assignment); err != nil {
			t.Fatalf("decode assignment: %v", err)
		}
		if assignment.ID != "assign-1" {
			t.Fatalf("assignment.ID = %q, want assign-1", assignment.ID)
		}
		_, _ = io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer closeFn()

	client := newRuntimeClient(socketPath, "tok123", "pool123")
	if err := client.SubmitAssignment(context.Background(), "w-1", pool.Assignment{ID: "assign-1", Type: "plan"}); err != nil {
		t.Fatalf("SubmitAssignment: %v", err)
	}
}

func TestRuntimeClientSubscribeEvents(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" {
			t.Fatalf("path = %s, want /v1/events", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "event: worker_spawned\n")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"worker_spawned\",\"workerId\":\"w-1\",\"timestamp\":\"%s\"}\n\n", time.Now().UTC().Format(time.RFC3339Nano))
		flusher.Flush()
	}))
	defer closeFn()

	client := newRuntimeClient(socketPath, "tok123", "pool123")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := client.SubscribeEvents(ctx)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}

	select {
	case event := <-events:
		if event.Type != "worker_spawned" || event.WorkerID != "w-1" {
			t.Fatalf("event = %+v, want worker_spawned/w-1", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime event")
	}
}

func startUnixRuntimeTestServer(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "runtime.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen(unix): %v", err)
	}

	srv := &http.Server{Handler: handler}
	go func() {
		_ = srv.Serve(ln)
	}()

	return socketPath, func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}
}
