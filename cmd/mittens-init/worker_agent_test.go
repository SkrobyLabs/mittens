package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

// mockLeaderServer simulates the WorkerBroker for worker agent testing.
type mockLeaderServer struct {
	mu              sync.Mutex
	registered      bool
	heartbeats      int
	lastActivity    *pool.WorkerActivity
	lastCurrentTool string
	completed       []string
	failed          []string
	failedErrors    []string
	reviewed        []reviewPayload
	task            *pool.Task // task to return on poll (consumed once)
	recycle         bool
	srv             *httptest.Server
}

type reviewPayload struct {
	WorkerID string `json:"workerId"`
	TaskID   string `json:"taskId"`
	Verdict  string `json:"verdict"`
	Feedback string `json:"feedback"`
	Severity string `json:"severity"`
}

func newMockLeaderServer(t *testing.T) *mockLeaderServer {
	t.Helper()
	m := &mockLeaderServer{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.registered = true
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Activity    *pool.WorkerActivity `json:"activity"`
			CurrentTool string               `json:"currentTool"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.heartbeats++
		if req.Activity != nil {
			cp := *req.Activity
			m.lastActivity = &cp
		} else {
			m.lastActivity = nil
		}
		m.lastCurrentTool = req.CurrentTool
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /task/{wid}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		task := m.task
		m.task = nil
		recycle := m.recycle
		m.recycle = false
		m.mu.Unlock()

		if recycle {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"recycle": true})
			return
		}
		if task == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
	})
	mux.HandleFunc("POST /complete", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TaskID string `json:"taskId"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.completed = append(m.completed, req.TaskID)
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /fail", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TaskID string `json:"taskId"`
			Error  string `json:"error"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.failed = append(m.failed, req.TaskID)
		m.failedErrors = append(m.failedErrors, req.Error)
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /report_review", func(w http.ResponseWriter, r *http.Request) {
		var req reviewPayload
		json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.reviewed = append(m.reviewed, req)
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

type fakeAdapter struct {
	result      adapter.Result
	err         error
	results     []adapter.Result
	errs        []error
	prompts     []string
	priorCtxs   []string
	clearCalls  int
	forceCleans int
	calls       int
}

func (a *fakeAdapter) Execute(ctx context.Context, prompt string, priorContext string) (adapter.Result, error) {
	a.prompts = append(a.prompts, prompt)
	a.priorCtxs = append(a.priorCtxs, priorContext)
	idx := a.calls
	a.calls++
	result := a.result
	err := a.err
	if idx < len(a.results) {
		result = a.results[idx]
	}
	if idx < len(a.errs) {
		err = a.errs[idx]
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (a *fakeAdapter) ClearSession() error {
	a.clearCalls++
	return nil
}

func (a *fakeAdapter) ForceClean() error {
	a.forceCleans++
	return nil
}

func (a *fakeAdapter) Healthy() bool {
	return true
}

func TestLeaderClient_Register(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	if err := client.Register("w-1", "container-abc"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !m.registered {
		t.Error("expected registered=true")
	}
}

func TestLeaderClient_Heartbeat(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	if err := client.Heartbeat("w-1", nil, ""); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	m.mu.Lock()
	if m.heartbeats != 1 {
		t.Errorf("heartbeats = %d, want 1", m.heartbeats)
	}
	m.mu.Unlock()
}

func TestLeaderClient_HeartbeatWithActivity(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	activity := &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "README.md",
	}

	if err := client.Heartbeat("w-1", activity, "Read"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	m.mu.Lock()
	if m.lastActivity == nil {
		t.Fatal("expected activity in heartbeat")
	}
	if *m.lastActivity != *activity {
		t.Fatalf("activity = %+v, want %+v", *m.lastActivity, *activity)
	}
	if m.lastCurrentTool != "Read" {
		t.Errorf("currentTool = %q, want Read", m.lastCurrentTool)
	}
	m.mu.Unlock()
}

func TestLeaderClient_HeartbeatLegacyToolOnly(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	if err := client.Heartbeat("w-1", nil, "Read"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	m.mu.Lock()
	if m.lastActivity != nil {
		t.Fatalf("activity = %+v, want nil", *m.lastActivity)
	}
	if m.lastCurrentTool != "Read" {
		t.Errorf("currentTool = %q, want Read", m.lastCurrentTool)
	}
	m.mu.Unlock()
}

func TestLeaderClient_PollTask_Empty(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	poll, err := client.PollTask("w-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if poll.Task != nil || poll.Recycle {
		t.Errorf("expected empty poll result, got %+v", poll)
	}
}

func TestLeaderClient_PollTask_WithTask(t *testing.T) {
	m := newMockLeaderServer(t)
	m.task = &pool.Task{
		ID:     "t-1",
		Prompt: "do stuff",
		Status: pool.TaskDispatched,
	}
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	poll, err := client.PollTask("w-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if poll.Recycle {
		t.Fatal("expected task poll, got recycle")
	}
	task := poll.Task
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.ID != "t-1" {
		t.Errorf("task.ID = %q, want t-1", task.ID)
	}
}

func TestLeaderClient_PollTask_Recycle(t *testing.T) {
	m := newMockLeaderServer(t)
	m.recycle = true
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	poll, err := client.PollTask("w-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !poll.Recycle {
		t.Fatalf("poll = %+v, want recycle=true", poll)
	}
	if poll.Task != nil {
		t.Fatalf("poll task = %+v, want nil", poll.Task)
	}
}

func TestWorkerRuntimeDescriptor(t *testing.T) {
	got := workerRuntimeDescriptor("codex", "gpt-5.4", "codex")
	want := "provider=codex model=gpt-5.4 adapter=codex"
	if got != want {
		t.Fatalf("workerRuntimeDescriptor() = %q, want %q", got, want)
	}

	got = workerRuntimeDescriptor("", "", "")
	want = "provider=default model=default adapter=default"
	if got != want {
		t.Fatalf("workerRuntimeDescriptor(empty) = %q, want %q", got, want)
	}
}

func TestLeaderClient_ReportComplete(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	err := client.ReportComplete("w-1", "t-1")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	m.mu.Lock()
	if len(m.completed) != 1 || m.completed[0] != "t-1" {
		t.Errorf("completed = %v", m.completed)
	}
	m.mu.Unlock()
}

func TestLeaderClient_ReportFail(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	err := client.ReportFail("w-1", "t-1", "broke")
	if err != nil {
		t.Fatalf("fail: %v", err)
	}

	m.mu.Lock()
	if len(m.failed) != 1 || m.failed[0] != "t-1" {
		t.Errorf("failed = %v", m.failed)
	}
	if len(m.failedErrors) != 1 || m.failedErrors[0] != "broke" {
		t.Errorf("failedErrors = %v, want [broke]", m.failedErrors)
	}
	m.mu.Unlock()
}

func TestLeaderClient_ReportFailSanitizesLargeErrors(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	longErr := "line1\n\n" + strings.Repeat("x", 700)
	if err := client.ReportFail("w-1", "t-1", longErr); err != nil {
		t.Fatalf("fail: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if got := len([]rune(m.failedErrors[0])); got > maxFailureMessageLen {
		t.Fatalf("sanitized error length = %d, want <= %d", got, maxFailureMessageLen)
	}
	if strings.Contains(m.failedErrors[0], "\n") {
		t.Fatalf("sanitized error should not contain newlines: %q", m.failedErrors[0])
	}
}

// --- Registration retry tests ---

func TestRegisterWithRetries_Success(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	err := registerWithRetries(client, "w-1", "ctr-1")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !m.registered {
		t.Error("expected registered=true")
	}
}

func TestRegisterWithRetries_FailsAfterExhaustion(t *testing.T) {
	// Create a server that always returns 500.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")

	err := registerWithRetries(client, "w-1", "ctr-1")
	if err == nil {
		t.Error("expected error after all retries fail")
	}
}

// --- heartbeatLoop killed detection ---

func TestHeartbeatLoop_KilledCancelsContext(t *testing.T) {
	// Server returns 401 on heartbeat (= worker token revoked / worker killed).
	mux := http.NewServeMux()
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	state := &workerAgentState{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run heartbeat loop in a goroutine; it should call cancel() on 404.
	done := make(chan struct{})
	go func() {
		heartbeatLoop(ctx, cancel, client, "w-1", state)
		close(done)
	}()

	// Wait for the context to be cancelled (heartbeat returns 401 → errWorkerKilled → cancel).
	select {
	case <-ctx.Done():
		// Good — context was cancelled.
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for heartbeat to cancel context")
	}

	// Wait for the goroutine to complete.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat goroutine did not exit")
	}
}

// --- File I/O tests ---

func TestWriteTeamFileAtomic(t *testing.T) {
	dir := t.TempDir()
	writeTeamFileAtomic(dir, "result.txt", []byte("test output"))

	data, err := os.ReadFile(filepath.Join(dir, "result.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "test output" {
		t.Errorf("content = %q, want 'test output'", data)
	}

	// Verify .tmp is cleaned up.
	if _, err := os.Stat(filepath.Join(dir, "result.txt.tmp")); !os.IsNotExist(err) {
		t.Error("expected .tmp file to be removed after atomic rename")
	}
}

func TestWriteTeamFileAtomic_EmptyDir(t *testing.T) {
	// Should be a no-op with empty teamDir.
	writeTeamFileAtomic("", "result.txt", []byte("data"))
}

func TestCleanTeamDir(t *testing.T) {
	dir := t.TempDir()

	// Write files that should be cleaned.
	for _, name := range []string{"task.md", "result.txt", "plan.json", "handover.json", "error.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644)
	}
	// Write a file that should NOT be cleaned.
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("keep"), 0644)

	cleanTeamDir(dir)

	for _, name := range []string{"task.md", "result.txt", "plan.json", "handover.json", "error.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Error("other.txt should not have been removed")
	}
}

func TestCleanTeamDir_PreservesActivityLogs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{pool.WorkerActivityLogFile, pool.WorkerActivityArchiveFile} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cleanTeamDir(dir)

	for _, name := range []string{pool.WorkerActivityLogFile, pool.WorkerActivityArchiveFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s to remain: %v", name, err)
		}
	}
}

func TestCleanTeamDir_EmptyDir(t *testing.T) {
	// Should be a no-op with empty string.
	cleanTeamDir("")
}

func TestWriteTaskFile(t *testing.T) {
	dir := t.TempDir()
	task := &pool.Task{
		ID:       "t-42",
		Role:     "implementer",
		Priority: 2,
		Prompt:   "implement feature X",
	}

	writeTaskFile(dir, task, "prior context here")

	data, err := os.ReadFile(filepath.Join(dir, "task.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "t-42") {
		t.Error("missing taskId in task.md")
	}
	if !strings.Contains(content, "implementer") {
		t.Error("missing role in task.md")
	}
	if !strings.Contains(content, "implement feature X") {
		t.Error("missing prompt in task.md")
	}
	if !strings.Contains(content, "prior context here") {
		t.Error("missing prior context in task.md")
	}
}

func TestWriteTaskFile_NoPriorContext(t *testing.T) {
	dir := t.TempDir()
	task := &pool.Task{
		ID:     "t-1",
		Prompt: "do stuff",
	}

	writeTaskFile(dir, task, "")

	data, err := os.ReadFile(filepath.Join(dir, "task.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "Prior Context") {
		t.Error("should not contain Prior Context section when empty")
	}
}

func TestWriteTaskFile_EmptyDir(t *testing.T) {
	// Should be a no-op.
	writeTaskFile("", &pool.Task{ID: "t-1", Prompt: "x"}, "ctx")
}

func TestWriteTeamFileAtomic_HandoverJSON(t *testing.T) {
	dir := t.TempDir()

	handover := pool.TaskHandover{
		TaskID:         "t-1",
		Summary:        "did things",
		KeyDecisions:   []string{"used Go", "chose postgres"},
		ContextForNext: "deploy next",
	}
	data, _ := json.Marshal(handover)
	writeTeamFileAtomic(dir, "handover.json", data)

	raw, err := os.ReadFile(filepath.Join(dir, "handover.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got pool.TaskHandover
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TaskID != "t-1" {
		t.Errorf("taskId = %q, want t-1", got.TaskID)
	}
	if got.ContextForNext != "deploy next" {
		t.Errorf("contextForNext = %q, want 'deploy next'", got.ContextForNext)
	}
}

// --- workerAgentState tests ---

func TestWorkerAgentState_Concurrency(t *testing.T) {
	state := &workerAgentState{}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			state.setTool("Read", "README.md")
			state.setTool("Write", "main.go")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = state.snapshot()
		}
	}()

	wg.Wait()
	activity, tool := state.snapshot()
	if tool != "Read" && tool != "Write" && tool != "" {
		t.Errorf("unexpected tool: %q", tool)
	}
	if activity != nil && activity.Kind != "tool" {
		t.Errorf("unexpected activity kind: %q", activity.Kind)
	}
}

func TestWorkerAgentState_StatusActivityClearsLegacyTool(t *testing.T) {
	state := &workerAgentState{}
	state.setTool("Read", "README.md")
	state.setActivity(&pool.WorkerActivity{
		Kind:    "status",
		Phase:   "started",
		Name:    "planning",
		Summary: "Reviewing repository state",
	})

	activity, tool := state.snapshot()
	if activity == nil {
		t.Fatal("expected current activity")
	}
	if activity.Kind != "status" || activity.Name != "planning" {
		t.Fatalf("activity = %+v, want status planning", *activity)
	}
	if tool != "" {
		t.Errorf("tool = %q, want empty", tool)
	}
}

func TestWorkerAgentState_CompletedToolDoesNotPopulateCurrentTool(t *testing.T) {
	state := &workerAgentState{}
	state.setTool("Read", "README.md")
	completed := &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "completed",
		Name:    "Read",
		Summary: "finished README.md",
	}

	state.setActivity(completed)

	activity, tool := state.snapshot()
	if activity == nil {
		t.Fatal("expected current activity")
	}
	if *activity != *completed {
		t.Fatalf("activity = %+v, want %+v", *activity, *completed)
	}
	if tool != "" {
		t.Errorf("tool = %q, want empty", tool)
	}
}

func TestWorkerAgentState_PersistsActivityHistory(t *testing.T) {
	dir := t.TempDir()
	state := &workerAgentState{teamDir: dir}
	state.setCurrentTask("t-1")
	activity := &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "README.md",
	}

	state.setActivity(activity)

	records := readActivityRecords(t, filepath.Join(dir, pool.WorkerActivityLogFile))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].TaskID != "t-1" {
		t.Fatalf("taskId = %q, want t-1", records[0].TaskID)
	}
	if records[0].Activity != *activity {
		t.Fatalf("activity = %+v, want %+v", records[0].Activity, *activity)
	}
	if records[0].RecordedAt.IsZero() {
		t.Fatal("recordedAt should be set")
	}
}

func TestWorkerAgentState_DeduplicatesIdenticalActivity(t *testing.T) {
	dir := t.TempDir()
	state := &workerAgentState{teamDir: dir}
	state.setCurrentTask("t-1")
	activity := &pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    "Read",
		Summary: "README.md",
	}

	state.setActivity(activity)
	state.setActivity(activity)
	state.setTool("Read", "README.md")

	records := readActivityRecords(t, filepath.Join(dir, pool.WorkerActivityLogFile))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
}

func TestWorkerAgentState_RotatesActivityHistory(t *testing.T) {
	dir := t.TempDir()
	state := &workerAgentState{teamDir: dir}
	state.setCurrentTask("t-rotate")

	for i := 0; i < pool.WorkerActivityLogMaxEntries+1; i++ {
		state.setActivity(&pool.WorkerActivity{
			Kind:    "tool",
			Phase:   "started",
			Name:    fmt.Sprintf("Tool-%03d", i),
			Summary: "rotating history",
		})
	}

	current := readActivityRecords(t, filepath.Join(dir, pool.WorkerActivityLogFile))
	archive := readActivityRecords(t, filepath.Join(dir, pool.WorkerActivityArchiveFile))
	if len(archive) != pool.WorkerActivityLogMaxEntries {
		t.Fatalf("archive records = %d, want %d", len(archive), pool.WorkerActivityLogMaxEntries)
	}
	if len(current) != 1 {
		t.Fatalf("current records = %d, want 1", len(current))
	}
	if current[0].Activity.Name != fmt.Sprintf("Tool-%03d", pool.WorkerActivityLogMaxEntries) {
		t.Fatalf("current last tool = %q", current[0].Activity.Name)
	}
}

// --- PollTask killed detection ---

func TestLeaderClient_PollTask_Killed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /task/{wid}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	_, err := client.PollTask("w-1")
	if err != errWorkerKilled {
		t.Errorf("expected errWorkerKilled, got %v", err)
	}
}

func TestTaskLoop_RecycleForcesCleanAndContinuesPolling(t *testing.T) {
	var polls int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /task/{wid}", func(w http.ResponseWriter, r *http.Request) {
		polls++
		switch polls {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"recycle": true})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{}
	state := &workerAgentState{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	taskLoop(ctx, client, ad, "w-1", state)

	if ad.forceCleans != 1 {
		t.Fatalf("forceCleans = %d, want 1", ad.forceCleans)
	}
	if polls < 2 {
		t.Fatalf("polls = %d, want at least 2", polls)
	}
}

func TestLeaderClient_Heartbeat_Killed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	err := client.Heartbeat("w-1", nil, "")
	if err != errWorkerKilled {
		t.Errorf("expected errWorkerKilled, got %v", err)
	}
}

func TestLeaderClient_AskQuestion_Killed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /question", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	_, err := client.AskQuestion("w-1", pool.Question{Question: "still there?", Blocking: true})
	if err != errWorkerKilled {
		t.Errorf("expected errWorkerKilled, got %v", err)
	}
}

// --- Report retry tests ---

func TestReportCompleteWithRetries_Success(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	reportCompleteWithRetries(client, "w-1", "t-1")

	m.mu.Lock()
	if len(m.completed) != 1 || m.completed[0] != "t-1" {
		t.Errorf("completed = %v, want [t-1]", m.completed)
	}
	m.mu.Unlock()
}

func TestReportFailWithRetries_Success(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")

	reportFailWithRetries(client, "w-1", "t-1", "boom")

	m.mu.Lock()
	if len(m.failed) != 1 || m.failed[0] != "t-1" {
		t.Errorf("failed = %v, want [t-1]", m.failed)
	}
	m.mu.Unlock()
}

// --- ReportReview tests ---

func TestReportReview_Success(t *testing.T) {
	var gotPayload struct {
		WorkerID string `json:"workerId"`
		TaskID   string `json:"taskId"`
		Verdict  string `json:"verdict"`
		Feedback string `json:"feedback"`
		Severity string `json:"severity"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /report_review", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	err := client.ReportReview("w-1", "t-1", "approved", "looks good", "low")
	if err != nil {
		t.Fatalf("ReportReview: %v", err)
	}
	if gotPayload.WorkerID != "w-1" {
		t.Errorf("workerId = %q, want w-1", gotPayload.WorkerID)
	}
	if gotPayload.TaskID != "t-1" {
		t.Errorf("taskId = %q, want t-1", gotPayload.TaskID)
	}
	if gotPayload.Verdict != "approved" {
		t.Errorf("verdict = %q, want approved", gotPayload.Verdict)
	}
	if gotPayload.Feedback != "looks good" {
		t.Errorf("feedback = %q, want 'looks good'", gotPayload.Feedback)
	}
	if gotPayload.Severity != "low" {
		t.Errorf("severity = %q, want low", gotPayload.Severity)
	}
}

func TestReportReview_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /report_review", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newKitchenClient(srv.Listener.Addr().String(), "")
	err := client.ReportReview("w-1", "t-1", "rejected", "bad code", "high")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention HTTP 500", err.Error())
	}
}

