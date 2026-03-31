package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

const (
	maxSmallBody = 4 * 1024 // 4KB — all endpoints are signal-only
)

// WorkerBroker is an HTTP server inside the leader container that exposes
// PoolManager methods as REST endpoints for worker agents.
type WorkerBroker struct {
	pm           *pool.PoolManager
	addr         string
	token        string
	srv          *http.Server
	ln           net.Listener
	mu           sync.RWMutex
	workerTokens map[string]string // token → workerID
}

// NewWorkerBroker creates a new WorkerBroker.
func NewWorkerBroker(pm *pool.PoolManager, addr, token string) *WorkerBroker {
	b := &WorkerBroker{pm: pm, addr: addr, token: token, workerTokens: make(map[string]string)}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", b.withAuth(b.handleRegister))
	mux.HandleFunc("POST /heartbeat", b.withAuth(b.handleHeartbeat))
	mux.HandleFunc("GET /task/{wid}", b.withAuth(b.handlePollTask))
	mux.HandleFunc("POST /complete", b.withAuth(b.handleComplete))
	mux.HandleFunc("POST /fail", b.withAuth(b.handleFail))
	mux.HandleFunc("POST /report_review", b.withAuth(b.handleReportReview))
	mux.HandleFunc("POST /question", b.withAuth(b.handleQuestion))
	mux.HandleFunc("GET /answer/{qid}", b.withAuth(b.handleAnswer))

	b.srv = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return b
}

// Serve starts the broker and blocks until shut down.
func (b *WorkerBroker) Serve() error {
	ln, err := net.Listen("tcp", b.addr)
	if err != nil {
		return fmt.Errorf("worker broker listen: %w", err)
	}
	b.ln = ln
	return b.srv.Serve(ln)
}

// Close shuts down the broker gracefully.
func (b *WorkerBroker) Close() error {
	if b.srv != nil {
		return b.srv.Close()
	}
	return nil
}

// RegisterWorkerToken registers a per-worker token so the broker can validate
// that a bearer token belongs to a specific worker.
func (b *WorkerBroker) RegisterWorkerToken(workerID, token string) {
	b.mu.Lock()
	b.workerTokens[token] = workerID
	b.mu.Unlock()
}

// --- Auth middleware ---

func (b *WorkerBroker) withAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Reject all requests when no broker token is configured — prevents
		// the empty-string bypass where ConstantTimeCompare("","") == 1.
		if b.token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		reqToken := r.Header.Get("X-Mittens-Token")
		if reqToken == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Check per-worker tokens first.
		b.mu.RLock()
		authedWorkerID, isWorkerToken := b.workerTokens[reqToken]
		b.mu.RUnlock()

		if isWorkerToken {
			// Verify the token owner matches the worker claimed in the request.
			if claimed := extractWorkerID(r); claimed != "" && claimed != authedWorkerID {
				http.Error(w, "forbidden: worker identity mismatch", http.StatusForbidden)
				return
			}
			handler(w, r)
			return
		}

		// Fall back to the shared broker token.
		if subtle.ConstantTimeCompare([]byte(reqToken), []byte(b.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}
}

// extractWorkerID returns the worker ID claimed by the request.
// For GET /task/{wid} it comes from the URL; for POST endpoints it is
// peeked from the JSON body (the body is re-wrapped so the handler can
// still read it).
func extractWorkerID(r *http.Request) string {
	if wid := r.PathValue("wid"); wid != "" {
		return wid
	}
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSmallBody))
		if err != nil {
			return ""
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var peek struct {
			WorkerID string `json:"workerId"`
		}
		if json.Unmarshal(body, &peek) == nil {
			return peek.WorkerID
		}
	}
	return ""
}

// --- Request/response types ---

type registerReq struct {
	WorkerID    string `json:"workerId"`
	ContainerID string `json:"containerId"`
}

type heartbeatReq struct {
	WorkerID    string `json:"workerId"`
	State       string `json:"state"`
	CurrentTool string `json:"currentTool"`
}

type completeSignal struct {
	WorkerID string `json:"workerId"`
	TaskID   string `json:"taskId"`
}

type failReq struct {
	WorkerID string `json:"workerId"`
	TaskID   string `json:"taskId"`
	Error    string `json:"error"`
}

type reviewReportReq struct {
	WorkerID string `json:"workerId"`
	TaskID   string `json:"taskId"`
	Verdict  string `json:"verdict"`
	Feedback string `json:"feedback"`
	Severity string `json:"severity"`
}

type questionReq struct {
	WorkerID string        `json:"workerId"`
	Question pool.Question `json:"question"`
}

type questionResp struct {
	QuestionID string `json:"questionId"`
}

// --- Helpers ---

