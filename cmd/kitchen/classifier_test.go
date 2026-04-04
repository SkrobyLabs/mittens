package main

import (
	"encoding/json"
	"testing"
)

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name     string
		reported string
		signals  KitchenSignals
		want     FailureClass
	}{
		{name: "auth", reported: "unauthorized from provider", want: FailureAuth},
		{name: "conflict", reported: "merge conflict while applying child branch", want: FailureConflict},
		{name: "environment", reported: "tool not found in environment", want: FailureEnvironment},
		{name: "capability", reported: "model cannot handle repository size", want: FailureCapability},
		{name: "timeout", reported: "task exceeded time budget of 5 minutes", want: FailureTimeout},
		{name: "infra signal", signals: KitchenSignals{OOMKilled: true}, want: FailureInfrastructure},
		{name: "unknown", reported: "weird failure", want: FailureUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyFailure(tt.reported, nil, tt.signals); got != tt.want {
				t.Fatalf("ClassifyFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyFailureDetailPayload(t *testing.T) {
	detail, err := json.Marshal(KitchenSignals{MergeConflict: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := ClassifyFailure("ignored", detail, KitchenSignals{}); got != FailureConflict {
		t.Fatalf("ClassifyFailure(detail) = %q, want conflict", got)
	}
}
