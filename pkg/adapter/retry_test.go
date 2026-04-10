package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type retryTestAdapter struct {
	results   []Result
	errs      []error
	prompts   []string
	priorCtxs []string
	calls     int
}

func (a *retryTestAdapter) Execute(_ context.Context, prompt string, priorContext string) (Result, error) {
	a.prompts = append(a.prompts, prompt)
	a.priorCtxs = append(a.priorCtxs, priorContext)
	idx := a.calls
	a.calls++

	var result Result
	if idx < len(a.results) {
		result = a.results[idx]
	}
	var err error
	if idx < len(a.errs) {
		err = a.errs[idx]
	}
	return result, err
}

func (a *retryTestAdapter) ClearSession() error { return nil }
func (a *retryTestAdapter) ForceClean() error   { return nil }
func (a *retryTestAdapter) Healthy() bool       { return true }

func TestExecuteForCouncilTurn_RetriesAndSucceeds(t *testing.T) {
	ad := &retryTestAdapter{
		results: []Result{
			{Output: "missing council turn", InputTokens: 10, OutputTokens: 20},
			{Output: `<council_turn>{"seat":"A","turn":1,"stance":"propose","candidatePlan":{"title":"Recovered","tasks":[{"id":"t1","title":"Task","prompt":"Do it","complexity":"low"}]}}</council_turn>`},
		},
	}
	var logs []string

	artifact, result, err := ExecuteForCouncilTurn(context.Background(), ad, "original prompt", "prior context", func(msg string) {
		logs = append(logs, msg)
	})
	if err != nil {
		t.Fatalf("ExecuteForCouncilTurn: %v", err)
	}
	if artifact == nil || artifact.CandidatePlan == nil || artifact.CandidatePlan.Title != "Recovered" {
		t.Fatalf("artifact = %+v, want recovered candidate plan", artifact)
	}
	if result.Output == "" {
		t.Fatal("expected final result output")
	}
	if len(ad.prompts) != 2 {
		t.Fatalf("calls = %d, want 2", len(ad.prompts))
	}
	if ad.priorCtxs[0] != "prior context" || ad.priorCtxs[1] != "" {
		t.Fatalf("priorCtxs = %v, want original then empty retry context", ad.priorCtxs)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "attempt 2/3") {
		t.Fatalf("logs = %v, want retry attempt 2/3", logs)
	}
	if !strings.Contains(ad.prompts[1], "## Original Task") || !strings.Contains(ad.prompts[1], "## Parse Error") {
		t.Fatalf("retry prompt = %q, want self-contained correction prompt", ad.prompts[1])
	}
}

func TestExecuteForCouncilTurn_ExhaustionReturnsAttempts(t *testing.T) {
	ad := &retryTestAdapter{
		results: []Result{
			{Output: "no block one"},
			{Output: "no block two"},
			{Output: "no block three"},
		},
	}

	artifact, result, err := ExecuteForCouncilTurn(context.Background(), ad, "prompt", "ctx", func(string) {})
	if artifact != nil {
		t.Fatalf("artifact = %+v, want nil", artifact)
	}
	if result.Output != "no block three" {
		t.Fatalf("result.Output = %q, want last attempt output", result.Output)
	}
	if err == nil {
		t.Fatal("expected extraction error")
	}
	if got := ExtractionAttempts(err); got != 3 {
		t.Fatalf("ExtractionAttempts(err) = %d, want 3", got)
	}
	if !strings.Contains(err.Error(), "extraction failed after 3 attempts") {
		t.Fatalf("err = %v, want wrapped exhaustion error", err)
	}
}

func TestExecuteForCouncilTurn_MidRetryAdapterErrorStopsImmediately(t *testing.T) {
	boom := errors.New("adapter boom")
	ad := &retryTestAdapter{
		results: []Result{
			{Output: "missing council turn"},
			{Output: "ignored"},
		},
		errs: []error{
			nil,
			boom,
		},
	}

	artifact, result, err := ExecuteForCouncilTurn(context.Background(), ad, "prompt", "ctx", func(string) {})
	if artifact != nil {
		t.Fatalf("artifact = %+v, want nil", artifact)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if result.Output != "ignored" {
		t.Fatalf("result.Output = %q, want second attempt result", result.Output)
	}
	if got := ExtractionAttempts(err); got != 0 {
		t.Fatalf("ExtractionAttempts(err) = %d, want 0", got)
	}
}

func TestExecuteForReviewVerdict_EmptyVerdictRetries(t *testing.T) {
	ad := &retryTestAdapter{
		results: []Result{
			{Output: "review text with no block"},
			{Output: `<review><verdict>pass</verdict><feedback>LGTM</feedback><severity>minor</severity></review>`},
		},
	}

	verdict, feedback, severity, _, err := ExecuteForReviewVerdict(context.Background(), ad, "review prompt", "prior context", func(string) {})
	if err != nil {
		t.Fatalf("ExecuteForReviewVerdict: %v", err)
	}
	if verdict != "pass" || feedback != "LGTM" || severity != "minor" {
		t.Fatalf("got (%q, %q, %q), want pass/LGTM/minor", verdict, feedback, severity)
	}
	if len(ad.prompts) != 2 {
		t.Fatalf("calls = %d, want 2", len(ad.prompts))
	}
	if ad.priorCtxs[1] != "" {
		t.Fatalf("retry prior context = %q, want empty", ad.priorCtxs[1])
	}
}

func TestExecuteForReviewVerdict_ExitCodeDoesNotRetry(t *testing.T) {
	ad := &retryTestAdapter{
		results: []Result{{Output: "bad exit", ExitCode: 9}},
	}

	_, _, _, _, err := ExecuteForReviewVerdict(context.Background(), ad, "prompt", "ctx", func(string) {})
	if err == nil {
		t.Fatal("expected exit-code error")
	}
	if len(ad.prompts) != 1 {
		t.Fatalf("calls = %d, want 1", len(ad.prompts))
	}
	if got := ExtractionAttempts(err); got != 0 {
		t.Fatalf("ExtractionAttempts(err) = %d, want 0", got)
	}
}
