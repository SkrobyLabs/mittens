package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/adapter"
	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const (
	workerAgentPollInterval    = 3 * time.Second
	workerAgentHeartbeatPeriod = 10 * time.Second
	workerAgentRegisterRetries = 3
	workerAgentRegisterBackoff = 2 * time.Second
	workerAgentMaxPollBackoff  = 30 * time.Second
	workerAgentPollGiveUp      = 30 * time.Minute
	workerAgentReportRetries   = 3
	workerAgentReportBackoff   = time.Second

	// Team directory file names.
	teamTaskFile            = pool.WorkerTaskFile
	teamResultFile          = pool.WorkerResultFile
	teamPlanFile            = pool.WorkerPlanFile
	teamHandoverFile        = pool.WorkerHandoverFile
	teamErrorFile           = pool.WorkerErrorFile
	teamActivityLogFile     = pool.WorkerActivityLogFile
	teamActivityArchiveFile = pool.WorkerActivityArchiveFile

	teamActivityScannerMaxToken = 1 << 20
)

var failureSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)([^\s]+)`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token)\b\s*[:=]\s*([^\s,"']+)`),
	regexp.MustCompile(`(?i)\b(sk-[a-z0-9_-]+)\b`),
}

type taskFailureReport struct {
	Error        string
	FailureClass string
	Detail       json.RawMessage
	StopWorker   bool
}

// workerAgentState tracks the latest live activity for heartbeat reporting.
// The worker agent is the main orchestrator that drives the AI adapter as a subprocess.
type workerAgentState struct {
	mu              sync.Mutex
	currentActivity *pool.WorkerActivity
	currentTaskID   string
	teamDir         string // mounted team directory for file-based data exchange
}

func cloneWorkerActivity(activity *pool.WorkerActivity) *pool.WorkerActivity {
	if activity == nil {
		return nil
	}
	cp := *activity
	return &cp
}

func currentToolFromActivity(activity *pool.WorkerActivity) string {
	if activity == nil || activity.Kind != "tool" || activity.Phase != "started" {
		return ""
	}
	return activity.Name
}

func workerActivityFromAdapter(activity adapter.Activity) *pool.WorkerActivity {
	if activity.Kind == "" && activity.Phase == "" && activity.Name == "" && activity.Summary == "" {
		return nil
	}
	return &pool.WorkerActivity{
		Kind:    string(activity.Kind),
		Phase:   string(activity.Phase),
		Name:    activity.Name,
		Summary: activity.Summary,
	}
}

func sameWorkerActivity(a, b *pool.WorkerActivity) bool {
	switch {
	case a == nil || b == nil:
		return a == b
	default:
		return a.Kind == b.Kind &&
			a.Phase == b.Phase &&
			a.Name == b.Name &&
			a.Summary == b.Summary
	}
}

func (s *workerAgentState) setActivity(activity *pool.WorkerActivity) {
	normalized := cloneWorkerActivity(activity)
	s.mu.Lock()
	if sameWorkerActivity(s.currentActivity, normalized) {
		s.mu.Unlock()
		return
	}
	s.currentActivity = normalized
	teamDir := s.teamDir
	taskID := s.currentTaskID
	s.mu.Unlock()

	persistWorkerActivity(teamDir, taskID, normalized)
}

func (s *workerAgentState) setTool(name, summary string) {
	if name == "" {
		s.setActivity(nil)
		return
	}
	s.setActivity(&pool.WorkerActivity{
		Kind:    "tool",
		Phase:   "started",
		Name:    name,
		Summary: summary,
	})
}

func (s *workerAgentState) clearActivity() {
	s.setActivity(nil)
}

func (s *workerAgentState) setCurrentTask(taskID string) {
	s.mu.Lock()
	s.currentTaskID = taskID
	s.mu.Unlock()
}

func (s *workerAgentState) clearCurrentTask() {
	s.setCurrentTask("")
}

func (s *workerAgentState) snapshot() (*pool.WorkerActivity, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneWorkerActivity(s.currentActivity), currentToolFromActivity(s.currentActivity)
}

