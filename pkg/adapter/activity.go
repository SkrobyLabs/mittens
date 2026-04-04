package adapter

import "strings"

// ActivityKind is the provider-neutral category of an adapter activity event.
type ActivityKind string

const (
	ActivityKindTool   ActivityKind = "tool"
	ActivityKindStatus ActivityKind = "status"
)

// ActivityPhase is the lifecycle stage of an adapter activity event.
type ActivityPhase string

const (
	ActivityPhaseStarted   ActivityPhase = "started"
	ActivityPhaseCompleted ActivityPhase = "completed"
)

// Activity is the normalized adapter-layer event shape shared across providers.
// Providers can map tool calls, progress/status updates, and short completion
// summaries into this contract without leaking provider-specific event schemas.
type Activity struct {
	Kind    ActivityKind
	Phase   ActivityPhase
	Name    string
	Summary string
}

func emitActivity(onActivity func(Activity), onToolUse func(toolName, inputSummary string), activity Activity) {
	if onActivity != nil {
		onActivity(activity)
	}
	if onToolUse != nil && activity.Kind == ActivityKindTool && activity.Phase == ActivityPhaseStarted && activity.Name != "" {
		onToolUse(activity.Name, activity.Summary)
	}
}

func shortSummary(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= 120 {
		return text
	}
	return text[:120] + "..."
}
