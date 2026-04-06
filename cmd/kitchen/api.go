package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const apiMaxBodyBytes = 1 << 20

func (k *Kitchen) NewAPIHandler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ideas", k.withAPIAuth(token, k.handleSubmitIdea))
	mux.HandleFunc("GET /v1/plans", k.withAPIAuth(token, k.handleListPlans))
	mux.HandleFunc("GET /v1/plans/{id}", k.withAPIAuth(token, k.handleGetPlan))
	mux.HandleFunc("GET /v1/plans/{id}/history", k.withAPIAuth(token, k.handlePlanHistory))
	mux.HandleFunc("GET /v1/plans/{id}/evidence", k.withAPIAuth(token, k.handlePlanEvidence))
	mux.HandleFunc("GET /v1/tasks/{id}/activity", k.withAPIAuth(token, k.handleTaskActivity))
	mux.HandleFunc("POST /v1/tasks/{id}/retry", k.withAPIAuth(token, k.handleRetryTask))
	mux.HandleFunc("POST /v1/tasks/{id}/fix-conflicts", k.withAPIAuth(token, k.handleFixConflicts))
	mux.HandleFunc("DELETE /v1/tasks/{id}", k.withAPIAuth(token, k.handleCancelTask))
	mux.HandleFunc("DELETE /v1/plans/{id}", k.withAPIAuth(token, k.handleCancelPlan))
	mux.HandleFunc("DELETE /v1/plans/{id}/purge", k.withAPIAuth(token, k.handleDeletePlan))
	mux.HandleFunc("POST /v1/plans/{id}/replan", k.withAPIAuth(token, k.handleReplanPlan))
	mux.HandleFunc("POST /v1/plans/{id}/approve", k.withAPIAuth(token, k.handleApprovePlan))
	mux.HandleFunc("POST /v1/plans/{id}/affinity/invalidate", k.withAPIAuth(token, k.handleInvalidateAffinity))
	mux.HandleFunc("POST /v1/plans/{id}/reject", k.withAPIAuth(token, k.handleRejectPlan))
	mux.HandleFunc("GET /v1/questions", k.withAPIAuth(token, k.handleListQuestions))
	mux.HandleFunc("POST /v1/questions/{id}/answer", k.withAPIAuth(token, k.handleAnswerQuestion))
	mux.HandleFunc("POST /v1/questions/{id}/unhelpful", k.withAPIAuth(token, k.handleMarkUnhelpful))
	mux.HandleFunc("GET /v1/status", k.withAPIAuth(token, k.handleStatus))
	mux.HandleFunc("GET /v1/queue", k.withAPIAuth(token, k.handleQueue))
	mux.HandleFunc("GET /v1/workers", k.withAPIAuth(token, k.handleWorkers))
	mux.HandleFunc("GET /v1/events", k.withAPIAuth(token, k.handleEvents))
	mux.HandleFunc("GET /v1/meta", k.withAPIAuth(token, k.handleMeta))
	mux.HandleFunc("GET /v1/lineages", k.withAPIAuth(token, k.handleLineages))
	mux.HandleFunc("POST /v1/lineages/{name}/merge", k.withAPIAuth(token, k.handleMergeLineage))
	mux.HandleFunc("POST /v1/lineages/{name}/fix-merge", k.withAPIAuth(token, k.handleFixLineageConflicts))
	mux.HandleFunc("GET /v1/lineages/{name}/merge-check", k.withAPIAuth(token, k.handleMergeCheck))
	mux.HandleFunc("GET /v1/providers/health", k.withAPIAuth(token, k.handleProviderHealth))
	mux.HandleFunc("POST /v1/providers/{provider}/models/{model}/reset", k.withAPIAuth(token, k.handleProviderReset))
	return mux
}

func (k *Kitchen) withAPIAuth(token string, handler http.HandlerFunc) http.HandlerFunc {
	token = strings.TrimSpace(token)
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			reqToken := strings.TrimSpace(r.Header.Get("X-Kitchen-Token"))
			if reqToken == "" {
				const prefix = "Bearer "
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, prefix) {
					reqToken = strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
				}
			}
			if subtle.ConstantTimeCompare([]byte(reqToken), []byte(token)) != 1 {
				writeAPIError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		handler(w, r)
	}
}

func (k *Kitchen) decodeAPIRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, apiMaxBodyBytes+1))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "read error")
		return false
	}
	if len(body) > apiMaxBodyBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return false
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeAPIJSON(w, status, map[string]string{"error": message})
}

func apiErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	case strings.Contains(msg, "not implemented"):
		return http.StatusNotImplemented
	case strings.Contains(msg, "already"), strings.Contains(msg, "conflict"), strings.Contains(msg, "active plan cancellation"):
		return http.StatusConflict
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"):
		return http.StatusUnauthorized
	case strings.Contains(msg, "must not"), strings.Contains(msg, "invalid"), strings.Contains(msg, "missing"), strings.Contains(msg, "unknown"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (k *Kitchen) handleSubmitIdea(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Idea               string `json:"idea"`
		Lineage            string `json:"lineage"`
		Auto               bool   `json:"auto"`
		Review             bool   `json:"review"`
		ReviewRounds       int    `json:"reviewRounds"`
		MaxReviewRevisions *int   `json:"maxReviewRevisions"`
		ImplReview         bool   `json:"implReview"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	maxReviewRevisions := -1
	if req.MaxReviewRevisions != nil {
		maxReviewRevisions = *req.MaxReviewRevisions
	}
	bundle, err := k.SubmitIdea(req.Idea, req.Lineage, req.Auto, req.Review, req.ReviewRounds, maxReviewRevisions, req.ImplReview)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	resp := map[string]any{
		"planId":  bundle.Plan.PlanID,
		"state":   bundle.Execution.State,
		"lineage": bundle.Plan.Lineage,
	}
	if bundle.Execution.ReviewRequested {
		resp["reviewStatus"] = bundle.Execution.ReviewStatus
		resp["reviewRounds"] = bundle.Execution.ReviewRounds
		resp["maxReviewRevisions"] = bundle.Execution.MaxReviewRevisions
		resp["reviewFindings"] = bundle.Execution.ReviewFindings
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (k *Kitchen) handleListPlans(w http.ResponseWriter, r *http.Request) {
	includeCompleted := r.URL.Query().Get("completed") == "true"
	plans, err := k.ListPlans(includeCompleted)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (k *Kitchen) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	detail, err := k.PlanDetail(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, detail)
}

func (k *Kitchen) handlePlanEvidence(w http.ResponseWriter, r *http.Request) {
	tier, err := normalizeEvidenceTier(r.URL.Query().Get("tier"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	evidence, err := k.EvidenceWithTier(r.PathValue("id"), tier)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, evidence)
}

func (k *Kitchen) handlePlanHistory(w http.ResponseWriter, r *http.Request) {
	cycle := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("cycle")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeAPIError(w, http.StatusBadRequest, "cycle must be a non-negative integer")
			return
		}
		cycle = n
	}
	history, err := k.PlanHistory(r.PathValue("id"), cycle)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"planId":  r.PathValue("id"),
		"cycle":   cycle,
		"history": history,
	})
}

func (k *Kitchen) handleTaskActivity(w http.ResponseWriter, r *http.Request) {
	transcript, err := k.TaskActivity(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"taskId":     r.PathValue("id"),
		"transcript": transcript,
	})
}

func (k *Kitchen) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequireFreshWorker *bool `json:"requireFreshWorker"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	requireFreshWorker := true
	if req.RequireFreshWorker != nil {
		requireFreshWorker = *req.RequireFreshWorker
	}
	taskID := r.PathValue("id")
	if k.pm != nil {
		task, ok := k.pm.Task(taskID)
		if !ok {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("task %s not found", taskID))
			return
		}
		if task.Status == pool.TaskQueued && task.RetryCount > 0 {
			writeAPIJSON(w, http.StatusOK, map[string]any{
				"status":             "retried",
				"taskId":             taskID,
				"requireFreshWorker": task.RequireFreshWorker,
				"alreadyRetried":     true,
			})
			return
		}
		if task.Status != pool.TaskFailed {
			writeAPIJSON(w, http.StatusConflict, map[string]any{
				"error":         "task_not_failed",
				"taskId":        taskID,
				"currentStatus": task.Status,
			})
			return
		}
	}
	if err := k.RetryTask(taskID, requireFreshWorker); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"status":             "retried",
		"taskId":             taskID,
		"requireFreshWorker": requireFreshWorker,
	})
}

func (k *Kitchen) handleFixConflicts(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	newTaskID, err := k.FixConflicts(taskID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"newTaskId": newTaskID})
}

func (k *Kitchen) handleFixLineageConflicts(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	newTaskID, err := k.FixLineageConflicts(name)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"newTaskId": newTaskID})
}

