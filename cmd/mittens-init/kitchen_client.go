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

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

// errWorkerKilled is returned when the Kitchen broker signals the worker identity is no
// longer valid (for example, killed/unknown or token revoked).
var errWorkerKilled = errors.New("worker killed or unknown")

// kitchenClient provides HTTP access to the WorkerBroker running in Kitchen.
type kitchenClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newKitchenClient(kitchenAddr, token string) *kitchenClient {
	return &kitchenClient{
		baseURL: "http://" + kitchenAddr,
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

func (c *kitchenClient) do(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("X-Mittens-Token", c.token)
	}
	return c.httpClient.Do(req)
}

func (c *kitchenClient) postJSON(path string, body any) (*http.Response, error) {
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
func (c *kitchenClient) Register(workerID, containerID string) error {
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
// Returns errWorkerKilled if Kitchen responds 401/404 (worker killed/unknown
// or token revoked).
func (c *kitchenClient) Heartbeat(workerID string, activity *pool.WorkerActivity, currentTool string) error {
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

type pollResult struct {
	Task    *pool.Task
	Recycle bool
}

// PollTask checks if a task is assigned to this worker.
// Returns ({nil,false}, nil) when Kitchen reports no task yet (204 No Content).
// Returns ({nil,true}, nil) when Kitchen asks the worker to recycle its adapter session.
// Returns ({nil,false}, errWorkerKilled) if Kitchen returns 401/404 (worker
// unknown/killed or token revoked).
// Returns ({nil,false}, err) for network or unexpected server errors.
func (c *kitchenClient) PollTask(workerID string) (pollResult, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/task/"+workerID, nil)
	if err != nil {
		return pollResult{}, fmt.Errorf("poll: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return pollResult{}, fmt.Errorf("poll: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return pollResult{}, nil
	case http.StatusUnauthorized, http.StatusNotFound:
		return pollResult{}, errWorkerKilled
	case http.StatusOK:
		// decode below
	default:
		return pollResult{}, fmt.Errorf("poll: HTTP %d", resp.StatusCode)
	}

	var respBody struct {
		Task    *pool.Task `json:"task,omitempty"`
		Recycle bool       `json:"recycle,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&respBody); err != nil {
		return pollResult{}, fmt.Errorf("poll: decode: %w", err)
	}
	return pollResult{Task: respBody.Task, Recycle: respBody.Recycle}, nil
}

// ReportComplete sends a signal-only completion notification to Kitchen.
// The actual result data is on the shared filesystem.
func (c *kitchenClient) ReportComplete(workerID, taskID string) error {
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
func (c *kitchenClient) ReportFail(workerID, taskID string, report taskFailureReport) error {
	payload := map[string]any{
		"workerId":     workerID,
		"taskId":       taskID,
		"error":        sanitizeFailureMessage(report.Error),
		"failureClass": report.FailureClass,
		"detail":       report.Detail,
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

// ReportReview submits a reviewer verdict to Kitchen.
func (c *kitchenClient) ReportReview(workerID, taskID, verdict, feedback, severity string) error {
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

// AskQuestion sends a blocking question to Kitchen.
func (c *kitchenClient) AskQuestion(workerID string, q pool.Question) (string, error) {
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
func (c *kitchenClient) PollAnswer(qid string) *pool.Question {
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
