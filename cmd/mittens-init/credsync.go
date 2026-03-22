package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/SkrobyLabs/mittens/internal/credutil"
)

const (
	credSyncInterval     = 5 * time.Second
	refreshThresholdMS   = 300000 // trigger proactive refresh when <5 min remain
)

// runCredSync is the credential sync daemon.
// Runs as a goroutine: pushes refreshed tokens up and pulls newer tokens down.
func runCredSync(bc *brokerClient, credFile string) {
	log := newCredLogger()

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
		log.write("initial push: expiresAt=%d → %d", localExp, code)
	} else {
		log.write("no credentials file at startup")
	}

	ticker := time.NewTicker(credSyncInterval)
	defer ticker.Stop()

	for range ticker.C {
		// --- Push: detect local file changes ---
		currentHash := computeFileHash(credFile)
		if currentHash != "" && currentHash != lastHash {
			if data, err := os.ReadFile(credFile); err == nil && len(data) > 0 {
				localExp := credExpiresAt(data)
				code, _ := bc.put("/", string(data))
				log.write("push: file changed, expiresAt=%d → %d", localExp, code)
				if code == 204 || code == 409 || code == 400 {
					lastHash = currentHash
				}
			}
		}

		// --- Pull: check if broker has newer credentials ---
		remote, code, err := bc.get("/")
		if err == nil && code == 200 && remote != "" {
			remoteExp := credExpiresAt([]byte(remote))
			localExp := credExpiresAtFile(credFile)

			if remoteExp > 0 && localExp >= 0 && remoteExp > localExp {
				tmp := credFile + fmt.Sprintf(".tmp.%d", os.Getpid())
				if err := os.WriteFile(tmp, []byte(remote), 0600); err == nil {
					os.Rename(tmp, credFile)
					lastHash = computeFileHash(credFile)
					log.write("pull: updated local creds (remote: %d, was: %d)", remoteExp, localExp)
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
					log.write("proactive refresh: triggering (expires in %dms)", remaining)
					triggerTokenRefresh(credFile, log)
					lastHash = computeFileHash(credFile)
				} else {
					log.write("proactive refresh: another container is handling it")
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
func triggerTokenRefresh(credFile string, log *credLogger) {
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
					log.write("proactive refresh: skipping (Gemini manages its own OAuth refresh)")
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
	log.write("proactive refresh: set early expiry, waiting for AI CLI to refresh")
}

// credLogger writes timestamped log entries.
type credLogger struct {
	file *os.File
}

func newCredLogger() *credLogger {
	f, _ := os.OpenFile("/tmp/cred-sync.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	return &credLogger{file: f}
}

func (l *credLogger) write(format string, args ...interface{}) {
	if l.file == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.file, "%s [cred-sync] %s\n", ts, msg)
}
