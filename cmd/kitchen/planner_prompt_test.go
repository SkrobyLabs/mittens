package main

import (
	"strings"
	"testing"
)

func TestBuildLineageFixMergePromptEmphasizesIntegratedResolution(t *testing.T) {
	prompt := buildLineageFixMergePrompt("main", "parser-errors", []string{"shared.txt"}, "Parser errors")

	required := []string{
		"reconciling the Kitchen lineage branch with the latest base branch",
		"does NOT mean you should prefer it",
		"Treat HEAD/current/ours and incoming/theirs as inputs to reconcile, not instructions about which side to keep.",
		"Do not drop the lineage's work just because the base changed, and do not drop the base's work just because the fix branch starts from lineage.",
		"synthesize the smallest correct combined result instead of picking one side verbatim",
	}
	for _, fragment := range required {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("prompt missing %q:\n%s", fragment, prompt)
		}
	}
}
