package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

func TestInitializeResponseAdvertisesToolCapability(t *testing.T) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage("1"),
		"result": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false,
				},
			},
			"serverInfo": map[string]any{
				"name":    "team-mcp",
				"version": "0.2.0",
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Result struct {
			Capabilities struct {
				Tools map[string]any `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Result.Capabilities.Tools == nil {
		t.Fatal("tools capability missing")
	}
	v, ok := decoded.Result.Capabilities.Tools["listChanged"]
	if !ok {
		t.Fatal("tools.listChanged missing")
	}
	if got, ok := v.(bool); !ok || got {
		t.Fatalf("tools.listChanged = %#v, want false", v)
	}
}

func TestAcquireRuntimeLock_Contention(t *testing.T) {
	stateDir := t.TempDir()

	var firstLog bytes.Buffer
	firstLock, err := acquireRuntimeLock(stateDir, "team-session-7", &firstLog)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	t.Cleanup(func() {
		if err := firstLock.Close(); err != nil {
			t.Fatalf("close first lock: %v", err)
		}
	})
	if !strings.Contains(firstLog.String(), "active runtime owner") {
		t.Fatalf("first startup log = %q, want active owner diagnostic", firstLog.String())
	}
	if strings.Contains(firstLog.String(), runtimeLockFileName) {
		t.Fatalf("first startup log = %q, want concise owner diagnostic", firstLog.String())
	}

	var secondLog bytes.Buffer
	secondLock, err := acquireRuntimeLock(stateDir, "team-session-7", &secondLog)
	if err == nil {
		_ = secondLock.Close()
		t.Fatal("expected second lock acquisition to fail")
	}
	if !strings.Contains(err.Error(), "session runtime lock busy") {
		t.Fatalf("second acquire error = %q, want busy error", err)
	}
	if !strings.Contains(secondLog.String(), "active runtime owner") {
		t.Fatalf("second startup log = %q, want active owner diagnostic", secondLog.String())
	}
	if !strings.Contains(secondLog.String(), "pid=") {
		t.Fatalf("second startup log = %q, want owner details", secondLog.String())
	}
	if strings.Contains(secondLog.String(), runtimeLockFileName) {
		t.Fatalf("second startup log = %q, want concise owner diagnostic", secondLog.String())
	}
}

func TestStartWorkerBroker_BindFailureIsFatal(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer occupied.Close()

	pm := pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: t.TempDir()}, newNopWAL(t), nil)

	var startupLog bytes.Buffer
	broker, err := startWorkerBroker(pm, occupied.Addr().String(), "test-token", &startupLog)
	if err == nil {
		if broker != nil {
			_ = broker.Close()
		}
		t.Fatal("expected broker bind failure")
	}
	if !strings.Contains(err.Error(), "worker broker listen") {
		t.Fatalf("bind error = %q, want listen failure", err)
	}
	if !strings.Contains(startupLog.String(), "worker broker bind failed") {
		t.Fatalf("startup log = %q, want bind failure diagnostic", startupLog.String())
	}
	if strings.Contains(startupLog.String(), "worker broker ready") {
		t.Fatalf("startup log = %q, readiness should not be logged on bind failure", startupLog.String())
	}
}

func TestStartWorkerBroker_UsesRecoveredWorkerTokensWithoutReplay(t *testing.T) {
	stateDir := t.TempDir()
	walPath := filepath.Join(stateDir, "events.jsonl")

	wal1, err := pool.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	pm1 := pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: stateDir}, wal1, nil)
	if _, err := pm1.SpawnWorker(pool.WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	workerToken := workerAuthToken(t, pm1, "w-1")
	if err := pm1.RegisterWorker("w-1", ""); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("close wal1: %v", err)
	}

	wal2, err := pool.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("reopen wal: %v", err)
	}
	defer wal2.Close()
	pm2, err := pool.RecoverPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: stateDir}, wal2, nil)
	if err != nil {
		t.Fatalf("recover pool manager: %v", err)
	}

	var startupLog bytes.Buffer
	broker, err := startWorkerBroker(pm2, "127.0.0.1:0", "broker-token", &startupLog)
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Fatalf("close broker: %v", err)
		}
	})
	if !strings.Contains(startupLog.String(), "worker broker ready on "+broker.ln.Addr().String()) {
		t.Fatalf("startup log = %q, want broker readiness diagnostic", startupLog.String())
	}

	// Bind readiness must remain synchronous even though serving is delayed.
	conn, err := net.DialTimeout("tcp", broker.ln.Addr().String(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("dial bound broker: %v", err)
	}
	_ = conn.Close()

	serveWorkerBroker(broker, &startupLog)

	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		resp, err = postHeartbeat(client, "http://"+broker.ln.Addr().String()+"/heartbeat", workerToken, "w-1")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat with recovered worker token: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("heartbeat status = %d, want 200: %s", resp.StatusCode, body)
	}
}

func TestStartWorkerBroker_RejectsRecoveredDeadWorkerActivity(t *testing.T) {
	stateDir := t.TempDir()
	walPath := filepath.Join(stateDir, "events.jsonl")

	wal1, err := pool.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	pm1 := pool.NewPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: stateDir}, wal1, nil)
	if _, err := pm1.SpawnWorker(pool.WorkerSpec{ID: "w-1"}); err != nil {
		t.Fatalf("spawn worker: %v", err)
	}
	workerToken := workerAuthToken(t, pm1, "w-1")
	if err := pm1.RegisterWorker("w-1", ""); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if err := pm1.MarkDead("w-1"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("close wal1: %v", err)
	}

	wal2, err := pool.OpenWAL(walPath)
	if err != nil {
		t.Fatalf("reopen wal: %v", err)
	}
	defer wal2.Close()
	pm2, err := pool.RecoverPoolManager(pool.PoolConfig{MaxWorkers: 4, StateDir: stateDir}, wal2, nil)
	if err != nil {
		t.Fatalf("recover pool manager: %v", err)
	}

	var startupLog bytes.Buffer
	broker, err := startWorkerBroker(pm2, "127.0.0.1:0", "broker-token", &startupLog)
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Fatalf("close broker: %v", err)
		}
	})
	serveWorkerBroker(broker, &startupLog)

	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	var heartbeatResp *http.Response
	for {
		heartbeatResp, err = postHeartbeat(client, "http://"+broker.ln.Addr().String()+"/heartbeat", workerToken, "w-1")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat with recovered dead worker token: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer heartbeatResp.Body.Close()
	if heartbeatResp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(heartbeatResp.Body)
		t.Fatalf("heartbeat status = %d, want 401: %s", heartbeatResp.StatusCode, body)
	}

	questionResp, err := postQuestion(client, "http://"+broker.ln.Addr().String()+"/question", workerToken, "w-1")
	if err != nil {
		t.Fatalf("question with recovered dead worker token: %v", err)
	}
	defer questionResp.Body.Close()
	if questionResp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(questionResp.Body)
		t.Fatalf("question status = %d, want 401: %s", questionResp.StatusCode, body)
	}

	pollReq, err := http.NewRequest(http.MethodGet, "http://"+broker.ln.Addr().String()+"/task/w-1", nil)
	if err != nil {
		t.Fatalf("new poll request: %v", err)
	}
	pollReq.Header.Set("X-Mittens-Token", workerToken)
	pollResp, err := client.Do(pollReq)
	if err != nil {
		t.Fatalf("poll with recovered dead worker token: %v", err)
	}
	defer pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(pollResp.Body)
		t.Fatalf("poll status = %d, want 401: %s", pollResp.StatusCode, body)
	}
}

func postHeartbeat(client *http.Client, url, token, workerID string) (*http.Response, error) {
	body := strings.NewReader(`{"workerId":"` + workerID + `","state":"alive"}`)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", token)
	return client.Do(req)
}

func postQuestion(client *http.Client, url, token, workerID string) (*http.Response, error) {
	body := strings.NewReader(`{"workerId":"` + workerID + `","question":{"question":"which way?","blocking":true}}`)
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", token)
	return client.Do(req)
}
