package adapter

import (
	"strings"
	"testing"
)

func TestExtractReviewCouncilTurnArtifactSupportsNitSeverity(t *testing.T) {
	raw := `<review_council_turn>{"seat":"A","turn":1,"stance":"propose","verdict":"pass","findings":[{"id":"f1","category":"style","description":"Polish wording","severity":"nit"}]}</review_council_turn>`
	artifact, err := ExtractReviewCouncilTurnArtifact(raw)
	if err != nil {
		t.Fatalf("ExtractReviewCouncilTurnArtifact: %v", err)
	}
	if got := artifact.Findings[0].Severity; got != "nit" {
		t.Fatalf("finding severity = %q, want nit", got)
	}
}

func TestBuildReviewCouncilTurnPromptDocumentsPassFailSeverityRules(t *testing.T) {
	prompt := BuildReviewCouncilTurnPrompt("Plan", "Summary", "", "A", 1, "HEAD~1", nil)
	if !strings.Contains(prompt, "Any `major` or `critical` finding requires `verdict=fail`") {
		t.Fatalf("prompt missing fail-rule guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, `"severity": "nit|minor|major|critical"`) {
		t.Fatalf("prompt missing nit severity schema:\n%s", prompt)
	}
}
