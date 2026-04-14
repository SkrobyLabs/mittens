package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func TestFormatNotification(t *testing.T) {
	tests := []struct {
		name string
		n    pool.Notification
		want string
	}{
		{
			name: "plan submitted",
			n:    pool.Notification{Type: "plan_submitted", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN] Typed parser errors: submitted for planning",
		},
		{
			name: "plan ready",
			n:    pool.Notification{Type: "plan_ready", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN READY] Typed parser errors: awaiting approval",
		},
		{
			name: "plan review requested",
			n:    pool.Notification{Type: "plan_review_requested", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN REVIEW] Typed parser errors: queued for review",
		},
		{
			name: "impl review requested",
			n:    pool.Notification{Type: "plan_impl_review_requested", ID: "plan_1", Message: "Typed parser errors"},
			want: "[IMPL REVIEW] Typed parser errors: queued for review",
		},
		{
			name: "impl review passed",
			n:    pool.Notification{Type: "plan_impl_review_passed", ID: "plan_1", Message: "Typed parser errors"},
			want: "[IMPL REVIEW PASS] Typed parser errors",
		},
		{
			name: "impl review failed",
			n:    pool.Notification{Type: "plan_impl_review_failed", ID: "plan_1", Message: "Typed parser errors"},
			want: "[IMPL REVIEW FAIL] Typed parser errors",
		},
		{
			name: "plan review passed",
			n:    pool.Notification{Type: "plan_review_passed", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN REVIEW PASS] Typed parser errors",
		},
		{
			name: "plan review failed",
			n:    pool.Notification{Type: "plan_review_failed", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN REVIEW FAIL] Typed parser errors",
		},
		{
			name: "plan revising",
			n:    pool.Notification{Type: "plan_revising", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN REVISION] Typed parser errors: queued for revision",
		},
		{
			name: "review council started",
			n:    pool.Notification{Type: "plan_review_council_started", ID: "plan_1", Message: "Typed parser errors"},
			want: "[REVIEW COUNCIL] Typed parser errors: started",
		},
		{
			name: "review council turn completed",
			n:    pool.Notification{Type: "plan_review_council_turn_completed", ID: "plan_1", Message: "Seat A: pass"},
			want: "[REVIEW COUNCIL] plan_1: Seat A: pass",
		},
		{
			name: "review council converged",
			n:    pool.Notification{Type: "plan_review_council_converged", ID: "plan_1", Message: "pass"},
			want: "[REVIEW COUNCIL PASS] plan_1: pass",
		},
		{
			name: "review council rejected",
			n:    pool.Notification{Type: "plan_review_council_rejected", ID: "plan_1", Message: "Typed parser errors"},
			want: "[REVIEW COUNCIL FAIL] plan_1: Typed parser errors",
		},
		{
			name: "plan deleted",
			n:    pool.Notification{Type: "plan_deleted", ID: "plan_1", Message: "Typed parser errors"},
			want: "[PLAN DELETED] Typed parser errors",
		},
		{
			name: "question",
			n:    pool.Notification{Type: "question", ID: "q-1", Message: "what color?"},
			want: "[BLOCKED] Question q-1: what color?",
		},
		{
			name: "task completed",
			n:    pool.Notification{Type: "task_completed", ID: "t-1"},
			want: "[DONE] Task t-1 completed",
		},
		{
			name: "plan completed",
			n:    pool.Notification{Type: "plan_completed", ID: "plan_1", Message: "Parser cleanup"},
			want: "[DONE] Plan plan_1 completed: Parser cleanup",
		},
		{
			name: "task failed with message",
			n:    pool.Notification{Type: "task_failed", ID: "t-2", Message: "timeout"},
			want: "[FAILED] Task t-2: timeout",
		},
		{
			name: "task failed no message",
			n:    pool.Notification{Type: "task_failed", ID: "t-3"},
			want: "[FAILED] Task t-3",
		},
		{
			name: "task requeued with detail",
			n:    pool.Notification{Type: "task_requeued", ID: "t-3", Message: "fresh worker required"},
			want: "[RETRY] Task t-3 requeued: fresh worker required",
		},
		{
			name: "task requeued no detail",
			n:    pool.Notification{Type: "task_requeued", ID: "t-4"},
			want: "[RETRY] Task t-4 requeued",
		},
		{
			name: "task deleted",
			n:    pool.Notification{Type: "task_deleted", ID: "t-5"},
			want: "[DELETED] Task t-5",
		},
		{
			name: "pipeline created",
			n:    pool.Notification{Type: "pipeline_created", ID: "pipe-1"},
			want: "[PIPELINE] Created pipe-1",
		},
		{
			name: "pipeline failed",
			n:    pool.Notification{Type: "pipeline_failed", ID: "pipe-2", Message: "canceled"},
			want: "[PIPELINE FAILED] pipe-2: canceled",
		},
		{
			name: "review pass",
			n:    pool.Notification{Type: "review_pass", ID: "t-4"},
			want: "[REVIEW PASS] Task t-4 accepted",
		},
		{
			name: "review fail",
			n:    pool.Notification{Type: "review_fail", ID: "t-5"},
			want: "[REVIEW FAIL] Task t-5 rejected",
		},
		{
			name: "escalation accept",
			n:    pool.Notification{Type: "escalation_accept", ID: "t-6"},
			want: "[ESCALATION] Task t-6: accepted by leader",
		},
		{
			name: "escalation retry",
			n:    pool.Notification{Type: "escalation_retry", ID: "t-7"},
			want: "[ESCALATION] Task t-7: retrying with more review cycles",
		},
		{
			name: "escalation abort",
			n:    pool.Notification{Type: "escalation_abort", ID: "t-8"},
			want: "[ESCALATION] Task t-8: aborted",
		},
		{
			name: "runtime worker spawned",
			n:    pool.Notification{Type: "runtime_worker_spawned", ID: "w-1", Message: "mittens-runtime-w-1"},
			want: "[RUNTIME] Worker w-1 spawned: mittens-runtime-w-1",
		},
		{
			name: "runtime assignment submitted",
			n:    pool.Notification{Type: "runtime_assignment_submitted", ID: "w-2", Message: "plan [assignment assign-7]"},
			want: "[RUNTIME] Worker w-2 assignment accepted: plan [assignment assign-7]",
		},
		{
			name: "runtime discovery unavailable",
			n:    pool.Notification{Type: "scheduler_runtime_discovery_unavailable", ID: "kitchen-test", Message: "runtime unavailable"},
			want: "[RUNTIME BLOCKED] runtime unavailable",
		},
		{
			name: "runtime discovery recovered",
			n:    pool.Notification{Type: "scheduler_runtime_discovery_recovered", ID: "kitchen-test"},
			want: "[RUNTIME OK] kitchen-test",
		},
		{
			name: "unknown type with message",
			n:    pool.Notification{Type: "custom", ID: "x-1", Message: "hello"},
			want: "[custom] x-1: hello",
		},
		{
			name: "unknown type no message",
			n:    pool.Notification{Type: "custom", ID: "x-2"},
			want: "[custom] x-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatNotification(tt.n)
			if got != tt.want {
				t.Errorf("formatNotification() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNotificationLevel(t *testing.T) {
	warnTypes := []string{
		"question", "task_failed", "pipeline_failed",
		"plan_failed", "plan_review_failed", "plan_impl_review_failed",
		"plan_review_council_rejected", "scheduler_runtime_discovery_unavailable",
		"escalation_accept", "escalation_retry", "escalation_abort",
		"review_fail",
	}
	for _, typ := range warnTypes {
		if notificationLevel(typ) != "warning" {
			t.Errorf("notificationLevel(%q) = %q, want warning", typ, notificationLevel(typ))
		}
	}

	infoTypes := []string{
		"task_completed", "plan_completed", "pipeline_created", "review_pass",
		"plan_submitted", "plan_ready", "plan_review_requested", "plan_review_passed", "plan_revising",
		"plan_impl_review_requested", "plan_impl_review_passed",
		"plan_review_council_started", "plan_review_council_turn_completed", "plan_review_council_converged",
		"runtime_worker_spawned", "runtime_assignment_submitted", "scheduler_runtime_discovery_recovered",
	}
	for _, typ := range infoTypes {
		if notificationLevel(typ) != "info" {
			t.Errorf("notificationLevel(%q) = %q, want info", typ, notificationLevel(typ))
		}
	}
}
