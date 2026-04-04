package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type runtimeClient struct {
	socketPath string
	token      string
	poolToken  string
	client     *http.Client
	streamer   *http.Client
}

type spawnResp struct {
	ContainerName string `json:"containerName"`
	ContainerID   string `json:"containerId"`
}

func newRuntimeClient(socketPath, token, poolToken string) *runtimeClient {
	socketPath = strings.TrimSpace(socketPath)
	return &runtimeClient{
		socketPath: socketPath,
		token:      token,
		poolToken:  poolToken,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
		streamer: &http.Client{
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

func (r *runtimeClient) SpawnWorker(ctx context.Context, spec pool.WorkerSpec) (string, string, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("marshal spawn request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runtime/v1/workers", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("create spawn request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	r.setPoolHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("spawn worker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", "", fmt.Errorf("spawn worker: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result spawnResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode spawn response: %w", err)
	}
	return result.ContainerName, result.ContainerID, nil
}

func (r *runtimeClient) KillWorker(ctx context.Context, workerID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://runtime/v1/workers/"+url.PathEscape(workerID), nil)
	if err != nil {
		return fmt.Errorf("create kill request: %w", err)
	}
	r.setPoolHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("kill worker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("kill worker: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (r *runtimeClient) ListContainers(ctx context.Context, sessionID string) ([]pool.ContainerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runtime/v1/workers?sessionId="+url.QueryEscape(sessionID), nil)
	if err != nil {
		return nil, fmt.Errorf("create list containers request: %w", err)
	}
	r.setPoolHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("list containers: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var payload struct {
		Workers []pool.RuntimeWorker `json:"workers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode list containers response: %w", err)
	}
	containers := make([]pool.ContainerInfo, 0, len(payload.Workers))
	for _, worker := range payload.Workers {
		state := "running"
		switch strings.ToLower(strings.TrimSpace(worker.Status)) {
		case "", pool.WorkerIdle, pool.WorkerWorking, pool.WorkerBlocked, pool.WorkerSpawning:
			state = "running"
		default:
			state = worker.Status
		}
		containers = append(containers, pool.ContainerInfo{
			ContainerID: worker.ContainerID,
			WorkerID:    worker.ID,
			State:       state,
			Status:      worker.Status,
		})
	}
	return containers, nil
}

func (r *runtimeClient) RecycleWorker(ctx context.Context, workerID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runtime/v1/workers/"+url.PathEscape(workerID)+"/recycle", nil)
	if err != nil {
		return fmt.Errorf("create recycle request: %w", err)
	}
	r.setPoolHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("recycle worker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("recycle worker: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (r *runtimeClient) GetWorkerActivity(ctx context.Context, workerID string) (*pool.WorkerActivity, error) {
	payload, err := r.getWorkerActivityPayload(ctx, workerID)
	if err != nil {
		return nil, err
	}
	return payload.Activity, nil
}

func (r *runtimeClient) GetWorkerTranscript(ctx context.Context, workerID string) ([]pool.WorkerActivityRecord, error) {
	payload, err := r.getWorkerActivityPayload(ctx, workerID)
	if err != nil {
		return nil, err
	}
	return append([]pool.WorkerActivityRecord(nil), payload.Transcript...), nil
}

func (r *runtimeClient) getWorkerActivityPayload(ctx context.Context, workerID string) (*runtimeActivityPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runtime/v1/workers/"+url.PathEscape(workerID)+"/activity", nil)
	if err != nil {
		return nil, fmt.Errorf("create activity request: %w", err)
	}
	r.setPoolHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get worker activity: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("get worker activity: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var payload runtimeActivityPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode worker activity response: %w", err)
	}
	return &payload, nil
}

type runtimeActivityPayload struct {
	Activity   *pool.WorkerActivity        `json:"activity"`
	Transcript []pool.WorkerActivityRecord `json:"transcript"`
}

func (r *runtimeClient) SubscribeEvents(ctx context.Context) (<-chan pool.RuntimeEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runtime/v1/events", nil)
	if err != nil {
		return nil, fmt.Errorf("create events request: %w", err)
	}
	r.setPoolHeaders(req)

	resp, err := r.streamer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscribe events: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("subscribe events: HTTP %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan pool.RuntimeEvent, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event pool.RuntimeEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				continue
			}
			select {
			case ch <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (r *runtimeClient) SubmitAssignment(ctx context.Context, workerID string, assignment pool.Assignment) error {
	body, err := json.Marshal(assignment)
	if err != nil {
		return fmt.Errorf("marshal assignment request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runtime/v1/workers/"+url.PathEscape(workerID)+"/assignments", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create assignment request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	r.setPoolHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("submit assignment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("submit assignment: HTTP %d: %s", resp.StatusCode, string(payload))
	}
	return nil
}

func (r *runtimeClient) setPoolHeaders(req *http.Request) {
	if r.token != "" {
		req.Header.Set("X-Mittens-Token", r.token)
	}
	if r.poolToken != "" {
		req.Header.Set("X-Mittens-Pool-Token", r.poolToken)
	}
}
