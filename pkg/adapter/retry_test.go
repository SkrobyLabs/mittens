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
			{Output: "missing council turn\n```\nextra fence", InputTokens: 10, OutputTokens: 20},
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
	if strings.Contains(ad.prompts[1], "```text") {
		t.Fatalf("retry prompt = %q, want indented previous output block instead of fenced block", ad.prompts[1])
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

func TestExecuteForCouncilTurn_ExitCodeIncludesOutput(t *testing.T) {
	ad := &retryTestAdapter{
		results: []Result{{
			Output:   `Failed to authenticate. API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"Invalid authentication credentials"}}`,
			ExitCode: 1,
		}},
	}

	artifact, result, err := ExecuteForCouncilTurn(context.Background(), ad, "prompt", "ctx", func(string) {})
	if artifact != nil {
		t.Fatalf("artifact = %+v, want nil", artifact)
	}
	if result.ExitCode != 1 {
		t.Fatalf("result.ExitCode = %d, want 1", result.ExitCode)
	}
	if err == nil {
		t.Fatal("expected exit-code error")
	}
	if !strings.Contains(err.Error(), "Invalid authentication credentials") {
		t.Fatalf("err = %v, want adapter output preserved", err)
	}
	if got := ExtractionAttempts(err); got != 0 {
		t.Fatalf("ExtractionAttempts(err) = %d, want 0", got)
	}
}

func TestExecuteForReviewCouncilTurn_RetriesAndSucceeds(t *testing.T) {
	ad := &retryTestAdapter{
		results: []Result{
			{Output: "missing review council turn\n```\nextra fence", InputTokens: 11, OutputTokens: 21},
			{Output: `<review_council_turn>{"seat":"A","turn":1,"stance":"propose","verdict":"fail","summary":"Recovered review"}</review_council_turn>`},
		},
	}
	var logs []string

	artifact, result, err := ExecuteForReviewCouncilTurn(context.Background(), ad, "original prompt", "prior context", func(msg string) {
		logs = append(logs, msg)
	})
	if err != nil {
		t.Fatalf("ExecuteForReviewCouncilTurn: %v", err)
	}
	if artifact == nil || artifact.Summary != "Recovered review" {
		t.Fatalf("artifact = %+v, want recovered review artifact", artifact)
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
