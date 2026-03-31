package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/internal/adapter"
	"github.com/SkrobyLabs/mittens/internal/pool"
)

// mockLeaderServer simulates the WorkerBroker for worker agent testing.
type mockLeaderServer struct {
	mu              sync.Mutex
	registered      bool
	heartbeats      int
	lastCurrentTool string
	completed       []string
	failed          []string
	reviewed        []reviewPayload
	task            *pool.Task // task to return on poll (consumed once)
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
			CurrentTool string `json:"currentTool"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.heartbeats++
		m.lastCurrentTool = req.CurrentTool
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /task/{wid}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		task := m.task
		m.task = nil
		m.mu.Unlock()

		if task == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task)
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
		}
		json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.failed = append(m.failed, req.TaskID)
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
	prompts     []string
	priorCtxs   []string
	clearCalls  int
	forceCleans int
}

func (a *fakeAdapter) Execute(ctx context.Context, prompt string, priorContext string) (adapter.Result, error) {
	a.prompts = append(a.prompts, prompt)
	a.priorCtxs = append(a.priorCtxs, priorContext)
	if a.err != nil {
		return adapter.Result{}, a.err
	}
	return a.result, nil
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
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	if err := client.Register("w-1", "container-abc"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if !m.registered {
		t.Error("expected registered=true")
	}
}

func TestLeaderClient_Heartbeat(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	if err := client.Heartbeat("w-1", ""); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	m.mu.Lock()
	if m.heartbeats != 1 {
		t.Errorf("heartbeats = %d, want 1", m.heartbeats)
	}
	m.mu.Unlock()
}

func TestLeaderClient_HeartbeatWithTool(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	if err := client.Heartbeat("w-1", "Read"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	m.mu.Lock()
	if m.lastCurrentTool != "Read" {
		t.Errorf("currentTool = %q, want Read", m.lastCurrentTool)
	}
	m.mu.Unlock()
}

func TestLeaderClient_PollTask_Empty(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	task, err := client.PollTask("w-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if task != nil {
		t.Errorf("expected nil task, got %+v", task)
	}
}

func TestLeaderClient_PollTask_WithTask(t *testing.T) {
	m := newMockLeaderServer(t)
	m.task = &pool.Task{
		ID:     "t-1",
		Prompt: "do stuff",
		Status: pool.TaskDispatched,
	}
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	task, err := client.PollTask("w-1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.ID != "t-1" {
		t.Errorf("task.ID = %q, want t-1", task.ID)
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
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

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
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	err := client.ReportFail("w-1", "t-1", "broke")
	if err != nil {
		t.Fatalf("fail: %v", err)
	}

	m.mu.Lock()
	if len(m.failed) != 1 || m.failed[0] != "t-1" {
		t.Errorf("failed = %v", m.failed)
	}
	m.mu.Unlock()
}

// --- Registration retry tests ---

func TestRegisterWithRetries_Success(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

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

	client := newLeaderClient(srv.Listener.Addr().String(), "")

	err := registerWithRetries(client, "w-1", "ctr-1")
	if err == nil {
		t.Error("expected error after all retries fail")
	}
}

// --- heartbeatLoop killed detection ---

func TestHeartbeatLoop_KilledCancelsContext(t *testing.T) {
	// Server returns 404 on heartbeat (= worker killed).
	mux := http.NewServeMux()
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newLeaderClient(srv.Listener.Addr().String(), "")
	state := &workerAgentState{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run heartbeat loop in a goroutine; it should call cancel() on 404.
	done := make(chan struct{})
	go func() {
		heartbeatLoop(ctx, cancel, client, "w-1", state)
		close(done)
	}()

	// Wait for the context to be cancelled (heartbeat returns 404 → errWorkerKilled → cancel).
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
	for _, name := range []string{"task.md", "result.txt", "handover.json", "error.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644)
	}
	// Write a file that should NOT be cleaned.
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("keep"), 0644)

	cleanTeamDir(dir)

	for _, name := range []string{"task.md", "result.txt", "handover.json", "error.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Error("other.txt should not have been removed")
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
			state.setTool("Read")
			state.setTool("Write")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = state.getTool()
		}
	}()

	wg.Wait()
	tool := state.getTool()
	if tool != "Read" && tool != "Write" && tool != "" {
		t.Errorf("unexpected tool: %q", tool)
	}
}

// --- PollTask killed detection ---

func TestLeaderClient_PollTask_Killed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /task/{wid}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newLeaderClient(srv.Listener.Addr().String(), "")
	_, err := client.PollTask("w-1")
	if err != errWorkerKilled {
		t.Errorf("expected errWorkerKilled, got %v", err)
	}
}

func TestLeaderClient_Heartbeat_Killed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := newLeaderClient(srv.Listener.Addr().String(), "")
	err := client.Heartbeat("w-1", "")
	if err != errWorkerKilled {
		t.Errorf("expected errWorkerKilled, got %v", err)
	}
}

// --- Report retry tests ---

func TestReportCompleteWithRetries_Success(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

	reportCompleteWithRetries(client, "w-1", "t-1")

	m.mu.Lock()
	if len(m.completed) != 1 || m.completed[0] != "t-1" {
		t.Errorf("completed = %v, want [t-1]", m.completed)
	}
	m.mu.Unlock()
}

func TestReportFailWithRetries_Success(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")

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

	client := newLeaderClient(srv.Listener.Addr().String(), "")
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

	client := newLeaderClient(srv.Listener.Addr().String(), "")
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
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		result: adapter.Result{
			Output: "Review complete.\n<review><verdict>pass</verdict><feedback>LGTM</feedback><severity>minor</severity></review>",
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

	if len(ad.prompts) != 1 {
		t.Fatalf("execute count = %d, want 1", len(ad.prompts))
	}
	if !strings.Contains(ad.prompts[0], "## Review Request") {
		t.Errorf("prompt = %q, want review framing", ad.prompts[0])
	}
	if len(ad.priorCtxs) != 1 || ad.priorCtxs[0] != "" {
		t.Errorf("priorContext = %q, want empty", ad.priorCtxs[0])
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

func TestExecuteTask_ReviewTaskMissingVerdictDefaultsToFail(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{
		result: adapter.Result{Output: "review text without structured verdict"},
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
	if len(m.reviewed) != 1 {
		t.Fatalf("reviewed = %d, want 1", len(m.reviewed))
	}
	if m.reviewed[0].Verdict != pool.ReviewFail {
		t.Errorf("verdict = %q, want fail", m.reviewed[0].Verdict)
	}
	if m.reviewed[0].Severity != pool.SeverityMajor {
		t.Errorf("severity = %q, want %q", m.reviewed[0].Severity, pool.SeverityMajor)
	}
}

func TestExecuteTask_RegularTaskStillReportsComplete(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")
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

func TestExecuteTask_ReviewTaskErrorReportsFail(t *testing.T) {
	m := newMockLeaderServer(t)
	client := newLeaderClient(m.srv.Listener.Addr().String(), "")
	ad := &fakeAdapter{err: errors.New("boom")}
	task := &pool.Task{
		ID:     "t-review",
		Prompt: "review this change",
		Status: pool.TaskReviewing,
	}

	executeTask(client, ad, "w-1", task, &workerAgentState{teamDir: t.TempDir()})

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.failed) != 1 || m.failed[0] != "t-review" {
		t.Errorf("failed = %v, want [t-review]", m.failed)
	}
	if len(m.reviewed) != 0 {
		t.Errorf("reviewed = %v, want none", m.reviewed)
	}
}