func (k *Kitchen) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	if err := k.CancelTask(r.PathValue("id")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (k *Kitchen) handleCancelPlan(w http.ResponseWriter, r *http.Request) {
	if err := k.CancelPlan(r.PathValue("id")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (k *Kitchen) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	if err := k.DeletePlan(r.PathValue("id")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (k *Kitchen) handleReplanPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	newPlanID, err := k.Replan(r.PathValue("id"), req.Reason)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"newPlanId": newPlanID})
}

func (k *Kitchen) handleApprovePlan(w http.ResponseWriter, r *http.Request) {
	if err := k.ApprovePlan(r.PathValue("id")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": planStateActive})
}

func (k *Kitchen) handleInvalidateAffinity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	if err := k.InvalidateAffinity(r.PathValue("id"), req.Reason); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "invalidated"})
}

func (k *Kitchen) handleRejectPlan(w http.ResponseWriter, r *http.Request) {
	if err := k.RejectPlan(r.PathValue("id")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": planStateRejected})
}

func (k *Kitchen) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	writeAPIJSON(w, http.StatusOK, map[string]any{"questions": k.ListQuestions()})
}

func (k *Kitchen) handleStatus(w http.ResponseWriter, r *http.Request) {
	historyLimit, ok := parseSnapshotHistoryLimit(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "historyLimit must be >= -1")
		return
	}
	snapshot, err := k.StatusSnapshotWithLimit(historyLimit)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, snapshot)
}

