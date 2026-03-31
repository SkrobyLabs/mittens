package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

// hostAPIClient implements pool.HostAPI by calling the host broker's
// /pool/spawn and /pool/kill endpoints.
type hostAPIClient struct {
	baseURL   string
	token     string
	poolToken string // separate token for pool management endpoints
	client    *http.Client
}

type spawnResp struct {
	ContainerName string `json:"containerName"`
	ContainerID   string `json:"containerId"`
}

// newHostAPIClient creates an HostAPI client pointing at the host broker.
func newHostAPIClient(brokerPort, token, poolToken string) *hostAPIClient {
	return &hostAPIClient{
		baseURL:   fmt.Sprintf("http://host.docker.internal:%s", brokerPort),
		token:     token,
		poolToken: poolToken,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: nil}, // bypass proxy
		},
	}
}

func (h *hostAPIClient) SpawnWorker(ctx context.Context, spec pool.WorkerSpec) (string, string, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("marshal spawn request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/pool/spawn", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("create spawn request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	h.setPoolHeaders(req)

	resp, err := h.client.Do(req)
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

func (h *hostAPIClient) ListContainers(ctx context.Context, sessionID string) ([]pool.ContainerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/pool/containers?sessionId="+url.QueryEscape(sessionID), nil)
	if err != nil {
		return nil, fmt.Errorf("create list containers request: %w", err)
	}
	h.setPoolHeaders(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("list containers: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var containers []pool.ContainerInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode list containers response: %w", err)
	}
	return containers, nil
}

func (h *hostAPIClient) CheckSession(ctx context.Context, sessionID string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/pool/session-alive?sessionId="+url.QueryEscape(sessionID), nil)
	if err != nil {
		return false, fmt.Errorf("create check session request: %w", err)
	}
	h.setPoolHeaders(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("check session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("check session: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Alive bool `json:"alive"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return false, fmt.Errorf("decode check session response: %w", err)
	}
	return result.Alive, nil
}

func (h *hostAPIClient) KillWorker(ctx context.Context, workerID string) error {
	payload := map[string]string{"workerId": workerID}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/pool/kill", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create kill request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	h.setPoolHeaders(req)

	resp, err := h.client.Do(req)
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

// setPoolHeaders sets both the standard auth header and the pool management
// token header on pool-management requests.
func (h *hostAPIClient) setPoolHeaders(req *http.Request) {
	if h.token != "" {
		req.Header.Set("X-Mittens-Token", h.token)
	}
	if h.poolToken != "" {
		req.Header.Set("X-Mittens-Pool-Token", h.poolToken)
	}
}
