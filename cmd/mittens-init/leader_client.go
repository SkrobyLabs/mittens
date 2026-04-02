package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

// errWorkerKilled is returned when the leader signals the worker identity is no
// longer valid (for example, killed/unknown or token revoked).
var errWorkerKilled = errors.New("worker killed or unknown")

// leaderClient provides HTTP access to the WorkerBroker running in the leader container.
type leaderClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newLeaderClient(leaderAddr, token string) *leaderClient {
	return &leaderClient{
		baseURL: "http://" + leaderAddr,
		token:   token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy: nil, // bypass proxy
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
	}
}

func (c *leaderClient) do(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("X-Mittens-Token", c.token)
	}
	return c.httpClient.Do(req)
}

func (c *leaderClient) postJSON(path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

// Register tells the WorkerBroker that this worker is ready.
func (c *leaderClient) Register(workerID, containerID string) error {
	payload := map[string]string{
		"workerId":    workerID,
		"containerId": containerID,
	}
	resp, err := c.postJSON("/register", payload)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("register: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Heartbeat sends a heartbeat to the WorkerBroker.
// Returns errWorkerKilled if the leader responds 401/404 (worker killed/unknown
// or token revoked).
func (c *leaderClient) Heartbeat(workerID string, activity *pool.WorkerActivity, currentTool string) error {
	payload := struct {
		WorkerID    string               `json:"workerId"`
		State       string               `json:"state"`
		Activity    *pool.WorkerActivity `json:"activity,omitempty"`
		CurrentTool string               `json:"currentTool,omitempty"`
	}{
		WorkerID:    workerID,
		State:       "alive",
		Activity:    activity,
		CurrentTool: currentTool,
	}
	resp, err := c.postJSON("/heartbeat", payload)
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return errWorkerKilled
	}
	return nil
}

// PollTask checks if a task is assigned to this worker.
// Returns (nil, nil) when the leader reports no task yet (204 No Content).
// Returns (nil, errWorkerKilled) if the leader returns 401/404 (worker
// unknown/killed or token revoked).
// Returns (nil, err) for network or unexpected server errors.
func (c *leaderClient) PollTask(workerID string) (*pool.Task, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/task/"+workerID, nil)
	if err != nil {
		return nil, fmt.Errorf("poll: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("poll: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusUnauthorized, http.StatusNotFound:
		return nil, errWorkerKilled
	case http.StatusOK:
		// decode below
	default:
		return nil, fmt.Errorf("poll: HTTP %d", resp.StatusCode)
	}

	var task pool.Task
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&task); err != nil {
		return nil, fmt.Errorf("poll: decode: %w", err)
	}
	return &task, nil
}

// ReportComplete sends a signal-only completion notification to the leader.
// The actual result data is on the shared filesystem.
func (c *leaderClient) ReportComplete(workerID, taskID string) error {
	payload := map[string]string{
		"workerId": workerID,
		"taskId":   taskID,
	}
	resp, err := c.postJSON("/complete", payload)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("complete: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ReportFail reports a task as failed.
func (c *leaderClient) ReportFail(workerID, taskID, errMsg string) error {
	payload := map[string]string{
		"workerId": workerID,
		"taskId":   taskID,
		"error":    sanitizeFailureMessage(errMsg),
	}
	resp, err := c.postJSON("/fail", payload)
	if err != nil {
		return fmt.Errorf("fail: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("fail: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

const maxFailureMessageLen = 512

func sanitizeFailureMessage(errMsg string) string {
	msg := strings.Join(strings.Fields(strings.TrimSpace(errMsg)), " ")
	if msg == "" {
		return "task execution failed"
	}

	runes := []rune(msg)
	if len(runes) <= maxFailureMessageLen {
		return msg
	}
	return string(runes[:maxFailureMessageLen-3]) + "..."
}

// ReportReview submits a reviewer verdict to the leader.
func (c *leaderClient) ReportReview(workerID, taskID, verdict, feedback, severity string) error {
	payload := map[string]string{
		"workerId": workerID,
		"taskId":   taskID,
		"verdict":  verdict,
		"feedback": feedback,
		"severity": severity,
	}
	resp, err := c.postJSON("/report_review", payload)
	if err != nil {
		return fmt.Errorf("report_review: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("report_review: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// AskQuestion sends a blocking question to the leader.
func (c *leaderClient) AskQuestion(workerID string, q pool.Question) (string, error) {
	payload := map[string]any{
		"workerId": workerID,
		"question": q,
	}
	resp, err := c.postJSON("/question", payload)
	if err != nil {
		return "", fmt.Errorf("ask question: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return "", errWorkerKilled
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("ask question: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		QuestionID string `json:"questionId"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	return result.QuestionID, nil
}

// PollAnswer checks if a question has been answered.
// Returns nil if not yet answered.
func (c *leaderClient) PollAnswer(qid string) *pool.Question {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/answer/"+qid, nil)
	if err != nil {
		return nil
	}
	resp, err := c.do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var q pool.Question
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&q); err != nil {
		return nil
	}
	return &q
}