func workerRuntimeDescriptor(providerName, modelName, adapterName string) string {
	if providerName == "" {
		providerName = "default"
	}
	if modelName == "" {
		modelName = "default"
	}
	if adapterName == "" {
		adapterName = "default"
	}
	return fmt.Sprintf("provider=%s model=%s adapter=%s", providerName, modelName, adapterName)
}

// runWorkerAgent runs the worker agent poll loop.
// It registers with Kitchen, heartbeats, polls for tasks, executes them
// via an adapter, extracts structured handover, reports completion, clears
// the session, and repeats.
func runWorkerAgent(cfg *config) {
	workerID := os.Getenv("MITTENS_WORKER_ID")
	kitchenAddr := os.Getenv("MITTENS_KITCHEN_ADDR")
	adapterName := os.Getenv("MITTENS_ADAPTER")
	modelName := os.Getenv("MITTENS_MODEL")
	providerName := os.Getenv("MITTENS_PROVIDER")
	if adapterName == "" {
		adapterName = adapter.DefaultAdapterForProvider(providerName)
	}
	if adapterName == "" {
		adapterName = "claude-code"
	}

	if workerID == "" || kitchenAddr == "" {
		fmt.Fprintf(os.Stderr, "[worker] MITTENS_WORKER_ID and MITTENS_KITCHEN_ADDR are required\n")
		os.Exit(1)
	}

	state := &workerAgentState{}

	// Initialize team directory for file-based data exchange.
	if td := os.Getenv("MITTENS_TEAM_DIR"); td != "" {
		if info, err := os.Stat(td); err == nil && info.IsDir() {
			state.teamDir = td
			logInfo("worker: team dir: %s", td)
		} else {
			logWarn("worker: MITTENS_TEAM_DIR=%s not accessible, file I/O disabled", td)
		}
	}

	// Prefer per-worker token over shared broker token.
	kitchenToken := os.Getenv("MITTENS_WORKER_TOKEN")
	if kitchenToken == "" {
		kitchenToken = cfg.BrokerToken
	}
	client := newKitchenClient(kitchenAddr, kitchenToken)

	ad, err := adapter.New(adapterName, cfg.HostWorkspace, func(c *adapter.Config) {
		if skipPermsFlag := os.Getenv("MITTENS_SKIP_PERMS_FLAG"); skipPermsFlag != "" {
			c.SkipPermsFlag = skipPermsFlag
		}
		c.Model = modelName
		c.OnActivity = func(activity adapter.Activity) {
			state.setActivity(workerActivityFromAdapter(activity))
		}
		c.OnToolUse = func(toolName, inputSummary string) {
			logInfo("worker: %s tool: %s → %s", workerID, toolName, inputSummary)
			state.setTool(toolName, inputSummary)
		}
		c.OnLog = func(msg string) {
			logInfo("worker %s: %s", workerID, msg)
		}
		// Session reuse configuration from environment.
		if os.Getenv("MITTENS_SESSION_REUSE") == "1" {
			c.SessionReuse = adapter.SessionReuseConfig{
				Enabled:      true,
				TTL:          parseDurationEnv("MITTENS_SESSION_REUSE_TTL", 300*time.Second),
				MaxTasks:     parseIntEnv("MITTENS_SESSION_REUSE_MAX_TASKS", 3),
				MaxTokens:    parseIntEnv("MITTENS_SESSION_REUSE_MAX_TOKENS", 100000),
				SameRoleOnly: os.Getenv("MITTENS_SESSION_REUSE_SAME_ROLE") != "0",
			}
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[worker] create adapter: %v\n", err)
		os.Exit(1)
	}

	// 1. Register with retries.
	if err := registerWithRetries(client, workerID, cfg.ContainerName); err != nil {
		fmt.Fprintf(os.Stderr, "[worker] registration failed: %v\n", err)
		os.Exit(1)
	}
	logInfo("worker: registered as %s", workerID)
	logInfo("worker: %s runtime: %s", workerID, workerRuntimeDescriptor(providerName, modelName, adapterName))

	// 2. Start heartbeat goroutine with cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go heartbeatLoop(ctx, cancel, client, workerID, state)

	// 3. Task loop — when it returns, cancel stops the heartbeat.
	taskLoop(ctx, client, ad, workerID, state)
}

func registerWithRetries(client *kitchenClient, workerID, containerName string) error {
	var lastErr error
	for i := 0; i < workerAgentRegisterRetries; i++ {
		if err := client.Register(workerID, containerName); err != nil {
			lastErr = err
			logWarn("worker: register attempt %d failed: %v", i+1, err)
			time.Sleep(workerAgentRegisterBackoff)
			continue
		}
		return nil
	}
	return fmt.Errorf("register failed after %d attempts: %w", workerAgentRegisterRetries, lastErr)
}

func heartbeatLoop(ctx context.Context, cancel context.CancelFunc, client *kitchenClient, workerID string, state *workerAgentState) {
	ticker := time.NewTicker(workerAgentHeartbeatPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			activity, currentTool := state.snapshot()
			if err := client.Heartbeat(workerID, activity, currentTool); err != nil {
				if err == errWorkerKilled {
					logWarn("worker: Kitchen reports worker killed, shutting down")
					cancel()
					return
				}
				logWarn("worker: heartbeat: %v", err)
			}
		}
	}
}

func taskLoop(ctx context.Context, client *kitchenClient, ad adapter.Adapter, workerID string, state *workerAgentState) {
	pollBackoff := workerAgentPollInterval
	var consecutiveFailStart time.Time

	// Track role of last executed task for SameRoleOnly session reuse enforcement.
	sameRoleOnly := os.Getenv("MITTENS_SESSION_REUSE") == "1" && os.Getenv("MITTENS_SESSION_REUSE_SAME_ROLE") != "0"
	var lastTaskRole string

	for {
		select {
		case <-ctx.Done():
			logInfo("worker: context cancelled, exiting task loop")
			return
		default:
		}

		poll, err := client.PollTask(workerID)
		switch {
		case err == errWorkerKilled:
			logWarn("worker: Kitchen reports worker killed during poll, exiting")
			return

		case err != nil:
			// Actual error — count toward give-up timer.
			logWarn("worker: poll error: %v", err)
			time.Sleep(pollBackoff)
			if consecutiveFailStart.IsZero() {
				consecutiveFailStart = time.Now()
			} else if time.Since(consecutiveFailStart) > workerAgentPollGiveUp {
				logWarn("worker: poll errors for %v, exiting", workerAgentPollGiveUp)
				return
			}
			if pollBackoff < workerAgentMaxPollBackoff {
				pollBackoff *= 2
				if pollBackoff > workerAgentMaxPollBackoff {
					pollBackoff = workerAgentMaxPollBackoff
				}
			}
			continue

		case poll.Recycle:
			logInfo("worker: %s recycling adapter session", workerID)
			if err := ad.ForceClean(); err != nil {
				logWarn("worker: recycle force clean: %v", err)
			}
			lastTaskRole = ""
			pollBackoff = workerAgentPollInterval
			consecutiveFailStart = time.Time{}
			continue

		case poll.Task == nil:
			// 204 No Content — no task yet, keep waiting. Reset give-up timer.
			time.Sleep(pollBackoff)
			consecutiveFailStart = time.Time{}
			if pollBackoff < workerAgentMaxPollBackoff {
				pollBackoff *= 2
				if pollBackoff > workerAgentMaxPollBackoff {
					pollBackoff = workerAgentMaxPollBackoff
				}
			}
			continue
		}

		// Got a task — reset everything.
		pollBackoff = workerAgentPollInterval
		consecutiveFailStart = time.Time{}
		task := poll.Task

		// Force-clean session when role changes under SameRoleOnly policy.
		if sameRoleOnly && lastTaskRole != "" && task.Role != lastTaskRole {
			logInfo("worker: %s role changed %s→%s, forcing session clean", workerID, lastTaskRole, task.Role)
			if err := ad.ForceClean(); err != nil {
				logWarn("worker: force clean on role change: %v", err)
			}
		}

		logInfo("worker: %s executing task %s", workerID, task.ID)
		stopWorker := executeTask(client, ad, workerID, task, state)
		lastTaskRole = task.Role
		if stopWorker {
			logWarn("worker: %s stopping after terminal authentication failure", workerID)
			return
		}
	}
}

func executeTask(client *kitchenClient, ad adapter.Adapter, workerID string, task *pool.Task, state *workerAgentState) bool {
	state.setCurrentTask(task.ID)
	defer state.clearCurrentTask()
	defer state.clearActivity()

	// Clean stale files from previous task.
	cleanTeamDir(state.teamDir)

	// Extract prior context from handover.
	var priorContext string
	if task.Handover != nil {
		priorContext = task.Handover.ContextForNext
	}

	// Write task.md to team dir before executing.
	writeTaskFile(state.teamDir, task, priorContext)

	prompt := task.Prompt
	if task.Status == pool.TaskReviewing {
		implementerSummary := ""
		if task.Result != nil {
			implementerSummary = task.Result.Summary
		}
		prompt = adapter.BuildReviewPrompt(task.Prompt, implementerSummary, priorContext)
		priorContext = ""
	} else if task.Role == "planner" {
		priorContext = ""
	}

	// Execute — BuildPrompt is called inside Execute(), do not call it here.
	ctx := context.Background()
	expectsCouncilTurn := task.Role == "planner" && adapterHasCouncilTurnPrompt(prompt)
	expectsReviewCouncilTurn := task.Role == "reviewer" && adapterHasReviewCouncilTurnPrompt(prompt)
	var (
		result                adapter.Result
		err                   error
		councilArtifact       *adapter.CouncilTurnArtifact
		reviewCouncilArtifact *adapter.ReviewCouncilTurnArtifact
		verdict               string
		feedback              string
		severity              string
	)
	switch {
	case expectsCouncilTurn:
		councilArtifact, result, err = adapter.ExecuteForCouncilTurn(ctx, ad, prompt, priorContext, func(msg string) {
			logInfo("adapter retry: %s", msg)
		})
	case expectsReviewCouncilTurn:
		reviewCouncilArtifact, result, err = adapter.ExecuteForReviewCouncilTurn(ctx, ad, prompt, priorContext, func(msg string) {
			logInfo("adapter retry: %s", msg)
		})
	case task.Status == pool.TaskReviewing:
		verdict, feedback, severity, result, err = adapter.ExecuteForReviewVerdict(ctx, ad, prompt, priorContext, func(msg string) {
			logInfo("adapter retry: %s", msg)
		})
	default:
		result, err = ad.Execute(ctx, prompt, priorContext)
	}
	if err != nil {
		if result.Output != "" {
			writeTeamFileAtomic(state.teamDir, teamResultFile, []byte(result.Output))
		}
		if attempts := adapter.ExtractionAttempts(err); attempts > 0 {
			reportMsg := fmt.Sprintf("invalid review verdict (after %d attempts): %v", attempts, err)
			switch {
			case expectsCouncilTurn:
				reportMsg = fmt.Sprintf("invalid plan artifact (after %d attempts): %v", attempts, err)
			case expectsReviewCouncilTurn:
				reportMsg = fmt.Sprintf("invalid review council artifact (after %d attempts): %v", attempts, err)
			}
			logWarn("worker: %s task %s failed: %v", workerID, task.ID, err)
			writeTeamFileAtomic(state.teamDir, teamErrorFile, []byte(reportMsg))
			reportFailWithRetries(client, workerID, task.ID, taskFailureReport{Error: reportMsg})
			if err := ad.ClearSession(); err != nil {
				logWarn("worker: clear session: %v", err)
			}
			return false
		}

		logWarn("worker: %s task %s failed: %v", workerID, task.ID, err)
		report := classifyTaskFailure(err)
		writeTeamFileAtomic(state.teamDir, teamErrorFile, []byte(report.Error))
		reportFailWithRetries(client, workerID, task.ID, report)
		if err := ad.ClearSession(); err != nil {
			logWarn("worker: clear session: %v", err)
		}
		return report.StopWorker
	} else {
		// Write result.txt atomically.
		writeTeamFileAtomic(state.teamDir, teamResultFile, []byte(result.Output))

		// Extract and write handover.json atomically.
		handover := adapter.ExtractHandover(task.ID, result.Output)
		if handover != nil {
			if data, err := json.Marshal(handover); err == nil {
				writeTeamFileAtomic(state.teamDir, teamHandoverFile, data)
			}
		}

		if expectsCouncilTurn {
			if data, err := json.MarshalIndent(councilArtifact, "", "  "); err == nil {
				writeTeamFileAtomic(state.teamDir, teamPlanFile, data)
			}
		}

		if task.Status == pool.TaskReviewing {
			reportReviewWithRetries(client, workerID, task.ID, verdict, feedback, severity)
		} else {
			// Signal-only completion — data is on filesystem.
			_ = reviewCouncilArtifact
			reportCompleteWithRetries(client, workerID, task.ID)
		}
		logInfo("worker: %s completed task %s", workerID, task.ID)
	}

	// Clear session for next task.
	if err := ad.ClearSession(); err != nil {
		logWarn("worker: clear session: %v", err)
	}
	return false
}

func adapterHasCouncilTurnPrompt(prompt string) bool {
	return hasSubstringFold(prompt, "<council_turn>")
}

func adapterHasReviewCouncilTurnPrompt(prompt string) bool {
	return hasSubstringFold(prompt, "<review_council_turn>")
}

func hasSubstringFold(s, needle string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(needle))
}

