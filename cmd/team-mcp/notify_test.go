package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/internal/pool"
)

func TestFormatNotification(t *testing.T) {
	tests := []struct {
		name string
		n    pool.Notification
		want string
	}{
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
		"escalation_accept", "escalation_retry", "escalation_abort",
		"review_fail",
	}
	for _, typ := range warnTypes {
		if notificationLevel(typ) != "warning" {
			t.Errorf("notificationLevel(%q) = %q, want warning", typ, notificationLevel(typ))
		}
	}

	infoTypes := []string{
		"task_completed", "pipeline_created", "review_pass",
	}
	for _, typ := range infoTypes {
		if notificationLevel(typ) != "info" {
			t.Errorf("notificationLevel(%q) = %q, want info", typ, notificationLevel(typ))
		}
	}
}
