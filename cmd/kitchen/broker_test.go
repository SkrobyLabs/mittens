package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
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

func workerAuthToken(t *testing.T, pm *pool.PoolManager, workerID string) string {
	t.Helper()
	w, ok := pm.Worker(workerID)
	if !ok {
		t.Fatalf("worker %q not found", workerID)
	}
	if w.Token == "" {
		t.Fatalf("worker %q has no auth token", workerID)
	}
	return w.Token
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
	if w.CurrentActivity == nil {
		t.Fatal("CurrentActivity should be synthesized from currentTool")
	}
	if w.CurrentActivity.Kind != "tool" || w.CurrentActivity.Phase != "started" || w.CurrentActivity.Name != "Edit" {
		t.Fatalf("CurrentActivity = %+v, want synthesized tool activity", *w.CurrentActivity)
	}
}

func TestBrokerHeartbeatWithActivity(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")

	rr := doReq(t, b.srv.Handler, "POST", "/heartbeat", heartbeatReq{
		WorkerID: "w-1",
		State:    "alive",
		Activity: &pool.WorkerActivity{
			Kind:    "status",
			Phase:   "started",
			Name:    "planning",
			Summary: "Inspecting repository state",
		},
	})
	if rr.Code != http.StatusOK {
		t.Errorf("heartbeat: got %d, want 200", rr.Code)
	}
	w, _ := pm.Worker("w-1")
	if w.CurrentActivity == nil {
		t.Fatal("CurrentActivity should be set")
	}
	if w.CurrentActivity.Kind != "status" || w.CurrentActivity.Name != "planning" {
		t.Fatalf("CurrentActivity = %+v, want status planning", *w.CurrentActivity)
	}
	if w.CurrentTool != "" {
		t.Errorf("CurrentTool = %q, want empty", w.CurrentTool)
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

	var resp pollTaskResp
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Task == nil || resp.Task.ID != "t-1" {
		t.Errorf("task = %+v, want t-1", resp.Task)
	}
}

func TestBrokerPollTask_Recycle(t *testing.T) {
	b, pm := newTestBroker(t)
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := pm.RequestRecycle("w-1"); err != nil {
		t.Fatalf("RequestRecycle: %v", err)
	}

	rr := doReq(t, b.srv.Handler, "GET", "/task/w-1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("poll with recycle: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp pollTaskResp
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if !resp.Recycle || resp.Task != nil {
		t.Fatalf("poll response = %+v, want recycle=true and no task", resp)
	}
	if pm.RecycleRequested("w-1") {
		t.Fatal("recycle flag should be cleared after broker poll")
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

func TestBrokerReportReview(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "impl-1"})
	pm.SpawnWorker(pool.WorkerSpec{ID: "rev-1", Role: "reviewer"})
	pm.RegisterWorker("impl-1", "")
	pm.RegisterWorker("rev-1", "")

	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "work", Priority: 1, MaxReviews: 3})
	pm.DispatchTask("t-1", "impl-1")
	workerDir := filepath.Join(pm.StateDir(), "workers", "impl-1")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "result.txt"), []byte("done"), 0644); err != nil {
		t.Fatalf("write result.txt: %v", err)
	}
	if err := pm.CompleteTask("impl-1", "t-1"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if err := pm.DispatchReview("t-1", "rev-1"); err != nil {
		t.Fatalf("dispatch review: %v", err)
	}

	rr := doReq(t, b.srv.Handler, "POST", "/report_review", reviewReportReq{
		WorkerID: "rev-1",
		TaskID:   "t-1",
		Verdict:  pool.ReviewPass,
		Feedback: "looks good",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("report review: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	task, _ := pm.Task("t-1")
	if task.Status != pool.TaskAccepted {
		t.Errorf("task status = %q, want accepted", task.Status)
	}
}

func TestBrokerReportReview_MissingFields(t *testing.T) {
	b, _ := newTestBroker(t)

	rr := doReq(t, b.srv.Handler, "POST", "/report_review", reviewReportReq{
		WorkerID: "rev-1",
		TaskID:   "t-1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("report review missing fields: got %d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestBrokerFail_ReviewTaskAbortsReview(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "impl-1"})
	pm.SpawnWorker(pool.WorkerSpec{ID: "rev-1", Role: "reviewer"})
	pm.RegisterWorker("impl-1", "")
	pm.RegisterWorker("rev-1", "")

	pm.EnqueueTask(pool.TaskSpec{ID: "t-1", Prompt: "work", Priority: 1, MaxReviews: 3})
	pm.DispatchTask("t-1", "impl-1")
	workerDir := filepath.Join(pm.StateDir(), "workers", "impl-1")
	if err := os.MkdirAll(workerDir, 0755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "result.txt"), []byte("done"), 0644); err != nil {
		t.Fatalf("write result.txt: %v", err)
	}
	if err := pm.CompleteTask("impl-1", "t-1"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if err := pm.DispatchReview("t-1", "rev-1"); err != nil {
		t.Fatalf("dispatch review: %v", err)
	}

	rr := doReq(t, b.srv.Handler, "POST", "/fail", failReq{
		WorkerID: "rev-1",
		TaskID:   "t-1",
		Error:    "review failed",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("fail review: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	task, _ := pm.Task("t-1")
	if task.Status != pool.TaskCompleted {
		t.Errorf("task status = %q, want completed", task.Status)
	}
	worker, _ := pm.Worker("rev-1")
	if worker.Status != pool.WorkerIdle {
		t.Errorf("worker status = %q, want idle", worker.Status)
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

func TestBrokerAuth_EmptySharedTokenStillAllowsPerWorkerToken(t *testing.T) {
	pm := pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: t.TempDir()}, newNopWAL(t), nil)
	b := NewWorkerBroker(pm, ":0", "")
	if _, err := pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := pm.RegisterWorker("w-1", ""); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	workerToken := workerAuthToken(t, pm, "w-1")

	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-1","state":"alive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", workerToken)
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("per-worker auth with empty shared token: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestBrokerAuth_PerWorkerToken(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	workerToken := workerAuthToken(t, pm, "w-1")

	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-1","state":"alive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", workerToken)
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
	workerToken := workerAuthToken(t, pm, "w-1")

	// Worker w-1's token used to impersonate w-2.
	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-2","state":"alive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", workerToken)
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
	workerToken := workerAuthToken(t, pm, "w-1")

	// w-1's token trying to poll w-2's tasks via GET /task/{wid}.
	req := httptest.NewRequest("GET", "/task/w-2", nil)
	req.Header.Set("X-Mittens-Token", workerToken)
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("GET task identity mismatch: got %d, want 403", rr.Code)
	}
}

func TestBrokerAuth_DeadWorkerTokenHeartbeatRejected(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	workerToken := workerAuthToken(t, pm, "w-1")
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}

	req := httptest.NewRequest("POST", "/heartbeat", bytes.NewBufferString(`{"workerId":"w-1","state":"alive"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", workerToken)
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("dead heartbeat: got %d, want 401: %s", rr.Code, rr.Body.String())
	}
}

func TestBrokerAuth_DeadWorkerTokenQuestionRejected(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	workerToken := workerAuthToken(t, pm, "w-1")
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}

	req := httptest.NewRequest("POST", "/question", bytes.NewBufferString(`{"workerId":"w-1","question":{"question":"which way?","blocking":true}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", workerToken)
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("dead question: got %d, want 401: %s", rr.Code, rr.Body.String())
	}
}

func TestBrokerAuth_DeadWorkerTokenPollRejected(t *testing.T) {
	b, pm := newTestBroker(t)
	pm.SpawnWorker(pool.WorkerSpec{ID: "w-1"})
	pm.RegisterWorker("w-1", "")
	workerToken := workerAuthToken(t, pm, "w-1")
	if err := pm.MarkDead("w-1"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}

	req := httptest.NewRequest("GET", "/task/w-1", nil)
	req.Header.Set("X-Mittens-Token", workerToken)
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("dead poll: got %d, want 401: %s", rr.Code, rr.Body.String())
	}
}
