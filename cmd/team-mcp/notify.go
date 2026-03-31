package main

import (
	"fmt"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

// pushNotification sends an MCP notifications/message to the leader.
func (s *mcpServer) pushNotification(n pool.Notification) {
	s.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/message",
		"params": map[string]any{
			"level":  notificationLevel(n.Type),
			"logger": "pool",
			"data":   formatNotification(n),
		},
	})
}

// formatNotification creates a human-readable message from a pool notification.
func formatNotification(n pool.Notification) string {
	switch n.Type {
	case "question":
		return fmt.Sprintf("[BLOCKED] Question %s: %s", n.ID, n.Message)
	case "task_completed":
		return fmt.Sprintf("[DONE] Task %s completed", n.ID)
	case "task_failed":
		if n.Message != "" {
			return fmt.Sprintf("[FAILED] Task %s: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[FAILED] Task %s", n.ID)
	case "pipeline_created":
		return fmt.Sprintf("[PIPELINE] Created %s", n.ID)
	case "pipeline_failed":
		if n.Message != "" {
			return fmt.Sprintf("[PIPELINE FAILED] %s: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[PIPELINE FAILED] %s", n.ID)
	case "review_pass":
		return fmt.Sprintf("[REVIEW PASS] Task %s accepted", n.ID)
	case "review_fail":
		return fmt.Sprintf("[REVIEW FAIL] Task %s rejected", n.ID)
	case "escalation_accept":
		return fmt.Sprintf("[ESCALATION] Task %s: accepted by leader", n.ID)
	case "escalation_retry":
		return fmt.Sprintf("[ESCALATION] Task %s: retrying with more review cycles", n.ID)
	case "escalation_abort":
		return fmt.Sprintf("[ESCALATION] Task %s: aborted", n.ID)
	default:
		if n.Message != "" {
			return fmt.Sprintf("[%s] %s: %s", n.Type, n.ID, n.Message)
		}
		return fmt.Sprintf("[%s] %s", n.Type, n.ID)
	}
}

// notificationLevel returns the MCP notification level for a given event type.
func notificationLevel(eventType string) string {
	switch eventType {
	case "question", "task_failed", "pipeline_failed",
		"escalation_accept", "escalation_retry", "escalation_abort",
		"review_fail":
		return "warning"
	default:
		return "info"
	}
}
