package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/SkrobyLabs/mittens/internal/credutil"
)

const (
	credSyncInterval      = 5 * time.Second
	refreshThresholdMS    = 300000 // trigger proactive refresh when <5 min remain
	refreshPendingTimeout = 60 * time.Second // safety timeout for refresh-pending state
	expiredRefreshCooldown = 60 * time.Second
)

// shouldAcceptPull decides whether a pull from broker should overwrite local creds.
// During a pending proactive refresh, only genuinely new creds (higher than the
// original pre-refresh expiresAt) are accepted — this prevents the pull loop from
// undoing the faked expiresAt=1 that triggers the CLI's OAuth refresh.
func shouldAcceptPull(remoteExp, localExp, refreshOrigExp int64, refreshPending bool) bool {
	if !refreshPending {
		return remoteExp > 0 && localExp >= 0 && remoteExp > localExp
	}
	return remoteExp > refreshOrigExp
}

// forkCredSync starts the credential sync daemon as a separate child process.
// This is necessary because the parent process will syscall.Exec to launch the
// AI CLI — which would kill an in-process goroutine. The child process inherits
// env vars (including MITTENS_CONFIG) and runs independently.
func forkCredSync() error {
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = append(os.Environ(), "MITTENS_CREDSYNC_MODE=1")
	logFile, _ := os.OpenFile("/tmp/credsync-child.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("fork credsync: %w", err)
	}
	// Don't Wait() — let the child run independently.
	// The parent is about to syscall.Exec, which orphans the child.
	return nil
}

// runCredsyncMain is the entry point for the forked credsync child process.
// It loads config, creates a broker client, and runs the sync loop forever.
func runCredsyncMain() {
	cfg := loadConfig()
	bc := newBrokerClient(cfg)
	if bc == nil {
		return
	}
	credFile := cfg.AIDir + "/" + cfg.AICredFile
	logPath := "/tmp/credsync.log"
	if strings.TrimSpace(cfg.LogDir) != "" {
		_ = os.MkdirAll(cfg.LogDir, 0755)
		logPath = cfg.LogDir + "/credsync.log"
	}
	runCredSync(bc, credFile, logPath, cfg.AIBinary, credSyncSource(cfg))
}