func TestExecuteTask_ReviewTaskReportsReview(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		results: []adapter.Result{
			{Output: "Review complete but forgot the structured block."},
			{Output: "Review complete.\n<review><verdict>pass</verdict><feedback>LGTM</feedback><severity>minor</severity></review>"},
		},
	}
	task := &pool.Task{
		ID:     "t-review",
		Prompt: "review this change",
		Status: pool.TaskReviewing,
		Result: &pool.TaskResult{Summary: "implemented feature"},
		Handover: &pool.TaskHandover{
			ContextForNext: "prior notes",
		},
	}
	state := &workerAgentState{teamDir: t.TempDir()}

	executeTask(client, ad, "w-1", task, state)

	if len(ad.prompts) != 2 {
		t.Fatalf("execute count = %d, want 2", len(ad.prompts))
	}
	if !strings.Contains(ad.prompts[0], "## Review Request") {
		t.Errorf("prompt = %q, want review framing", ad.prompts[0])
	}
	if len(ad.priorCtxs) != 2 || ad.priorCtxs[0] != "" || ad.priorCtxs[1] != "" {
		t.Errorf("priorContexts = %v, want empty contexts", ad.priorCtxs)
	}
	if !strings.Contains(ad.prompts[1], "## Parse Error") {
		t.Fatalf("retry prompt = %q, want parse error context", ad.prompts[1])
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.completed) != 0 {
		t.Errorf("completed = %v, want none", m.completed)
	}
	if len(m.reviewed) != 1 {
		t.Fatalf("reviewed = %d, want 1", len(m.reviewed))
	}
	if m.reviewed[0].TaskID != "t-review" || m.reviewed[0].Verdict != "pass" {
		t.Errorf("review payload = %+v", m.reviewed[0])
	}
}