func (k *Kitchen) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Answer string `json:"answer"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	if err := k.AnswerQuestion(r.PathValue("id"), req.Answer); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "answered"})
}

func (k *Kitchen) handleMarkUnhelpful(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	if err := k.MarkUnhelpful(r.PathValue("id")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (k *Kitchen) handleQueue(w http.ResponseWriter, r *http.Request) {
	writeAPIJSON(w, http.StatusOK, k.QueueSnapshot())
}

func (k *Kitchen) handleWorkers(w http.ResponseWriter, r *http.Request) {
	workers := k.pm.Workers()
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	writeAPIJSON(w, http.StatusOK, map[string]any{"workers": workers})
}

func (k *Kitchen) handleMeta(w http.ResponseWriter, r *http.Request) {
	if k == nil {
		writeAPIError(w, http.StatusInternalServerError, "kitchen not configured")
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"version":      version,
		"commit":       commit,
		"date":         date,
		"config":       k.cfg,
		"capabilities": kitchenCapabilities(),
		"paths": map[string]any{
			"home":      k.paths.HomeDir,
			"config":    k.paths.ConfigPath,
			"state":     k.paths.StateDir,
			"projects":  k.paths.ProjectsDir,
			"worktrees": k.paths.WorktreesDir,
			"repo":      k.repoPath,
		},
	})
}

func (k *Kitchen) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	historyLimit, ok := parseSnapshotHistoryLimit(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "historyLimit must be an integer >= -1")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sendEvent := func(event string, data any) bool {
		payload, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !sendEvent("snapshot", k.eventSnapshot(time.Now().UTC(), historyLimit)) {
		return
	}

	if k == nil {
		return
	}
	kitchenSub, kitchenCancel := k.SubscribeNotifications(32)
	defer kitchenCancel()

	var poolSub <-chan pool.Notification
	if k.pm != nil {
		sub, cancel := k.pm.SubscribeNotifications(32)
		defer cancel()
		poolSub = sub
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case n, ok := <-kitchenSub:
			if !ok {
				kitchenSub = nil
				continue
			}
			if !sendEvent(n.Type, k.eventPayload(n, time.Now().UTC())) {
				return
			}
		case n, ok := <-poolSub:
			if !ok {
				poolSub = nil
				continue
			}
			if !sendEvent(n.Type, k.eventPayload(n, time.Now().UTC())) {
				return
			}
		case ts := <-keepAlive.C:
			if _, err := fmt.Fprintf(w, ": keepalive %s\n\n", ts.UTC().Format(time.RFC3339Nano)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func parseSnapshotHistoryLimit(r *http.Request) (int, bool) {
	if r == nil {
		return -1, true
	}
	raw := strings.TrimSpace(r.URL.Query().Get("historyLimit"))
	if raw == "" {
		return -1, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < -1 {
		return 0, false
	}
	return n, true
}

func (k *Kitchen) eventSnapshot(ts time.Time, historyLimit int) map[string]any {
	workers := 0
	if k != nil && k.pm != nil {
		workers = len(k.pm.Workers())
	}
	plans := []PlanProgress(nil)
	historyLimit, snapshotMeta := k.resolveSnapshotHistoryLimit(historyLimit)
	if k != nil && k.planStore != nil {
		if progress, err := k.OpenPlanProgressWithLimit(historyLimit); err == nil {
			plans = progress
		}
	}
	return map[string]any{
		"queue":    k.QueueSnapshot(),
		"workers":  workers,
		"plans":    plans,
		"snapshot": snapshotMeta,
		"time":     ts.UTC(),
	}
}

func (k *Kitchen) eventPayload(n pool.Notification, ts time.Time) map[string]any {
	payload := map[string]any{
		"type":      n.Type,
		"id":        n.ID,
		"message":   n.Message,
		"formatted": formatNotification(n),
		"level":     notificationLevel(n.Type),
		"queue":     k.QueueSnapshot(),
		"time":      ts.UTC(),
	}
	if planID := k.planIDForNotification(n); planID != "" {
		payload["planId"] = planID
		if detail, err := k.PlanDetail(planID); err == nil {
			payload["progress"] = detail.Progress
			if entry, ok := historyEntryForNotification(detail.History, n.Type); ok {
				payload["historyEntry"] = entry
			}
		}
	}
	return payload
}

func (k *Kitchen) planIDForNotification(n pool.Notification) string {
	if k == nil {
		return ""
	}
	if strings.HasPrefix(n.Type, "plan_") && strings.TrimSpace(n.ID) != "" {
		return n.ID
	}
	if n.Type == "question" {
		planID, err := k.planIDForQuestion(n.ID)
		if err == nil {
			return planID
		}
	}
	if k.pm == nil || strings.TrimSpace(n.ID) == "" {
		return ""
	}
	task, ok := k.pm.Task(n.ID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(task.PlanID)
}

func (k *Kitchen) handleLineages(w http.ResponseWriter, r *http.Request) {
	lineages, err := k.lineageMgr.List()
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"lineages": lineages})
}

func (k *Kitchen) handleMergeLineage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode     string `json:"mode"`
		NoCommit bool   `json:"noCommit"`
	}
	if !k.decodeAPIRequest(w, r, &req) {
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "direct"
	}
	if mode != "direct" && mode != "squash" {
		writeAPIError(w, http.StatusBadRequest, "mode must be direct or squash")
		return
	}
	var (
		resp map[string]any
		err  error
	)
	if req.NoCommit {
		resp, err = k.PreviewMergeLineage(r.PathValue("name"), mode)
	} else {
		resp, err = k.MergeLineage(r.PathValue("name"), mode)
	}
	if err != nil {
		status := apiErrorStatus(err)
		resp := map[string]any{"status": "failed"}
		if strings.Contains(strings.ToLower(err.Error()), "merge conflicts:") {
			resp["conflicts"] = parseConflictList(err.Error())
		} else {
			resp["error"] = err.Error()
		}
		writeAPIJSON(w, status, resp)
		return
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

func (k *Kitchen) handleMergeCheck(w http.ResponseWriter, r *http.Request) {
	gitMgr, err := k.gitManager()
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	lineage := r.PathValue("name")
	baseBranch := k.baseBranchForLineage(lineage)
	clean, conflicts, err := gitMgr.MergeCheck(lineage, baseBranch)
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	currentHead, err := runGit(k.repoPath, "rev-parse", lineageBranchName(lineage))
	if err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"clean":       clean,
		"conflicts":   conflicts,
		"baseBranch":  baseBranch,
		"currentHead": strings.TrimSpace(currentHead),
	})
}

func (k *Kitchen) handleProviderHealth(w http.ResponseWriter, r *http.Request) {
	if k.health == nil {
		writeAPIJSON(w, http.StatusOK, map[string]any{"providers": map[string]HealthEntry{}})
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"providers": k.health.Snapshot()})
}

func (k *Kitchen) handleProviderReset(w http.ResponseWriter, r *http.Request) {
	if err := k.ResetProviderKey(r.PathValue("provider") + "/" + r.PathValue("model")); err != nil {
		writeAPIError(w, apiErrorStatus(err), err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func parseConflictList(message string) []string {
	const marker = "merge conflicts:"
	idx := strings.Index(strings.ToLower(message), marker)
	if idx < 0 {
		return nil
	}
	raw := strings.TrimSpace(message[idx+len(marker):])
	if raw == "" {
		return nil
	}
	var conflicts []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			conflicts = append(conflicts, item)
		}
	}
	return conflicts
}