// runCredSync is the credential sync daemon main loop.
// Runs as the main function of a forked child process: pushes refreshed tokens up and pulls newer tokens down.
func runCredSync(bc *brokerClient, credFile, logPath, binary, source string) {
	log := newCredLogger(bc, logPath, source)

	log.write("started (broker: %s)", bc.baseURL)

	// Connectivity check.
	if _, _, err := bc.get("/"); err != nil {
		log.write("WARNING: broker NOT reachable at %s", bc.baseURL)
	} else {
		log.write("broker reachable")
	}

	// Initial push.
	lastHash := computeFileHash(credFile)
	if data, err := os.ReadFile(credFile); err == nil && len(data) > 0 {
		localExp := credExpiresAt(data)
		code, _ := bc.put("/", string(data))
		log.event("initial-push", "expiresAt=%d status=%d", localExp, code)
	} else {
		log.write("no credentials file at startup")
	}

	ticker := time.NewTicker(credSyncInterval)
	defer ticker.Stop()

	// Refresh-pending state: tracks when we've triggered proactive refresh
	// and are waiting for the CLI to complete its OAuth flow.
	var refreshPending bool
	var refreshOrigExp int64
	var refreshTriggeredAt time.Time
	var lastExpiredRefreshAttempt time.Time

	for range ticker.C {
		// --- Push: detect local file changes ---
		currentHash := computeFileHash(credFile)
		if currentHash != "" && currentHash != lastHash {
			if data, err := os.ReadFile(credFile); err == nil && len(data) > 0 {
				localExp := credExpiresAt(data)
				code, _ := bc.put("/", string(data))
				switch code {
				case 204:
					log.event("push-accepted", "expiresAt=%d", localExp)
				case 409:
					log.event("push-rejected", "stale expiresAt=%d", localExp)
				case 400:
					log.event("push-invalid", "expiresAt=%d", localExp)
				default:
					log.event("push-status", "expiresAt=%d status=%d", localExp, code)
				}
				if code == 204 || code == 409 || code == 400 {
					lastHash = currentHash
				}
			}
		}

		// --- Pull: check if broker has newer credentials ---
		// During a pending proactive refresh, only accept genuinely new creds
		// (higher expiresAt than the original pre-refresh value). This prevents
		// the pull loop from overwriting the faked expiresAt=1 with the broker's
		// stale near-expiry creds before the CLI can complete its OAuth refresh.
		if refreshPending && time.Since(refreshTriggeredAt) > refreshPendingTimeout {
			refreshPending = false
			log.event("refresh-timeout", "waiting for CLI refresh timed out after %s", refreshPendingTimeout)
		}
		remote, code, err := bc.get("/")
		if err == nil && code == 200 && remote != "" {
			remoteExp := credExpiresAt([]byte(remote))
			localExp := credExpiresAtFile(credFile)

			if shouldAcceptPull(remoteExp, localExp, refreshOrigExp, refreshPending) {
				tmp := credFile + fmt.Sprintf(".tmp.%d", os.Getpid())
				if err := os.WriteFile(tmp, []byte(remote), 0600); err == nil {
					os.Rename(tmp, credFile)
					lastHash = computeFileHash(credFile)
					if refreshPending {
						refreshPending = false
						log.event("refresh-complete", "accepted refreshed creds remote=%d previous=%d", remoteExp, refreshOrigExp)
					} else {
						log.event("pull-update", "accepted broker creds remote=%d previous=%d", remoteExp, localExp)
					}
				}
			}
		}

		// --- Proactive refresh ---
		curExp := credExpiresAtFile(credFile)
		nowMS := time.Now().UnixMilli()
		if curExp > 0 {
			remaining := curExp - nowMS
			if remaining > 0 && remaining < refreshThresholdMS {
				action := brokerRefreshRequest(bc)
				if action == "refresh" {
					log.event("refresh-trigger", "expires in %dms", remaining)
					refreshOrigExp = curExp
					if triggerRefreshProbe(binary, credFile, log, log.write) {
						lastHash = computeFileHash(credFile)
						if err := pushFreshCreds(bc, credFile); err != nil {
							log.event("refresh-push-fail", "failed to push refreshed credentials: %v", err)
						} else {
							log.event("refresh-probe-ok", "CLI refreshed credentials via probe")
							refreshPending = true
							refreshTriggeredAt = time.Now()
						}
					} else {
						log.event("refresh-probe-fail", "CLI probe did not produce fresh credentials")
					}
				} else {
					log.event("refresh-wait", "another container is handling proactive refresh")
				}
			} else if remaining <= 0 && time.Since(lastExpiredRefreshAttempt) > expiredRefreshCooldown {
				lastExpiredRefreshAttempt = time.Now()
				action := brokerRefreshRequest(bc)
				if action == "refresh" {
					log.event("expired-refresh-trigger", "token expired %dms ago", -remaining)
					if triggerRefreshProbe(binary, credFile, log, log.write) {
						lastHash = computeFileHash(credFile)
						if err := pushFreshCreds(bc, credFile); err != nil {
							log.event("expired-refresh-push-fail", "failed to push refreshed credentials: %v", err)
						} else {
							log.event("expired-refresh-ok", "recovered expired credentials via probe")
						}
					} else {
						log.event("expired-refresh-fail", "CLI probe did not recover expired credentials")
						lastExpiredRefreshAttempt = time.Time{}
					}
				} else {
					log.event("expired-refresh-wait", "another container is handling expired credential refresh")
				}
			}
		}
	}
}

// credExpiresAt extracts the highest expiry timestamp from credential JSON.
// Delegates to the shared credutil package.
func credExpiresAt(data []byte) int64 {
	return credutil.ExpiresAt(data)
}

func credExpiresAtFile(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return credExpiresAt(data)
}

func computeFileHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", md5.Sum(data))
}

func brokerRefreshRequest(bc *brokerClient) string {
	body, _, err := bc.postWithBody("/refresh", "", "")
	if err != nil {
		return "wait"
	}
	var result struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(body), &result) != nil {
		return "wait"
	}
	if result.Action == "" {
		return "wait"
	}
	return result.Action
}

// triggerTokenRefresh triggers proactive token refresh by faking an early expiry.
func triggerTokenRefresh(credFile string, log credEventLogger) {
	data, err := os.ReadFile(credFile)
	if err != nil {
		return
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return
	}

	// Detect Gemini format: skip proactive refresh.
	if _, hasExpiryDate := obj["expiry_date"]; hasExpiryDate {
		if _, hasClaude := obj["claudeAiOauth"]; !hasClaude {
			if _, hasExpiresAt := obj["expiresAt"]; !hasExpiresAt {
				if _, hasExpiresAtSnake := obj["expires_at"]; !hasExpiresAtSnake {
					log.event("refresh-skip", "skipping proactive refresh because provider manages OAuth internally")
					return
				}
			}
		}
	}

	// Set early expiry to trigger the AI CLI's internal refresh.
	modified := make(map[string]json.RawMessage)
	for k, v := range obj {
		modified[k] = v
	}

	if _, ok := modified["claudeAiOauth"]; ok {
		// Nested Claude OAuth: set claudeAiOauth.expiresAt = 1.
		var nested map[string]json.RawMessage
		if json.Unmarshal(modified["claudeAiOauth"], &nested) == nil {
			nested["expiresAt"] = json.RawMessage("1")
			if b, err := json.Marshal(nested); err == nil {
				modified["claudeAiOauth"] = b
			}
		}
	} else if _, ok := modified["expires_at"]; ok {
		modified["expires_at"] = json.RawMessage("1")
	} else {
		modified["expiresAt"] = json.RawMessage("1")
	}

	newData, err := json.Marshal(modified)
	if err != nil {
		return
	}

	tmp := credFile + fmt.Sprintf(".refresh.%d", os.Getpid())
	if err := os.WriteFile(tmp, newData, 0600); err != nil {
		return
	}
	os.Rename(tmp, credFile)
	log.event("refresh-armed", "set early expiry marker and waiting for CLI refresh")
}

// credLogger writes timestamped log entries.
type credLogger struct {
	file *os.File
	bc   *brokerClient
	source string
}

func newCredLogger(bc *brokerClient, logPath, source string) *credLogger {
	if strings.TrimSpace(logPath) == "" {
		logPath = "/tmp/credsync.log"
	}
	f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	return &credLogger{file: f, bc: bc, source: strings.TrimSpace(source)}
}

func (l *credLogger) write(format string, args ...interface{}) {
	if l.file == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	if l.source != "" {
		fmt.Fprintf(l.file, "%s [cred-sync %s] %s\n", ts, l.source, msg)
		return
	}
	fmt.Fprintf(l.file, "%s [cred-sync] %s\n", ts, msg)
}

func (l *credLogger) event(event, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	l.write("%s — %s", strings.TrimSpace(event), msg)
	if l == nil || l.bc == nil {
		return
	}
	body, err := json.Marshal(map[string]string{
		"component": "cred-sync",
		"event":     strings.TrimSpace(event),
		"message":   msg,
		"source":    l.source,
	})
	if err != nil {
		return
	}
	_, _ = l.bc.postJSON("/sync-log", string(body))
}

func credSyncSource(cfg *config) string {
	if cfg == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if name := strings.TrimSpace(cfg.ProviderName); name != "" {
		parts = append(parts, "provider="+name)
	}
	if container := strings.TrimSpace(cfg.ContainerName); container != "" {
		parts = append(parts, "container="+container)
	}
	if instance := strings.TrimSpace(cfg.InstanceName); instance != "" {
		parts = append(parts, "instance="+instance)
	}
	return strings.Join(parts, " ")
}
