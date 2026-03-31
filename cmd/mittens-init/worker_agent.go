package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	teamTaskFile     = "task.md"
	teamResultFile   = "result.txt"
	teamHandoverFile = "handover.json"
	teamErrorFile    = "error.txt"
)

// workerAgentState tracks the currently executing tool for heartbeat reporting.
// The worker agent is the main orchestrator that drives the AI adapter as a subprocess.
type workerAgentState struct {
	mu          sync.Mutex
	currentTool string
	teamDir     string // mounted team directory for file-based data exchange
}

func (s *workerAgentState) setTool(name string) {
	s.mu.Lock()
	s.currentTool = name
	s.mu.Unlock()
}

func (s *workerAgentState) getTool() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentTool
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
		c.OnToolUse = func(toolName, inputSummary string) {
			logInfo("worker: %s tool: %s → %s", workerID, toolName, inputSummary)
			state.setTool(toolName)
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
			if err := client.Heartbeat(workerID, state.getTool()); err != nil {
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
		executeTask(client, ad, workerID, task, state)
		lastTaskRole = task.Role
	}
}

func executeTask(client *leaderClient, ad adapter.Adapter, workerID string, task *pool.Task, state *workerAgentState) {
	defer state.setTool("")

	// Clean stale files from previous task.
	cleanTeamDir(state.teamDir)

	// Extract prior context from handover.
	var priorContext string
	if task.Handover != nil {
		priorContext = task.Handover.ContextForNext
	}

	// Write task.md to team dir before executing.
	writeTaskFile(state.teamDir, task, priorContext)

	// Execute — BuildPrompt is called inside Execute(), do not call it here.
	ctx := context.Background()
	result, err := ad.Execute(ctx, task.Prompt, priorContext)
	if err != nil {
		logWarn("worker: %s task %s failed: %v", workerID, task.ID, err)
		writeTeamFileAtomic(state.teamDir, teamErrorFile, []byte(err.Error()))
		reportFailWithRetries(client, workerID, task.ID, err.Error())
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

		// Signal-only completion — data is on filesystem.
		reportCompleteWithRetries(client, workerID, task.ID)
		logInfo("worker: %s completed task %s", workerID, task.ID)
	}

	// Clear session for next task.
	if err := ad.ClearSession(); err != nil {
		logWarn("worker: clear session: %v", err)
	}
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
