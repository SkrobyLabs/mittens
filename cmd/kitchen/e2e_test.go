package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestKitchenEndToEndWithRuntimeClient(t *testing.T) {
	runtime := newFakeRuntimeDaemon(t, "broker-token", "pool-token")
	defer runtime.Close()
	hostAPI := newRuntimeClient(runtime.socketPath, "broker-token", "pool-token")
	k := newTestKitchenWithHostAPI(t, hostAPI)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	bundle, err := k.SubmitIdea("Add end-to-end Kitchen coverage", "kitchen-e2e", false, false)
	if err != nil {
		t.Fatalf("SubmitIdea: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return runtime.SpawnCount() >= 1 })
	planningSpawn := runtime.Spawn(0)
	if planningSpawn.Role != plannerTaskRole {
		t.Fatalf("planning spawn role = %q, want %q", planningSpawn.Role, plannerTaskRole)
	}

	completePlannerSpawn(t, k, runtime, planningSpawn, adapter.PlanArtifact{
		Title:   "Kitchen end-to-end coverage",
		Summary: "Implement one end-to-end test task.",
		Tasks: []adapter.PlanArtifactTask{{
			ID:               "t1",
			Title:            "Implement end-to-end Kitchen coverage",
			Prompt:           "Add end-to-end Kitchen coverage in this repository.",
			Complexity:       string(ComplexityMedium),
			ReviewComplexity: string(ComplexityMedium),
		}},
	})

	waitFor(t, 2*time.Second, func() bool {
		planned, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && planned.Execution.State == planStatePendingApproval
	})

	if err := k.ApprovePlan(bundle.Plan.PlanID); err != nil {
		t.Fatalf("ApprovePlan: %v", err)
	}

	implSpawn := waitForSpawnByRole(t, runtime, "implementer", 1)[0]
	if implSpawn.WorkspacePath == "" {
		t.Fatal("implementation spawn missing workspacePath")
	}
	if _, err := os.Stat(implSpawn.WorkspacePath); err != nil {
		t.Fatalf("implementation workspacePath stat: %v", err)
	}

	implTask := registerAndPollWorkerTask(t, k, implSpawn.ID, implSpawn.containerID)
	if implTask.PlanID != bundle.Plan.PlanID {
		t.Fatalf("implementation task planID = %q, want %q", implTask.PlanID, bundle.Plan.PlanID)
	}
	writeFile(t, filepath.Join(implSpawn.WorkspacePath, "feature.txt"), "lineage change\n")
	mustRunGit(t, implSpawn.WorkspacePath, "add", "feature.txt")
	mustRunGit(t, implSpawn.WorkspacePath, "commit", "-m", "worker change")
	writeWorkerResult(t, k, implSpawn.ID, "implemented\n")
	completeWorkerTask(t, k, implSpawn.ID, implTask.ID)

	waitFor(t, 3*time.Second, func() bool {
		got, err := k.GetPlan(bundle.Plan.PlanID)
		return err == nil && got.Execution.State == planStateCompleted
	})

	got, err := k.GetPlan(bundle.Plan.PlanID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Execution.State != planStateCompleted {
		t.Fatalf("state = %q, want %q", got.Execution.State, planStateCompleted)
	}

	content, err := runGit(k.repoPath, "show", lineageBranchName(got.Plan.Lineage)+":feature.txt")
	if err != nil {
		t.Fatalf("show lineage file: %v", err)
	}
	if strings.TrimSpace(content) != "lineage change" {
		t.Fatalf("lineage file = %q, want merged worker change", content)
	}
}

func newTestKitchenWithHostAPI(t *testing.T, hostAPI pool.RuntimeAPI) *Kitchen {
	t.Helper()

	repo := initGitRepo(t)
	paths := newKitchenTestPaths(t)
	project, err := paths.Project(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Ensure(); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(project.PoolsDir, defaultPoolStateName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wal, err := pool.OpenWAL(filepath.Join(stateDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wal.Close() })

	pm := pool.NewPoolManager(pool.PoolConfig{
		SessionID:  "kitchen-test",
		MaxWorkers: 8,
		StateDir:   stateDir,
	}, wal, hostAPI)

	health, err := NewProviderHealth(filepath.Join(project.RootDir, "provider_health.json"))
	if err != nil {
		t.Fatal(err)
	}

	return &Kitchen{
		pm:         pm,
		wal:        wal,
		hostAPI:    hostAPI,
		router:     NewComplexityRouter(DefaultKitchenConfig(), health),
		health:     health,
		planStore:  NewPlanStore(project.PlansDir),
		lineageMgr: NewLineageManager(project.LineagesDir, project.PlansDir),
		cfg:        DefaultKitchenConfig(),
		repoPath:   repo,
		paths:      paths,
		project:    project,
	}
}

type fakeRuntimeDaemon struct {
	rootDir    string
	socketPath string
	server     *http.Server
	listener   net.Listener

	token     string
	poolToken string

	mu         sync.Mutex
	spawnSpecs []fakeRuntimeSpawn
	containers map[string]pool.ContainerInfo
	eventSubs  map[int]chan pool.RuntimeEvent
	eventSeq   int
}

type fakeRuntimeSpawn struct {
	pool.WorkerSpec
	containerID string
}

func newFakeRuntimeDaemon(t *testing.T, token, poolToken string) *fakeRuntimeDaemon {
	t.Helper()

	rootDir, err := os.MkdirTemp("", "mtnrt-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	socketPath := filepath.Join(rootDir, "runtime.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen(unix): %v", err)
	}

	rt := &fakeRuntimeDaemon{
		rootDir:    rootDir,
		socketPath: socketPath,
		listener:   ln,
		token:      token,
		poolToken:  poolToken,
		containers: make(map[string]pool.ContainerInfo),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/workers", rt.handleSpawn)
	mux.HandleFunc("DELETE /v1/workers/{id}", rt.handleKill)
	mux.HandleFunc("GET /v1/workers", rt.handleContainers)
	mux.HandleFunc("POST /v1/workers/{id}/recycle", rt.handleRecycle)
	mux.HandleFunc("GET /v1/events", rt.handleEvents)
	rt.server = &http.Server{Handler: mux}
	go func() {
		_ = rt.server.Serve(ln)
	}()
	return rt
}

func (r *fakeRuntimeDaemon) Close() {
	if r.server != nil {
		_ = r.server.Close()
	}
	if r.listener != nil {
		_ = r.listener.Close()
	}
	_ = os.Remove(r.socketPath)
	if r.rootDir != "" {
		_ = os.RemoveAll(r.rootDir)
	}
}

func (r *fakeRuntimeDaemon) SpawnCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spawnSpecs)
}

func (r *fakeRuntimeDaemon) Spawn(index int) fakeRuntimeSpawn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spawnSpecs[index]
}

func (r *fakeRuntimeDaemon) Spawns() []fakeRuntimeSpawn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]fakeRuntimeSpawn, len(r.spawnSpecs))
	copy(out, r.spawnSpecs)
	return out
}