// decodeBody reads a size-limited request body and decodes JSON into dst.
func (b *WorkerBroker) decodeBody(w http.ResponseWriter, r *http.Request, dst any, maxSize int64) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return false
	}
	if int64(len(body)) > maxSize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

// errorCode maps PoolManager errors to HTTP status codes.
func errorCode(err error) int {
	msg := err.Error()
	if strings.Contains(msg, "not found") {
		return http.StatusNotFound
	}
	if strings.Contains(msg, "expected") || strings.Contains(msg, "already") {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// --- Handlers ---

func (b *WorkerBroker) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if !b.decodeBody(w, r, &req, maxSmallBody) {
		return
	}
	if req.WorkerID == "" {
		http.Error(w, "missing workerId", http.StatusBadRequest)
		return
	}
	if err := b.pm.RegisterWorker(req.WorkerID, req.ContainerID); err != nil {
		http.Error(w, err.Error(), errorCode(err))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *WorkerBroker) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatReq
	if !b.decodeBody(w, r, &req, maxSmallBody) {
		return
	}
	if req.WorkerID == "" {
		http.Error(w, "missing workerId", http.StatusBadRequest)
		return
	}
	if err := b.pm.Heartbeat(req.WorkerID, req.State, req.CurrentTool); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *WorkerBroker) handlePollTask(w http.ResponseWriter, r *http.Request) {
	wid := r.PathValue("wid")
	if wid == "" {
		http.Error(w, "missing worker ID", http.StatusBadRequest)
		return
	}
	task := b.pm.PollTask(wid)
	if task == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(task)
}

func (b *WorkerBroker) handleComplete(w http.ResponseWriter, r *http.Request) {
	var sig completeSignal
	if !b.decodeBody(w, r, &sig, maxSmallBody) {
		return
	}
	if sig.WorkerID == "" || sig.TaskID == "" {
		http.Error(w, "missing workerId or taskId", http.StatusBadRequest)
		return
	}
	if err := b.pm.CompleteTask(sig.WorkerID, sig.TaskID); err != nil {
		http.Error(w, err.Error(), errorCode(err))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *WorkerBroker) handleFail(w http.ResponseWriter, r *http.Request) {
	var req failReq
	if !b.decodeBody(w, r, &req, maxSmallBody) {
		return
	}
	if req.WorkerID == "" || req.TaskID == "" {
		http.Error(w, "missing workerId or taskId", http.StatusBadRequest)
		return
	}
	if task, ok := b.pm.Task(req.TaskID); ok && task.Status == pool.TaskReviewing && task.ReviewerID == req.WorkerID {
		if err := b.pm.AbortReview(req.WorkerID, req.TaskID); err != nil {
			http.Error(w, err.Error(), errorCode(err))
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := b.pm.FailTask(req.WorkerID, req.TaskID, req.Error); err != nil {
		http.Error(w, err.Error(), errorCode(err))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (b *WorkerBroker) handleReportReview(w http.ResponseWriter, r *http.Request) {
	var req reviewReportReq
	if !b.decodeBody(w, r, &req, maxSmallBody) {
		return
	}
	if req.WorkerID == "" || req.TaskID == "" || req.Verdict == "" {
		http.Error(w, "missing workerId, taskId, or verdict", http.StatusBadRequest)
		return
	}
	if req.Verdict != pool.ReviewPass && req.Verdict != pool.ReviewFail {
		http.Error(w, fmt.Sprintf("invalid verdict %q", req.Verdict), http.StatusBadRequest)
		return
	}
	task, ok := b.pm.Task(req.TaskID)
	if !ok {
		http.Error(w, fmt.Sprintf("task %q not found", req.TaskID), http.StatusNotFound)
		return
	}
	if task.ReviewerID != req.WorkerID {
		http.Error(w, fmt.Sprintf("worker %q is not reviewer for task %q", req.WorkerID, req.TaskID), http.StatusConflict)
		return
	}
	if err := b.pm.ReportReview(req.TaskID, req.Verdict, req.Feedback, req.Severity); err != nil {
		http.Error(w, err.Error(), errorCode(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (b *WorkerBroker) handleQuestion(w http.ResponseWriter, r *http.Request) {
	var req questionReq
	if !b.decodeBody(w, r, &req, maxSmallBody) {
		return
	}
	if req.WorkerID == "" {
		http.Error(w, "missing workerId", http.StatusBadRequest)
		return
	}
	qid, err := b.pm.AskQuestion(req.WorkerID, req.Question)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(questionResp{QuestionID: qid})
}

func (b *WorkerBroker) handleAnswer(w http.ResponseWriter, r *http.Request) {
	qid := r.PathValue("qid")
	if qid == "" {
		http.Error(w, "missing question ID", http.StatusBadRequest)
		return
	}
	q := b.pm.GetQuestion(qid)
	if q == nil {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	if !q.Answered {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(q)
}
