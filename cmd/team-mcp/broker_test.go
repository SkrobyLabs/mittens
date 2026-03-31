package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

func newTestBroker(t *testing.T) (*WorkerBroker, *pool.PoolManager) {
	t.Helper()
	pm := pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: t.TempDir()}, newNopWAL(t), nil)
	b := NewWorkerBroker(pm, ":0", "test-token")
	return b, pm
}

func newNopWAL(t *testing.T) *pool.WAL {
	t.Helper()
	f := t.TempDir() + "/test.wal"
	w, err := pool.OpenWAL(f)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func doReq(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", "test-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestBrokerRegister(t *testing.T) {
	b, pm := newTestBroker(t)

	// Spawn a worker so it exists in the pool.
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})

	rr := doReq(t, b.srv.Handler, "POST", "/register", registerReq{
		WorkerID:    "w-1",
		ContainerID: "abc123",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("register: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	w, ok := pm.Worker("w-1")
	if !ok {
		t.Fatal("worker not found after register")
	}
	if w.Status != pool.WorkerIdle {
		t.Errorf("worker status = %q, want idle", w.Status)
	}
}

func TestBrokerRegisterNotFound(t *testing.T) {
	b, _ := newTestBroker(t)

	rr := doReq(t, b.srv.Handler, "POST", "/register", registerReq{
		WorkerID: "w-nonexistent",
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("register unknown: got %d, want 404", rr.Code)
	}
}

func TestBrokerHeartbeat(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	rr := doReq(t, b.srv.Handler, "POST", "/heartbeat", heartbeatReq{
		WorkerID: "w-1",
		State:    "alive",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("heartbeat: got %d, want 200", rr.Code)
	}
}

func TestBrokerHeartbeatWithTool(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	rr := doReq(t, b.srv.Handler, "POST", "/heartbeat", heartbeatReq{
		WorkerID:    "w-1",
		State:       "alive",
		CurrentTool: "Edit",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("heartbeat: got %d, want 200", rr.Code)
	}
	w, _ := pm.Worker("w-1")
	if w.CurrentTool != "Edit" {
		t.Errorf("CurrentTool = %q, want Edit", w.CurrentTool)
	}
}

func TestBrokerPollTask_NoTask(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	rr := doReq(t, b.srv.Handler, "GET", "/task/w-1", nil)
	if rr.Code != http.StatusNoContent {
		t.Errorf("poll empty: got %d, want 204", rr.Code)
	}
}

func TestBrokerPollTask_WithTask(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "do stuff", Priority: 10})
	pm.DispatchTask("t-1", "w-1")

	rr := doReq(t, b.srv.Handler, "GET", "/task/w-1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("poll with task: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var task pool.Task
	json.NewDecoder(rr.Body).Decode(&task)
	if task.ID != "t-1" {
		t.Errorf("task ID = %q, want t-1", task.ID)
	}
}

func TestBrokerComplete(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "work", Priority: 1})
	pm.DispatchTask("t-1", "w-1")

	// Write result files to worker dir before signaling completion.
	workerDir := filepath.Join(pm.StateDir(), "workers", "w-1")
	os.MkdirAll(workerDir, 0755)
	os.WriteFile(filepath.Join(workerDir, "result.txt"), []byte("done"), 0644)

	rr := doReq(t, b.srv.Handler, "POST", "/complete", completeSignal{
		WorkerID: "w-1",
		TaskID:   "t-1",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("complete: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	task, _ := pm.Task("t-1")
	if task.Status != pool.TaskCompleted {
		t.Errorf("task status = %q, want completed", task.Status)
	}
}

func TestBrokerFail(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "work", Priority: 1})
	pm.DispatchTask("t-1", "w-1")

	rr := doReq(t, b.srv.Handler, "POST", "/fail", failReq{
		WorkerID: "w-1",
		TaskID:   "t-1",
		Error:    "something broke",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("fail: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	task, _ := pm.Task("t-1")
	if task.Status != pool.TaskFailed {
		t.Errorf("task status = %q, want failed", task.Status)
	}
}

func TestBrokerQuestion(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	rr := doReq(t, b.srv.Handler, "POST", "/question", questionReq{
		WorkerID: "w-1",
		Question: pool.Question{
			Question: "which approach?",
			Blocking: true,
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("question: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp questionResp
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.QuestionID == "" {
		t.Error("expected non-empty questionId")
	}
}

func TestBrokerAnswer_NotAnswered(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	// Ask question via PM directly.
	qid, _ := pm.AskQuestion("w-1", pool.Question{
		Question: "which way?",
		Blocking: true,
	})

	rr := doReq(t, b.srv.Handler, "GET", "/answer/"+qid, nil)
	if rr.Code != http.StatusNoContent {
		t.Errorf("unanswered: got %d, want 204", rr.Code)
	}
}

func TestBrokerAnswer_Answered(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	qid, _ := pm.AskQuestion("w-1", pool.Question{
		Question: "which way?",
		Blocking: true,
	})
	pm.AnswerQuestion(qid, "go left", "leader")

	rr := doReq(t, b.srv.Handler, "GET", "/answer/"+qid, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("answered: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var q pool.Question
	json.NewDecoder(rr.Body).Decode(&q)
	if q.Answer != "go left" {
		t.Errorf("answer = %q, want %q", q.Answer, "go left")
	}
}

func TestBrokerAuth_Rejected(t *testing.T) {
	b, _ := newTestBroker(t)

	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-1"}`))
	req.Header.Set("Content-Type", "application/json")
	// No auth token set.
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no-auth: got %d, want 401", rr.Code)
	}
}

func TestBrokerAuth_EmptyTokenRejectsAll(t *testing.T) {
	pm := pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: t.TempDir()}, newNopWAL(t), nil)
	b := NewWorkerBroker(pm, ":0", "") // empty broker token

	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-1"}`))
	req.Header.Set("Content-Type", "application/json")
	// Request also has empty token — previously this would bypass auth.
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("empty-token bypass: got %d, want 401", rr.Code)
	}
}

func TestBrokerAuth_PerWorkerToken(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	b.RegisterWorkerToken("w-1", "worker-secret-1")

	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-1","state":"alive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", "worker-secret-1")
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("per-worker auth: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestBrokerAuth_PerWorkerTokenIdentityMismatch(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-1", "")
	pm.RegisterWorker("w-2", "")

	b.RegisterWorkerToken("w-1", "worker-secret-1")

	// Worker w-1's token used to impersonate w-2.
	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-2","state":"alive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", "worker-secret-1")
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("identity mismatch: got %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

func TestBrokerAuth_PerWorkerTokenGetTask(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-2"})
	pm.RegisterWorker("w-1", "")

	b.RegisterWorkerToken("w-1", "worker-secret-1")

	// w-1's token trying to poll w-2's tasks via GET /task/{wid}.
	req := httptest.NewRequest("GET", "/task/w-2", nil)
	req.Header.Set("X-Mittens-Token", "worker-secret-1")
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("GET task identity mismatch: got %d, want 403", rr.Code)
	}
}