func (r *fakeRuntimeDaemon) authorize(w http.ResponseWriter, req *http.Request) bool {
	if got := req.Header.Get("X-Mittens-Token"); got != r.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if got := req.Header.Get("X-Mittens-Pool-Token"); got != r.poolToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (r *fakeRuntimeDaemon) handleSpawn(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req) {
		return
	}
	var spec pool.WorkerSpec
	if err := json.NewDecoder(req.Body).Decode(&spec); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	containerID := "cid-" + spec.ID

	r.mu.Lock()
	r.spawnSpecs = append(r.spawnSpecs, fakeRuntimeSpawn{WorkerSpec: spec, containerID: containerID})
	r.containers[spec.ID] = pool.ContainerInfo{
		ContainerID: containerID,
		WorkerID:    spec.ID,
		State:       "running",
		Status:      "Up 1 second",
	}
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spawnResp{
		ContainerName: "mittens-runtime-" + spec.ID,
		ContainerID:   containerID,
	})
}

func (r *fakeRuntimeDaemon) handleKill(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req) {
		return
	}
	workerID := req.PathValue("id")
	r.mu.Lock()
	delete(r.containers, workerID)
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *fakeRuntimeDaemon) handleContainers(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req) {
		return
	}
	r.mu.Lock()
	containers := make([]pool.ContainerInfo, 0, len(r.containers))
	for _, c := range r.containers {
		containers = append(containers, c)
	}
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	workers := make([]pool.RuntimeWorker, 0, len(containers))
	for _, c := range containers {
		workers = append(workers, pool.RuntimeWorker{
			ID:          c.WorkerID,
			ContainerID: c.ContainerID,
			Status:      c.Status,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"workers": workers})
}

