package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type kitchenAPIClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newKitchenAPIClient(meta serveMetadata) *kitchenAPIClient {
	return &kitchenAPIClient{
		baseURL: strings.TrimRight(strings.TrimSpace(meta.URL), "/"),
		token:   strings.TrimSpace(meta.Token),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func openKitchenAPIClient(repoPath string) (*kitchenAPIClient, bool, error) {
	meta, ok, err := detectKitchenServer(repoPath)
	if err != nil || !ok {
		return nil, ok, err
	}
	return newKitchenAPIClient(meta), true, nil
}

func (c *kitchenAPIClient) request(method, path string, body any, dst any) error {
	if c == nil {
		return fmt.Errorf("kitchen api client not configured")
	}

	var payload io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
		payload = &buf
	}

	req, err := http.NewRequest(method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-Kitchen-Token", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && strings.TrimSpace(apiErr.Error) != "" {
			return fmt.Errorf(apiErr.Error)
		}
		return fmt.Errorf("%s %s returned %d", method, path, resp.StatusCode)
	}
	if dst == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (c *kitchenAPIClient) SubmitIdea(idea, lineage string, auto, implReview bool, dependsOn ...string) (map[string]any, error) {
	return c.SubmitIdeaAt(idea, lineage, auto, implReview, "", nil, dependsOn...)
}

func (c *kitchenAPIClient) SubmitIdeaAt(idea, lineage string, auto, implReview bool, anchorRef string, overrides *PlanProviderOverrides, dependsOn ...string) (map[string]any, error) {
	req := map[string]any{
		"idea":       idea,
		"lineage":    lineage,
		"auto":       auto,
		"implReview": implReview,
	}
	if strings.TrimSpace(anchorRef) != "" {
		req["anchorRef"] = strings.TrimSpace(anchorRef)
	}
	if len(dependsOn) > 0 {
		req["dependsOn"] = dependsOn
	}
	if overrides != nil {
		req["providerOverrides"] = overrides
	}
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/ideas", req, &resp)
}

func (c *kitchenAPIClient) SubmitResearch(topic string) (map[string]any, error) {
	req := map[string]any{
		"topic": topic,
	}
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/research", req, &resp)
}

func (c *kitchenAPIClient) PromoteResearch(planID, lineage string, auto, implReview bool) (map[string]any, error) {
	req := map[string]any{
		"lineage":    lineage,
		"auto":       auto,
		"implReview": implReview,
	}
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/promote", req, &resp)
}

func (c *kitchenAPIClient) RefineResearch(planID, clarification string) (map[string]any, error) {
	req := map[string]any{
		"clarification": clarification,
	}
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/refine-research", req, &resp)
}

func (c *kitchenAPIClient) ExtendCouncil(planID string, turns int) (map[string]any, error) {
	req := map[string]any{}
	if turns != 0 {
		req["turns"] = turns
	}
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/extend", req, &resp)
}

func (c *kitchenAPIClient) ListPlans(includeCompleted bool) ([]PlanRecord, error) {
	path := "/v1/plans"
	if includeCompleted {
		path += "?completed=true"
	}
	var resp struct {
		Plans []PlanRecord `json:"plans"`
	}
	err := c.request(http.MethodGet, path, nil, &resp)
	return resp.Plans, err
}

func (c *kitchenAPIClient) PlanDetail(planID string) (PlanDetail, error) {
	var detail PlanDetail
	err := c.request(http.MethodGet, "/v1/plans/"+url.PathEscape(planID), nil, &detail)
	return detail, err
}

func (c *kitchenAPIClient) PlanHistory(planID string, cycle int) (map[string]any, []PlanHistoryEntry, error) {
	path := "/v1/plans/" + url.PathEscape(planID) + "/history"
	if cycle > 0 {
		path += fmt.Sprintf("?cycle=%d", cycle)
	}
	var resp struct {
		PlanID  string             `json:"planId"`
		Cycle   int                `json:"cycle"`
		History []PlanHistoryEntry `json:"history"`
	}
	if err := c.request(http.MethodGet, path, nil, &resp); err != nil {
		return nil, nil, err
	}
	payload := map[string]any{
		"planId":  resp.PlanID,
		"cycle":   resp.Cycle,
		"history": resp.History,
	}
	return payload, resp.History, nil
}

func (c *kitchenAPIClient) Evidence(planID, tier string) (map[string]any, error) {
	path := "/v1/plans/" + url.PathEscape(planID) + "/evidence"
	if tier != "" && tier != evidenceTierRich {
		path += "?tier=" + url.QueryEscape(tier)
	}
	var resp map[string]any
	return resp, c.request(http.MethodGet, path, nil, &resp)
}

func (c *kitchenAPIClient) TaskActivity(taskID string) ([]pool.WorkerActivityRecord, error) {
	var resp struct {
		Transcript []pool.WorkerActivityRecord `json:"transcript"`
	}
	err := c.request(http.MethodGet, "/v1/tasks/"+url.PathEscape(taskID)+"/activity", nil, &resp)
	return resp.Transcript, err
}

func (c *kitchenAPIClient) TaskOutput(taskID string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("kitchen api client not configured")
	}
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/v1/tasks/"+url.PathEscape(taskID)+"/output", nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("X-Kitchen-Token", c.token)
	}
	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode == http.StatusNotFound {
		return "", &fs.PathError{
			Op:   "GET",
			Path: "/v1/tasks/" + url.PathEscape(taskID) + "/output",
			Err:  fs.ErrNotExist,
		}
	}
	if httpResp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(httpResp.Body).Decode(&apiErr); err == nil && strings.TrimSpace(apiErr.Error) != "" {
			return "", fmt.Errorf(apiErr.Error)
		}
		return "", fmt.Errorf("GET /v1/tasks/%s/output returned %d", url.PathEscape(taskID), httpResp.StatusCode)
	}
	var resp struct {
		TaskID string `json:"taskId"`
		Output string `json:"output"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return "", err
	}
	return resp.Output, nil
}

func (c *kitchenAPIClient) ApprovePlan(planID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/approve", map[string]any{}, &resp)
}

func (c *kitchenAPIClient) RejectPlan(planID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/reject", map[string]any{}, &resp)
}

func (c *kitchenAPIClient) ReplanPlan(planID, reason string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/replan", map[string]any{"reason": reason}, &resp)
}

func (c *kitchenAPIClient) RequestReview(planID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/review", map[string]any{}, &resp)
}

func (c *kitchenAPIClient) RemediateReview(planID string, includeNits bool) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/plans/"+url.PathEscape(planID)+"/remediate-review", map[string]any{
		"includeNits": includeNits,
	}, &resp)
}

func (c *kitchenAPIClient) CancelPlan(planID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodDelete, "/v1/plans/"+url.PathEscape(planID), nil, &resp)
}

func (c *kitchenAPIClient) DeletePlan(planID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodDelete, "/v1/plans/"+url.PathEscape(planID)+"/purge", nil, &resp)
}

func (c *kitchenAPIClient) DeletePlanAndLineageBranch(planID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodDelete, "/v1/plans/"+url.PathEscape(planID)+"/purge-with-lineage", nil, &resp)
}

func (c *kitchenAPIClient) CancelTask(taskID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodDelete, "/v1/tasks/"+url.PathEscape(taskID), nil, &resp)
}

func (c *kitchenAPIClient) RetryTask(taskID string, requireFreshWorker bool) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/retry", map[string]any{
		"requireFreshWorker": requireFreshWorker,
	}, &resp)
}

func (c *kitchenAPIClient) FixConflicts(taskID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/tasks/"+url.PathEscape(taskID)+"/fix-conflicts", nil, &resp)
}

func (c *kitchenAPIClient) FixLineageConflicts(lineage string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/lineages/"+url.PathEscape(lineage)+"/fix-merge", nil, &resp)
}

func (c *kitchenAPIClient) Status(historyLimit int) (map[string]any, error) {
	path := "/v1/status"
	if historyLimit >= 0 {
		path += fmt.Sprintf("?historyLimit=%d", historyLimit)
	}
	var resp map[string]any
	return resp, c.request(http.MethodGet, path, nil, &resp)
}

func (c *kitchenAPIClient) ListQuestions() ([]pool.Question, error) {
	var resp struct {
		Questions []pool.Question `json:"questions"`
	}
	err := c.request(http.MethodGet, "/v1/questions", nil, &resp)
	return resp.Questions, err
}

func (c *kitchenAPIClient) AnswerQuestion(questionID, answer string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/questions/"+url.PathEscape(questionID)+"/answer", map[string]any{"answer": answer}, &resp)
}

func (c *kitchenAPIClient) MarkUnhelpful(questionID string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/questions/"+url.PathEscape(questionID)+"/unhelpful", map[string]any{}, &resp)
}

func (c *kitchenAPIClient) ListLineages() ([]LineageState, error) {
	var resp struct {
		Lineages []LineageState `json:"lineages"`
	}
	err := c.request(http.MethodGet, "/v1/lineages", nil, &resp)
	return resp.Lineages, err
}

func (c *kitchenAPIClient) MergeLineage(lineage string, noCommit bool) (map[string]any, error) {
	req := map[string]any{
		"noCommit": noCommit,
	}
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/lineages/"+url.PathEscape(lineage)+"/merge", req, &resp)
}

func (c *kitchenAPIClient) ReapplyLineage(lineage string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodPost, "/v1/lineages/"+url.PathEscape(lineage)+"/reapply", nil, &resp)
}

func (c *kitchenAPIClient) MergeCheck(lineage string) (map[string]any, error) {
	var resp map[string]any
	return resp, c.request(http.MethodGet, "/v1/lineages/"+url.PathEscape(lineage)+"/merge-check", nil, &resp)
}

func (c *kitchenAPIClient) ResetProviderKey(key string) (map[string]any, error) {
	provider, model, ok := strings.Cut(strings.TrimSpace(key), "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("provider key must be in provider/model form")
	}
	var resp map[string]any
	path := "/v1/providers/" + url.PathEscape(strings.TrimSpace(provider)) + "/models/" + url.PathEscape(strings.TrimSpace(model)) + "/reset"
	return resp, c.request(http.MethodPost, path, map[string]any{}, &resp)
}
