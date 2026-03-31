package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

func newTestHostBrokerWithPool(t *testing.T) *HostBroker {
	t.Helper()
	b := NewHostBroker("", "", nil)
	b.OnPoolSpawn = func(spec pool.WorkerSpec) (string, string, error) {
		return "mittens-pool-" + spec.ID, "sha256:" + spec.ID, nil
	}
	b.OnPoolKill = func(workerID string) error {
		return nil
	}
	return b
}

func doPoolReq(t *testing.T, b *HostBroker, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	b.srv.Handler.ServeHTTP(rr, req)
	return rr
}

func TestPoolSpawn(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "POST", "/pool/spawn", pool.WorkerSpec{ID: "w-1", Role: "impl"})

	if rr.Code != http.StatusOK {
		t.Fatalf("spawn: got %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var resp poolSpawnResp
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.ContainerName != "mittens-pool-w-1" {
		t.Errorf("containerName = %q", resp.ContainerName)
	}
	if resp.ContainerID != "sha256:w-1" {
		t.Errorf("containerID = %q", resp.ContainerID)
	}
}

func TestPoolSpawn_NotConfigured(t *testing.T) {
	b := NewHostBroker("", "", nil)
	rr := doPoolReq(t, b, "POST", "/pool/spawn", pool.WorkerSpec{ID: "w-1"})

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured spawn: got %d, want 503", rr.Code)
	}
}

func TestPoolKill(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "POST", "/pool/kill", map[string]string{"workerId": "w-1"})

	if rr.Code != http.StatusOK {
		t.Fatalf("kill: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestPoolKill_MissingID(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "POST", "/pool/kill", map[string]string{})

	if rr.Code != http.StatusBadRequest {
		t.Errorf("kill no id: got %d, want 400", rr.Code)
	}
}

func TestPoolSpawn_WrongMethod(t *testing.T) {
	b := newTestHostBrokerWithPool(t)
	rr := doPoolReq(t, b, "GET", "/pool/spawn", nil)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method: got %d, want 405", rr.Code)
	}
}