// cleanTeamDir removes stale files from previous task cycle.
func cleanTeamDir(teamDir string) {
	if teamDir == "" {
		return
	}
	for _, name := range []string{teamTaskFile, teamResultFile, teamPlanFile, teamHandoverFile, teamErrorFile} {
		os.Remove(filepath.Join(teamDir, name))
	}
}

// writeTaskFile writes task.md with YAML frontmatter to the team dir.
func writeTaskFile(teamDir string, task *pool.Task, priorContext string) {
	if teamDir == "" {
		return
	}
	content := fmt.Sprintf("---\ntaskId: %s\nrole: %s\npriority: %d\n---\n\n%s",
		task.ID, task.Role, task.Priority, task.Prompt)
	if priorContext != "" {
		content += "\n\n## Prior Context\n" + priorContext
	}
	writeTeamFileAtomic(teamDir, teamTaskFile, []byte(content))
}

// writeTeamFileAtomic writes data to teamDir/name via atomic rename.
func writeTeamFileAtomic(teamDir, name string, data []byte) {
	if teamDir == "" {
		return
	}
	target := filepath.Join(teamDir, name)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logWarn("worker: write %s: %v", name, err)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		logWarn("worker: rename %s: %v", name, err)
	}
}

func persistWorkerActivity(teamDir, taskID string, activity *pool.WorkerActivity) {
	if teamDir == "" || activity == nil {
		return
	}
	if rotateTeamActivityLog(teamDir) {
		logInfo("worker: rotated %s", teamActivityLogFile)
	}

	record := pool.WorkerActivityRecord{
		RecordedAt: time.Now().UTC(),
		TaskID:     taskID,
		Activity:   *activity,
	}
	path := filepath.Join(teamDir, teamActivityLogFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		logWarn("worker: open %s: %v", teamActivityLogFile, err)
		return
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(record); err != nil {
		logWarn("worker: append %s: %v", teamActivityLogFile, err)
	}
}

func rotateTeamActivityLog(teamDir string) bool {
	currentPath := filepath.Join(teamDir, teamActivityLogFile)
	if !teamActivityLogNeedsRotation(currentPath) {
		return false
	}

	archivePath := filepath.Join(teamDir, teamActivityArchiveFile)
	if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
		logWarn("worker: remove %s: %v", teamActivityArchiveFile, err)
		return false
	}
	if err := os.Rename(currentPath, archivePath); err != nil {
		logWarn("worker: rotate %s: %v", teamActivityLogFile, err)
		return false
	}
	return true
}

