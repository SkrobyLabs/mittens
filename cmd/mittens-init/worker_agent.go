package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SkrobyLabs/mittens/internal/adapter"
	"github.com/SkrobyLabs/mittens/internal/pool"
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
	teamHandoverFile        = pool.WorkerHandoverFile
	teamErrorFile           = pool.WorkerErrorFile
	teamActivityLogFile     = pool.WorkerActivityLogFile
	teamActivityArchiveFile = pool.WorkerActivityArchiveFile

	teamActivityScannerMaxToken = 1 << 20
)

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
// It registers with the leader, heartbeats, polls for tasks, executes them
// via an adapter, extracts structured handover, reports completion, clears
// the session, and repeats.
func runWorkerAgent(cfg *config) {
	workerID := os.Getenv("MITTENS_WORKER_ID")
	leaderAddr := os.Getenv("MITTENS_LEADER_ADDR")
	adapterName := os.Getenv("MITTENS_ADAPTER")
	modelName := os.Getenv("MITTENS_MODEL")
	providerName := os.Getenv("MITTENS_PROVIDER")
	if adapterName == "" {
		adapterName = adapter.DefaultAdapterForProvider(providerName)
	}
	if adapterName == "" {
		adapterName = "claude-code"
	}

	if workerID == "" || leaderAddr == "" {
		fmt.Fprintf(os.Stderr, "[worker] MITTENS_WORKER_ID and MITTENS_LEADER_ADDR are required\n")
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
	leaderToken := os.Getenv("MITTENS_WORKER_TOKEN")
	if leaderToken == "" {
		leaderToken = cfg.BrokerToken
	}
	client := newLeaderClient(leaderAddr, leaderToken)

	ad, err := adapter.New(adapterName, cfg.HostWorkspace, func(c *adapter.Config) {
		c.SkipPermsFlag = cfg.AISkipPermsFlag
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

func registerWithRetries(client *leaderClient, workerID, containerName string) error {
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

func heartbeatLoop(ctx context.Context, cancel context.CancelFunc, client *leaderClient, workerID string, state *workerAgentState) {
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
					logWarn("worker: leader reports worker killed, shutting down")
					cancel()
					return
				}
				logWarn("worker: heartbeat: %v", err)
			}
		}
	}
}

func taskLoop(ctx context.Context, client *leaderClient, ad adapter.Adapter, workerID string, state *workerAgentState) {
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

		task, err := client.PollTask(workerID)
		switch {
		case err == errWorkerKilled:
			logWarn("worker: leader reports worker killed during poll, exiting")
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

		case task == nil:
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

func executeTask(client *leaderClient, ad adapter.Adapter, workerID string, task *pool.Task, state *workerAgentState) bool {
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
	}

	// Execute — BuildPrompt is called inside Execute(), do not call it here.
	ctx := context.Background()
	result, err := ad.Execute(ctx, prompt, priorContext)
	if err != nil {
		logWarn("worker: %s task %s failed: %v", workerID, task.ID, err)
		writeTeamFileAtomic(state.teamDir, teamErrorFile, []byte(err.Error()))
		reportMsg, stopWorker := classifyTaskFailure(err)
		reportFailWithRetries(client, workerID, task.ID, reportMsg)
		if err := ad.ClearSession(); err != nil {
			logWarn("worker: clear session: %v", err)
		}
		return stopWorker
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

		if task.Status == pool.TaskReviewing {
			verdict, feedback, severity := adapter.ExtractReviewVerdict(result.Output)
			if verdict == "" {
				verdict = pool.ReviewFail
				feedback = "review verdict not found in output"
				if severity == "" {
					severity = pool.SeverityMajor
				}
			}
			reportReviewWithRetries(client, workerID, task.ID, verdict, feedback, severity)
		} else {
			// Signal-only completion — data is on filesystem.
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

// cleanTeamDir removes stale files from previous task cycle.
func cleanTeamDir(teamDir string) {
	if teamDir == "" {
		return
	}
	for _, name := range []string{teamTaskFile, teamResultFile, teamHandoverFile, teamErrorFile} {
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

func reportCompleteWithRetries(client *leaderClient, workerID, taskID string) {
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

func reportFailWithRetries(client *leaderClient, workerID, taskID, errMsg string) {
	for i := 0; i < workerAgentReportRetries; i++ {
		if err := client.ReportFail(workerID, taskID, errMsg); err != nil {
			logWarn("worker: report fail attempt %d: %v", i+1, err)
			time.Sleep(workerAgentReportBackoff)
			continue
		}
		return
	}
	logWarn("worker: failed to report failure for task %s after retries", taskID)
}

func reportReviewWithRetries(client *leaderClient, workerID, taskID, verdict, feedback, severity string) {
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

func classifyTaskFailure(err error) (reportMsg string, stopWorker bool) {
	if err == nil {
		return "task execution failed", false
	}

	raw := strings.TrimSpace(err.Error())
	if requiresAuthReset(raw) {
		return "authentication failure: token expired or refresh token reused; run codex logout and sign in again on the host, then restart the worker", true
	}
	return sanitizeFailureMessage(raw), false
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