func TestExecuteTask_ReviewTaskMissingVerdictFailsAfterRetries(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		results: []adapter.Result{
			{Output: "review text without structured verdict"},
			{Output: "still no verdict"},
			{Output: "final attempt still missing the review block"},
		},
	}
	task := &pool.Task{
		ID:     "t-review",
		Prompt: "review this change",
		Status: pool.TaskReviewing,
		Result: &pool.TaskResult{Summary: "implemented feature"},
	}

	executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: t.TempDir()})

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.reviewed) != 0 {
		t.Fatalf("reviewed = %d, want 0", len(m.reviewed))
	}
	if len(m.failed) != 1 || m.failed[0] != "t-review" {
		t.Fatalf("failed = %v, want [t-review]", m.failed)
	}
	if len(m.failedErrors) != 1 || !strings.Contains(m.failedErrors[0], "invalid review verdict (after 3 attempts)") {
		t.Fatalf("failedErrors = %v, want invalid review verdict retry message", m.failedErrors)
	}
	if len(ad.prompts) != 3 {
		t.Fatalf("execute count = %d, want 3", len(ad.prompts))
	}
}

func TestExecuteTask_RegularTaskStillReportsComplete(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		result: adapter.Result{Output: "done\n<handover><summary>ok</summary></handover>"},
	}
	task := &pool.Task{
		ID:     "t-regular",
		Prompt: "implement change",
		Status: pool.TaskDispatched,
	}

	executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: t.TempDir()})

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.completed) != 1 || m.completed[0] != "t-regular" {
		t.Errorf("completed = %v, want [t-regular]", m.completed)
	}
	if len(m.reviewed) != 0 {
		t.Errorf("reviewed = %v, want none", m.reviewed)
	}
}