func (r *fakeRuntimeDaemon) handleRecycle(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req) {
		return
	}
	workerID := req.PathValue("id")
	if strings.TrimSpace(workerID) == "" {
		http.Error(w, "missing worker id", http.StatusBadRequest)
		return
	}
	r.emitEvent(pool.RuntimeEvent{
		Type:     "worker_recycled",
		WorkerID: workerID,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "recycled"})
}

func (r *fakeRuntimeDaemon) handleEvents(w http.ResponseWriter, req *http.Request) {
	if !r.authorize(w, req) {
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

	ch, cancel := r.subscribeEvents()
	defer cancel()

	_, _ = fmt.Fprintf(w, ": runtime events\n\n")
	flusher.Flush()

	for {
		select {
		case <-req.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\n", event.Type)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func (r *fakeRuntimeDaemon) subscribeEvents() (<-chan pool.RuntimeEvent, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eventSubs == nil {
		r.eventSubs = make(map[int]chan pool.RuntimeEvent)
	}
	id := r.eventSeq
	r.eventSeq++
	ch := make(chan pool.RuntimeEvent, 8)
	r.eventSubs[id] = ch
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if sub, ok := r.eventSubs[id]; ok {
			delete(r.eventSubs, id)
			close(sub)
		}
	}
}

func (r *fakeRuntimeDaemon) emitEvent(event pool.RuntimeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.eventSubs {
		select {
		case ch <- event:
		default:
		}
	}
}

func registerAndPollWorkerTask(t *testing.T, k *Kitchen, workerID, containerID string) pool.Task {
	t.Helper()

	token := workerAuthTokenFromKitchen(t, k, workerID)
	brokerURL := kitchenBrokerBaseURL(t, k)
	body, err := json.Marshal(registerReq{
		WorkerID:    workerID,
		ContainerID: containerID,
	})
	if err != nil {
		t.Fatalf("Marshal register payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, brokerURL+"/register", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest register: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST %s status = %d, want 200 or 409", brokerURL+"/register", resp.StatusCode)
	}

	var task pool.Task
	waitFor(t, 2*time.Second, func() bool {
		req, err := http.NewRequest(http.MethodGet, brokerURL+"/task/"+workerID, nil)
		if err != nil {
			t.Fatalf("NewRequest poll: %v", err)
		}
		req.Header.Set("X-Mittens-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("poll task: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return false
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("poll status = %d", resp.StatusCode)
		}
		var pollResp struct {
			Task    *pool.Task `json:"task,omitempty"`
			Recycle bool       `json:"recycle,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&pollResp); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		if pollResp.Recycle || pollResp.Task == nil {
			return false
		}
		task = *pollResp.Task
		return true
	})
	return task
}

func completeWorkerTask(t *testing.T, k *Kitchen, workerID, taskID string) {
	t.Helper()
	token := workerAuthTokenFromKitchen(t, k, workerID)
	brokerURL := kitchenBrokerBaseURL(t, k)
	postBrokerJSON(t, brokerURL+"/complete", token, completeSignal{WorkerID: workerID, TaskID: taskID})
}

func writePlannerArtifactForWorker(t *testing.T, k *Kitchen, workerID string, artifact adapter.PlanArtifact) {
	t.Helper()
	var task pool.Task
	found := false
	for _, item := range k.pm.Tasks() {
		if item.WorkerID == workerID && item.Role == plannerTaskRole && item.Status == pool.TaskDispatched {
			task = item
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no dispatched planner task found for worker %s", workerID)
	}
	raw, err := json.Marshal(testCouncilArtifactForTask(task, artifact))
	if err != nil {
		t.Fatalf("Marshal council artifact: %v", err)
	}
	workerStateDir := pool.WorkerStateDir(k.pm.StateDir(), workerID)
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerPlanFile), raw, 0o644); err != nil {
		t.Fatalf("WriteFile plan artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte("planned\n"), 0o644); err != nil {
		t.Fatalf("WriteFile planner result: %v", err)
	}
}

func writeWorkerResult(t *testing.T, k *Kitchen, workerID, content string) {
	t.Helper()
	workerStateDir := pool.WorkerStateDir(k.pm.StateDir(), workerID)
	if err := os.MkdirAll(workerStateDir, 0o755); err != nil {
		t.Fatalf("MkdirAll worker state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerStateDir, pool.WorkerResultFile), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile worker result: %v", err)
	}
}

func kitchenBrokerBaseURL(t *testing.T, k *Kitchen) string {
	t.Helper()
	return "http://127.0.0.1:" + listenerPort(t, k.workerBkr.ln.Addr().String())
}

func postBrokerJSON(t *testing.T, url, token string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", url, resp.StatusCode)
	}
}

func pollBrokerTaskOnce(t *testing.T, k *Kitchen, workerID string) (int, pollTaskResp) {
	t.Helper()
	token := workerAuthTokenFromKitchen(t, k, workerID)
	req, err := http.NewRequest(http.MethodGet, kitchenBrokerBaseURL(t, k)+"/task/"+workerID, nil)
	if err != nil {
		t.Fatalf("NewRequest poll: %v", err)
	}
	req.Header.Set("X-Mittens-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("poll task: %v", err)
	}
	defer resp.Body.Close()

	var decoded pollTaskResp
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode poll response: %v", err)
		}
	}
	return resp.StatusCode, decoded
}

func waitForBrokerTask(t *testing.T, k *Kitchen, workerID string) pool.Task {
	t.Helper()
	var task pool.Task
	waitFor(t, 2*time.Second, func() bool {
		statusCode, poll := pollBrokerTaskOnce(t, k, workerID)
		if statusCode != http.StatusOK || poll.Recycle || poll.Task == nil {
			return false
		}
		task = *poll.Task
		return true
	})
	return task
}

func waitForSpawnByRole(t *testing.T, runtime *fakeRuntimeDaemon, role string, count int) []fakeRuntimeSpawn {
	t.Helper()
	var matches []fakeRuntimeSpawn
	waitFor(t, 3*time.Second, func() bool {
		all := runtime.Spawns()
		matches = matches[:0]
		for _, spawn := range all {
			if spawn.Role == role {
				matches = append(matches, spawn)
			}
		}
		return len(matches) >= count
	})
	return matches
}

func completePlannerSpawn(t *testing.T, k *Kitchen, runtime *fakeRuntimeDaemon, spawn fakeRuntimeSpawn, artifact adapter.PlanArtifact) {
	t.Helper()
	current := spawn
	handled := map[string]bool{}
	for i := 0; i < 4; i++ {
		task := registerAndPollWorkerTask(t, k, current.ID, current.containerID)
		if task.Role != plannerTaskRole {
			t.Fatalf("planning task role = %q, want %q", task.Role, plannerTaskRole)
		}
		writePlannerArtifactForWorker(t, k, current.ID, artifact)
		completeWorkerTask(t, k, current.ID, task.ID)
		handled[current.ID] = true

		var bundle StoredPlan
		waitFor(t, 3*time.Second, func() bool {
			got, err := k.GetPlan(task.PlanID)
			if err != nil {
				return false
			}
			bundle = got
			return bundle.Execution.CouncilAwaitingAnswers ||
				bundle.Execution.State == planStatePendingApproval ||
				bundle.Execution.State == planStateActive ||
				bundle.Execution.State == planStateRejected ||
				!contains(bundle.Execution.ActiveTaskIDs, task.ID)
		})
		if bundle.Execution.CouncilAwaitingAnswers ||
			bundle.Execution.State == planStatePendingApproval ||
			bundle.Execution.State == planStateActive ||
			bundle.Execution.State == planStateRejected {
			return
		}

		var next fakeRuntimeSpawn
		waitFor(t, 3*time.Second, func() bool {
			for _, candidate := range runtime.Spawns() {
				if candidate.Role == plannerTaskRole && !handled[candidate.ID] {
					next = candidate
					return true
				}
			}
			statusCode, poll := pollBrokerTaskOnce(t, k, current.ID)
			if statusCode == http.StatusOK && !poll.Recycle && poll.Task != nil && poll.Task.Role == plannerTaskRole {
				next = current
				return true
			}
			return false
		})
		current = next
	}
	t.Fatalf("planner council did not converge for spawn %s", spawn.ID)
}
