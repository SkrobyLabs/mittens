package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type eventingRuntimeAPI struct {
	events      chan pool.RuntimeEvent
	activity    map[string]*pool.WorkerActivity
	transcript  map[string][]pool.WorkerActivityRecord
	subscribedC chan struct{}
}

func (e *eventingRuntimeAPI) SpawnWorker(context.Context, pool.WorkerSpec) (string, string, error) {
	return "", "", nil
}

func (e *eventingRuntimeAPI) KillWorker(context.Context, string) error { return nil }

func (e *eventingRuntimeAPI) ListContainers(context.Context, string) ([]pool.ContainerInfo, error) {
	return nil, nil
}

func (e *eventingRuntimeAPI) RecycleWorker(context.Context, string) error { return nil }

func (e *eventingRuntimeAPI) GetWorkerActivity(_ context.Context, workerID string) (*pool.WorkerActivity, error) {
	if e.activity == nil {
		return nil, nil
	}
	return e.activity[workerID], nil
}

func (e *eventingRuntimeAPI) GetWorkerTranscript(_ context.Context, workerID string) ([]pool.WorkerActivityRecord, error) {
	if e.transcript == nil {
		return nil, nil
	}
	return append([]pool.WorkerActivityRecord(nil), e.transcript[workerID]...), nil
}

func (e *eventingRuntimeAPI) SubscribeEvents(context.Context) (<-chan pool.RuntimeEvent, error) {
	if e.subscribedC != nil {
		select {
		case <-e.subscribedC:
		default:
			close(e.subscribedC)
		}
	}
	return e.events, nil
}

func (e *eventingRuntimeAPI) SubmitAssignment(context.Context, string, pool.Assignment) error {
	return nil
}

func TestBrokerAdvertiseAddrNormalizesLoopbackHosts(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:7682": "host.docker.internal:7682",
		"0.0.0.0:7682":   "host.docker.internal:7682",
		"[::]:7682":      "host.docker.internal:7682",
		"localhost:7682": "host.docker.internal:7682",
		"10.0.0.8:7682":  "10.0.0.8:7682",
	}
	for in, want := range tests {
		if got := brokerAdvertiseAddr(in); got != want {
			t.Fatalf("brokerAdvertiseAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKitchenStartRuntimeStartsBrokerAndScheduler(t *testing.T) {
	k := newTestKitchen(t)
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker w-1: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker w-1: %v", err)
	}
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-2", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker w-2: %v", err)
	}
	taskID, err := k.pm.EnqueueTask(pool.TaskSpec{
		ID:         "t-1",
		Prompt:     "Implement change",
		Complexity: string(ComplexityLow),
		Priority:   1,
		Role:       "implementer",
	})
	if err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	advertisedAddr, err := k.StartRuntime(ctx, "127.0.0.1:0", "test-broker-token", "")
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	if advertisedAddr == "" {
		t.Fatal("expected advertised worker broker address")
	}
	if k.workerBkr == nil || k.workerBkr.ln == nil {
		t.Fatal("expected worker broker listener to be started")
	}

	waitFor(t, time.Second, func() bool {
		task, ok := k.pm.Task(taskID)
		return ok && task.Status == pool.TaskDispatched && task.WorkerID == "w-1"
	})

	port := listenerPort(t, k.workerBkr.ln.Addr().String())
	reqBody, err := json.Marshal(registerReq{WorkerID: "w-2", ContainerID: "container-w-2"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/register", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", "test-broker-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("broker register request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("broker register status = %d, want 200", resp.StatusCode)
	}
	worker, ok := k.pm.Worker("w-2")
	if !ok || worker.Status != pool.WorkerIdle {
		t.Fatalf("worker w-2 = %+v, want idle registered worker", worker)
	}
}

func TestKitchenStartRuntimeAllowsEmptySharedBrokerToken(t *testing.T) {
	k := newTestKitchen(t)
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	workerToken := workerAuthTokenFromKitchen(t, k, "w-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	port := listenerPort(t, k.workerBkr.ln.Addr().String())
	reqBody, err := json.Marshal(registerReq{WorkerID: "w-1", ContainerID: "container-w-1"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/register", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mittens-Token", workerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("broker register request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("broker register status = %d, want 200", resp.StatusCode)
	}
	worker, ok := k.pm.Worker("w-1")
	if !ok || worker.Status != pool.WorkerIdle {
		t.Fatalf("worker w-1 = %+v, want idle registered worker", worker)
	}
}

func TestKitchenStartRuntimeForwardsRuntimeEvents(t *testing.T) {
	k := newTestKitchen(t)
	hostAPI := &eventingRuntimeAPI{events: make(chan pool.RuntimeEvent, 1)}
	k.hostAPI = hostAPI

	sub, cancelSub := k.SubscribeNotifications(1)
	defer cancelSub()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "test-broker-token", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}

	hostAPI.events <- pool.RuntimeEvent{
		Type:         "assignment_submitted",
		WorkerID:     "w-1",
		AssignmentID: "assign-1",
		Message:      "plan",
	}

	select {
	case n := <-sub:
		if n.Type != "runtime_assignment_submitted" {
			t.Fatalf("notification type = %q, want runtime_assignment_submitted", n.Type)
		}
		if n.ID != "w-1" {
			t.Fatalf("notification id = %q, want w-1", n.ID)
		}
		if n.Message != "plan [assignment assign-1]" {
			t.Fatalf("notification message = %q, want assignment summary", n.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded runtime event")
	}
}

func TestKitchenStartRuntimeRecycleEventRequestsWorkerRecycle(t *testing.T) {
	k := newTestKitchen(t)
	hostAPI := &eventingRuntimeAPI{
		events:      make(chan pool.RuntimeEvent, 1),
		subscribedC: make(chan struct{}),
	}
	k.hostAPI = hostAPI
	if _, err := k.pm.SpawnWorker(pool.WorkerSpec{ID: "w-1", Role: "implementer"}); err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if err := k.pm.RegisterWorker("w-1", "container-w-1"); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := k.StartRuntime(ctx, "127.0.0.1:0", "test-broker-token", ""); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		select {
		case <-hostAPI.subscribedC:
			return true
		default:
			return false
		}
	})

	hostAPI.events <- pool.RuntimeEvent{
		Type:     "worker_recycled",
		WorkerID: "w-1",
	}

	waitFor(t, time.Second, func() bool {
		return k.pm.RecycleRequested("w-1")
	})
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func listenerPort(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	return port
}

func workerAuthTokenFromKitchen(t *testing.T, k *Kitchen, workerID string) string {
	t.Helper()
	w, ok := k.pm.Worker(workerID)
	if !ok {
		t.Fatalf("worker %q not found", workerID)
	}
	if w.Token == "" {
		t.Fatalf("worker %q has no auth token", workerID)
	}
	return w.Token
}