func teamActivityLogNeedsRotation(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), teamActivityScannerMaxToken)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount >= pool.WorkerActivityLogMaxEntries {
			return true
		}
	}
	if err := scanner.Err(); err != nil {
		logWarn("worker: scan %s: %v", teamActivityLogFile, err)
	}
	return false
}

func reportCompleteWithRetries(client *kitchenClient, workerID, taskID string) {
	for i := 0; i < workerAgentReportRetries; i++ {
		if err := client.ReportComplete(workerID, taskID); err != nil {
			logWarn("worker: report complete attempt %d: %v", i+1, err)
			time.Sleep(workerAgentReportBackoff)
			continue
		}
		return
	}
	logWarn("worker: failed to report completion for task %s after retries", taskID)
}

func reportFailWithRetries(client *kitchenClient, workerID, taskID string, report taskFailureReport) {
	for i := 0; i < workerAgentReportRetries; i++ {
		if err := client.ReportFail(workerID, taskID, report); err != nil {
			logWarn("worker: report fail attempt %d: %v", i+1, err)
			time.Sleep(workerAgentReportBackoff)
			continue
		}
		return
	}
	logWarn("worker: failed to report failure for task %s after retries", taskID)
}

func reportReviewWithRetries(client *kitchenClient, workerID, taskID, verdict, feedback, severity string) {
	for i := 0; i < workerAgentReportRetries; i++ {
		if err := client.ReportReview(workerID, taskID, verdict, feedback, severity); err != nil {
			logWarn("worker: report review attempt %d: %v", i+1, err)
			time.Sleep(workerAgentReportBackoff)
			continue
		}
		return
	}
	logWarn("worker: failed to report review for task %s after retries", taskID)
}