func TestExecuteTask_GenericPlannerTaskStillReportsComplete(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		result: adapter.Result{Output: "generic planner summary\n<handover><summary>ok</summary></handover>"},
	}
	task := &pool.Task{
		ID:     "t-generic-plan",
		Role:   "planner",
		Prompt: "Create a design summary for this feature.",
		Status: pool.TaskDispatched,
	}
	teamDir := t.TempDir()

	executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: teamDir})

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.completed) != 1 || m.completed[0] != "t-generic-plan" {
		t.Fatalf("completed = %v, want [t-generic-plan]", m.completed)
	}
	if len(m.failed) != 0 {
		t.Fatalf("failed = %v, want none", m.failed)
	}
	if _, err := os.Stat(filepath.Join(teamDir, pool.WorkerPlanFile)); !os.IsNotExist(err) {
		t.Fatalf("plan artifact should not be written for non-council planner task, stat err = %v", err)
	}
	if len(ad.prompts) != 1 {
		t.Fatalf("execute count = %d, want 1", len(ad.prompts))
	}
}

func TestExecuteTask_PlannerTaskWritesCouncilArtifactAndCompletes(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		result: adapter.Result{
			Output: `Planning complete.
<council_turn>{"seat":"A","turn":1,"stance":"propose","candidatePlan":{"title":"Typed parser errors","tasks":[{"id":"t1","title":"Add typed errors","prompt":"Implement typed parser errors.","complexity":"medium","reviewComplexity":"medium"}]}}</council_turn>
<handover><summary>Drafted the implementation plan.</summary></handover>`,
		},
	}
	task := &pool.Task{
		ID:     "t-plan",
		Role:   "planner",
		Prompt: adapter.BuildCouncilTurnPrompt("Plan this change", nil, "A", 1, ""),
		Status: pool.TaskDispatched,
	}
	teamDir := t.TempDir()

	executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: teamDir})

	if len(ad.prompts) != 1 {
		t.Fatalf("execute count = %d, want 1", len(ad.prompts))
	}
	if !strings.Contains(ad.prompts[0], "## Planner Council Turn") {
		t.Fatalf("prompt = %q, want council planning framing", ad.prompts[0])
	}

	raw, err := os.ReadFile(filepath.Join(teamDir, pool.WorkerPlanFile))
	if err != nil {
		t.Fatalf("read plan artifact: %v", err)
	}
	var artifact adapter.CouncilTurnArtifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("unmarshal council artifact: %v", err)
	}
	if artifact.CandidatePlan == nil || artifact.CandidatePlan.Title != "Typed parser errors" {
		t.Fatalf("candidate plan = %+v, want Typed parser errors", artifact.CandidatePlan)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.completed) != 1 || m.completed[0] != "t-plan" {
		t.Fatalf("completed = %v, want [t-plan]", m.completed)
	}
	if len(m.failed) != 0 {
		t.Fatalf("failed = %v, want none", m.failed)
	}
}

