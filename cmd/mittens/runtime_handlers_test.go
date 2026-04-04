package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func newTestHostBrokerWithRuntime(t *testing.T) *HostBroker {
	t.Helper()
	b := NewHostBroker("", "", nil)
	b.OnPoolSpawn = func(spec pool.WorkerSpec) (string, string, error) {
		return "mittens-runtime-" + spec.ID, "sha256:" + spec.ID, nil
	}
	b.OnPoolKill = func(workerID string) error {
		return nil
	}
	return b
}

func doRuntimeReq(t *testing.T, b *HostBroker, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)
	return rr
}

func TestRuntimeWorkers(t *testing.T) {
	b := newTestHostBrokerWithRuntime(t)
	b.OnRuntimeListWorkers = func() ([]pool.RuntimeWorker, error) {
		return []pool.RuntimeWorker{{
			ID:          "w-1",
			ContainerID: "cid-1",
			Status:      pool.WorkerIdle,
			Provider:    "openai",
		}}, nil
	}

	rr := doRuntimeReq(t, b, http.MethodGet, "/v1/workers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("workers: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp runtimeWorkersResp
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Workers) != 1 || resp.Workers[0].ID != "w-1" {
		t.Fatalf("workers = %+v, want w-1", resp.Workers)
	}
}

func TestRuntimeWorkerStatusNotFound(t *testing.T) {
	b := newTestHostBrokerWithRuntime(t)
	b.OnRuntimeGetWorker = func(string) (*pool.RuntimeWorker, error) {
		return nil, os.ErrNotExist
	}

	rr := doRuntimeReq(t, b, http.MethodGet, "/v1/workers/w-missing", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestRuntimeWorkerActivity(t *testing.T) {
	b := newTestHostBrokerWithRuntime(t)
	b.OnRuntimeGetWorkerActivity = func(string) (*pool.WorkerActivity, []pool.WorkerActivityRecord, error) {
		return &pool.WorkerActivity{
				Kind:  "tool",
				Phase: "started",
				Name:  "apply_patch",
			}, []pool.WorkerActivityRecord{{
				Activity: pool.WorkerActivity{
					Kind:  "tool",
					Phase: "started",
					Name:  "apply_patch",
				},
			}}, nil
	}

	rr := doRuntimeReq(t, b, http.MethodGet, "/v1/workers/w-1/activity", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("activity: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp runtimeActivityResp
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Activity == nil || resp.Activity.Name != "apply_patch" {
		t.Fatalf("activity = %+v, want apply_patch", resp.Activity)
	}
	if len(resp.Transcript) != 1 {
		t.Fatalf("transcript len = %d, want 1", len(resp.Transcript))
	}
}

func TestRuntimeWorkerAssignment(t *testing.T) {
	b := newTestHostBrokerWithRuntime(t)
	var gotWorkerID string
	var gotAssignment pool.Assignment
	b.OnRuntimeSubmitAssignment = func(workerID string, assignment pool.Assignment) error {
		gotWorkerID = workerID
		gotAssignment = assignment
		return nil
	}

	rr := doRuntimeReq(t, b, http.MethodPost, "/v1/workers/w-2/assignments", pool.Assignment{
		ID:   "assign-1",
		Type: "plan",
		Payload: map[string]any{
			"idea": "ship runtime api",
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("assignment: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if gotWorkerID != "w-2" {
		t.Fatalf("workerID = %q, want w-2", gotWorkerID)
	}
	if gotAssignment.ID != "assign-1" || gotAssignment.Type != "plan" {
		t.Fatalf("assignment = %+v, want assign-1/plan", gotAssignment)
	}
}

func TestRuntimeRecycleWorkerError(t *testing.T) {
	b := newTestHostBrokerWithRuntime(t)
	b.OnRuntimeRecycleWorker = func(string) error {
		return errors.New("boom")
	}

	rr := doRuntimeReq(t, b, http.MethodPost, "/v1/workers/w-1/recycle", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("recycle: got %d, want 500: %s", rr.Code, rr.Body.String())
	}
}