func classifyTaskFailure(err error) taskFailureReport {
	if err == nil {
		detail := mustMarshalFailureDetail(pool.FailureDetail{
			Summary: "task execution failed",
		})
		return taskFailureReport{
			Error:  "task execution failed",
			Detail: detail,
		}
	}

	raw := redactFailureMessage(strings.TrimSpace(err.Error()))
	report := taskFailureReport{
		Error: sanitizeFailureMessage(raw),
	}
	detail := pool.FailureDetail{
		Summary: report.Error,
	}
	if authMode, stopWorker := authFailureMode(raw); authMode != "" {
		detail.AuthMode = authMode
		detail.Signals.AuthFailure = true
		report.FailureClass = "auth"
		report.StopWorker = stopWorker
		if stopWorker {
			report.Error = "authentication failure: token expired or refresh token reused; run codex logout and sign in again on the host, then restart the worker"
			detail.Summary = report.Error
		}
	}
	report.Detail = mustMarshalFailureDetail(detail)
	return report
}

func requiresAuthReset(msg string) bool {
	lower := strings.ToLower(msg)
	for _, needle := range []string{
		"refresh_token_reused",
		"token_expired",
		"log out and sign in again",
		"provided authentication token is expired",
		"access token could not be refreshed",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func authFailureMode(msg string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return "", false
	}
	if requiresAuthReset(lower) {
		return "hard", true
	}
	for _, needle := range []string{
		"authentication_error",
		"failed to authenticate",
		"invalid authentication credentials",
		"invalid api key",
		"api_key_invalid",
		"unauthorized",
		"unauthenticated",
		"forbidden",
		"permission_denied",
		"auth error",
		"authentication failure",
	} {
		if strings.Contains(lower, needle) {
			return "soft", false
		}
	}
	return "", false
}

func mustMarshalFailureDetail(detail pool.FailureDetail) json.RawMessage {
	data, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return data
}

func redactFailureMessage(msg string) string {
	redacted := strings.TrimSpace(msg)
	if redacted == "" {
		return ""
	}
	for _, pattern := range failureSecretPatterns {
		switch pattern.String() {
		case `(?i)(authorization:\s*bearer\s+)([^\s]+)`:
			redacted = pattern.ReplaceAllString(redacted, "${1}[REDACTED]")
		case `(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token)\b\s*[:=]\s*([^\s,"']+)`:
			redacted = pattern.ReplaceAllString(redacted, "${1}=[REDACTED]")
		default:
			redacted = pattern.ReplaceAllString(redacted, "[REDACTED]")
		}
	}
	return redacted
}

// parseDurationEnv reads an env var as seconds and returns a time.Duration.
// Falls back to defaultVal if unset or unparseable.
func parseDurationEnv(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	secs, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return time.Duration(secs) * time.Second
}

// parseIntEnv reads an env var as an integer.
// Falls back to defaultVal if unset or unparseable.
func parseIntEnv(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}