func TestExecuteTask_PlannerTaskMissingArtifactReportsFail(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		results: []adapter.Result{
			{Output: "Planning complete but forgot the structured block."},
			{Output: "Still missing the council turn block."},
			{Output: "Third attempt still malformed."},
		},
	}
	task := &pool.Task{
		ID:     "t-plan",
		Role:   "planner",
		Prompt: adapter.BuildCouncilTurnPrompt("Plan this change", nil, "A", 1, ""),
		Status: pool.TaskDispatched,
	}
	teamDir := t.TempDir()

	stopWorker := executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: teamDir})
	if stopWorker {
		t.Fatal("stopWorker = true, want false")
	}
	if _, err := os.Stat(filepath.Join(teamDir, pool.WorkerPlanFile)); !os.IsNotExist(err) {
		t.Fatalf("plan artifact file should not exist, stat err = %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.completed) != 0 {
		t.Fatalf("completed = %v, want none", m.completed)
	}
	if len(m.failed) != 1 || m.failed[0] != "t-plan" {
		t.Fatalf("failed = %v, want [t-plan]", m.failed)
	}
	if len(m.failedErrors) != 1 || !strings.Contains(m.failedErrors[0], "invalid plan artifact (after 3 attempts)") {
		t.Fatalf("failedErrors = %v, want planner artifact retry error", m.failedErrors)
	}
	if len(ad.prompts) != 3 {
		t.Fatalf("execute count = %d, want 3", len(ad.prompts))
	}
}

