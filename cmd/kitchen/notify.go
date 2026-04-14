package main

import (
	"fmt"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

// formatNotification creates a human-readable message from a pool notification.
func formatNotification(n pool.Notification) string {
	switch n.Type {
	case "plan_submitted":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN] %s: submitted for planning", n.Message)
		}
		return fmt.Sprintf("[PLAN] %s: submitted for planning", n.ID)
	case "plan_ready":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN READY] %s: awaiting approval", n.Message)
		}
		return fmt.Sprintf("[PLAN READY] %s", n.ID)
	case "plan_review_requested":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN REVIEW] %s: queued for review", n.Message)
		}
		return fmt.Sprintf("[PLAN REVIEW] %s", n.ID)
	case "plan_impl_review_requested":
		if n.Message != "" {
			return fmt.Sprintf("[IMPL REVIEW] %s: queued for review", n.Message)
		}
		return fmt.Sprintf("[IMPL REVIEW] %s", n.ID)
	case "plan_impl_review_passed":
		if n.Message != "" {
			return fmt.Sprintf("[IMPL REVIEW PASS] %s", n.Message)
		}
		return fmt.Sprintf("[IMPL REVIEW PASS] %s", n.ID)
	case "plan_impl_review_failed":
		if n.Message != "" {
			return fmt.Sprintf("[IMPL REVIEW FAIL] %s", n.Message)
		}
		return fmt.Sprintf("[IMPL REVIEW FAIL] %s", n.ID)
	case "plan_review_passed":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN REVIEW PASS] %s", n.Message)
		}
		return fmt.Sprintf("[PLAN REVIEW PASS] %s", n.ID)
	case "plan_review_failed":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN REVIEW FAIL] %s", n.Message)
		}
		return fmt.Sprintf("[PLAN REVIEW FAIL] %s", n.ID)
	case "plan_review_council_started":
		return fmt.Sprintf("[REVIEW COUNCIL] %s: started", firstNonEmpty(n.Message, n.ID))
	case "plan_review_council_turn_completed":
		return fmt.Sprintf("[REVIEW COUNCIL] %s: %s", n.ID, n.Message)
	case "plan_review_council_converged":
		return fmt.Sprintf("[REVIEW COUNCIL PASS] %s: %s", n.ID, n.Message)
	case "plan_review_council_rejected":
		return fmt.Sprintf("[REVIEW COUNCIL FAIL] %s: %s", n.ID, n.Message)
	case "plan_waiting_on_dependency":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN WAITING] %s: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[PLAN WAITING] %s: waiting on dependency", n.ID)
	case "plan_revising":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN REVISION] %s: queued for revision", n.Message)
		}
		return fmt.Sprintf("[PLAN REVISION] %s", n.ID)
	case "plan_deleted":
		if n.Message != "" {
			return fmt.Sprintf("[PLAN DELETED] %s", n.Message)
		}
		return fmt.Sprintf("[PLAN DELETED] %s", n.ID)
	case "question":
		return fmt.Sprintf("[BLOCKED] Question %s: %s", n.ID, n.Message)
	case "task_completed":
		return fmt.Sprintf("[DONE] Task %s completed", n.ID)
	case "plan_completed":
		if n.Message != "" {
			return fmt.Sprintf("[DONE] Plan %s completed: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[DONE] Plan %s completed", n.ID)
	case "task_failed":
		if n.Message != "" {
			return fmt.Sprintf("[FAILED] Task %s: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[FAILED] Task %s", n.ID)
	case "task_requeued":
		if n.Message != "" {
			return fmt.Sprintf("[RETRY] Task %s requeued: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[RETRY] Task %s requeued", n.ID)
	case "task_deleted":
		return fmt.Sprintf("[DELETED] Task %s", n.ID)
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
	case "runtime_worker_spawned":
		if n.Message != "" {
			return fmt.Sprintf("[RUNTIME] Worker %s spawned: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[RUNTIME] Worker %s spawned", n.ID)
	case "runtime_worker_killed":
		return fmt.Sprintf("[RUNTIME] Worker %s killed", n.ID)
	case "runtime_worker_recycled":
		return fmt.Sprintf("[RUNTIME] Worker %s recycled", n.ID)
	case "runtime_assignment_submitted":
		if n.Message != "" {
			return fmt.Sprintf("[RUNTIME] Worker %s assignment accepted: %s", n.ID, n.Message)
		}
		return fmt.Sprintf("[RUNTIME] Worker %s assignment accepted", n.ID)
	case "scheduler_runtime_discovery_unavailable":
		if n.Message != "" {
			return fmt.Sprintf("[RUNTIME BLOCKED] %s", n.Message)
		}
		return fmt.Sprintf("[RUNTIME BLOCKED] %s", n.ID)
	case "scheduler_runtime_discovery_recovered":
		return fmt.Sprintf("[RUNTIME OK] %s", firstNonEmpty(n.Message, n.ID))
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
		"plan_failed", "plan_review_failed", "plan_impl_review_failed",
		"plan_review_council_rejected", "scheduler_runtime_discovery_unavailable",
		"escalation_accept", "escalation_retry", "escalation_abort",
		"review_fail":
		return "warning"
	default:
		return "info"
	}
}
