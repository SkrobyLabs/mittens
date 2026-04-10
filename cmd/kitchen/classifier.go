package main

import (
	"encoding/json"
	"strings"
)

type FailureClass string

const (
	FailureCapability     FailureClass = "capability"
	FailurePlan           FailureClass = "plan"
	FailureEnvironment    FailureClass = "environment"
	FailureConflict       FailureClass = "conflict"
	FailureAuth           FailureClass = "auth"
	FailureTimeout        FailureClass = "timeout"
	FailureInfrastructure FailureClass = "infrastructure"
	FailureUnknown        FailureClass = "unknown"
)

type KitchenSignals struct {
	HeartbeatTimeout bool `json:"heartbeatTimeout,omitempty"`
	OOMKilled        bool `json:"oomKilled,omitempty"`
	MergeConflict    bool `json:"mergeConflict,omitempty"`
	AuthFailure      bool `json:"authFailure,omitempty"`
	ExitCode         int  `json:"exitCode,omitempty"`
}

func ClassifyFailure(reported string, detail json.RawMessage, signals KitchenSignals) FailureClass {
	if len(detail) > 0 {
		var decoded KitchenSignals
		if json.Unmarshal(detail, &decoded) == nil {
			if decoded.HeartbeatTimeout {
				signals.HeartbeatTimeout = true
			}
			if decoded.OOMKilled {
				signals.OOMKilled = true
			}
			if decoded.MergeConflict {
				signals.MergeConflict = true
			}
			if decoded.AuthFailure {
				signals.AuthFailure = true
			}
			if decoded.ExitCode != 0 {
				signals.ExitCode = decoded.ExitCode
			}
		}
	}

	switch {
	case signals.MergeConflict:
		return FailureConflict
	case signals.AuthFailure:
		return FailureAuth
	case strings.Contains(strings.ToLower(strings.TrimSpace(reported)), "time budget"), strings.Contains(strings.ToLower(strings.TrimSpace(reported)), "timed out"), strings.Contains(strings.ToLower(strings.TrimSpace(reported)), "timeout"):
		return FailureTimeout
	case signals.HeartbeatTimeout || signals.OOMKilled:
		return FailureInfrastructure
	}

	msg := strings.ToLower(strings.TrimSpace(reported))
	switch {
	case strings.Contains(msg, "time budget"), strings.Contains(msg, "timed out"), strings.Contains(msg, "timeout"):
		return FailureTimeout
	case strings.Contains(msg, "merge conflict"), strings.Contains(msg, "conflict"):
		return FailureConflict
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "api key"), strings.Contains(msg, "auth"):
		return FailureAuth
	case strings.Contains(msg, "docker"), strings.Contains(msg, "container"), strings.Contains(msg, "oom"), strings.Contains(msg, "heartbeat"), strings.Contains(msg, "network"):
		return FailureInfrastructure
	case strings.Contains(msg, "missing dependency"), strings.Contains(msg, "tool not found"), strings.Contains(msg, "environment"), strings.Contains(msg, "permission denied"):
		return FailureEnvironment
	case strings.Contains(msg, "unsupported"), strings.Contains(msg, "cannot"), strings.Contains(msg, "too large"), strings.Contains(msg, "capability"):
		return FailureCapability
	case strings.Contains(msg, "invalid plan"),
		strings.Contains(msg, "invalid review verdict (after"),
		strings.Contains(msg, "invalid review council artifact (after"),
		strings.Contains(msg, "bad plan"),
		strings.Contains(msg, "unclear task"):
		return FailurePlan
	default:
		return FailureUnknown
	}
}