func TestExecuteTask_ReviewTaskErrorReportsFail(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{err: errors.New("boom")}
	task := &pool.Task{
		ID:     "t-review",
		Prompt: "review this change",
		Status: pool.TaskReviewing,
	}

	stopWorker := executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: t.TempDir()})

	m.mu.Lock()
	defer m.mu.Unlock()
	if stopWorker {
		t.Fatal("stopWorker = true, want false")
	}
	if len(m.failed) != 1 || m.failed[0] != "t-review" {
		t.Errorf("failed = %v, want [t-review]", m.failed)
	}
	if len(m.reviewed) != 0 {
		t.Errorf("reviewed = %v, want none", m.reviewed)
	}
}

func TestExecuteTask_AuthErrorStopsWorkerAndReportsResetMessage(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newKitchenClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{err: errors.New("execute codex (exit 1): refresh_token_reused\nPlease log out and sign in again.")}
	task := &pool.Task{
		ID:     "t-auth",
		Prompt: "plan this change",
		Status: pool.TaskDispatched,
	}

	stopWorker := executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: t.TempDir()})

	m.mu.Lock()
	defer m.mu.Unlock()
	if !stopWorker {
		t.Fatal("stopWorker = false, want true")
	}
	if len(m.failed) != 1 || m.failed[0] != "t-auth" {
		t.Fatalf("failed = %v, want [t-auth]", m.failed)
	}
	if len(m.failedErrors) != 1 || !strings.Contains(m.failedErrors[0], "run codex logout") {
		t.Fatalf("failedErrors = %v, want host logout guidance", m.failedErrors)
	}
}

func readActivityRecords(t *testing.T, path string) []pool.WorkerActivityRecord {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var records []pool.WorkerActivityRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var record pool.WorkerActivityRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return records
}
