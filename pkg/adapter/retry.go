package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	extractionRetryBudget   = 2
	extractionOutputTailMax = 8192
)

type extractionError struct {
	attempts int
	err      error
}

func (e *extractionError) Error() string {
	return fmt.Sprintf("extraction failed after %d attempts: %v", e.attempts, e.err)
}

func (e *extractionError) Unwrap() error {
	return e.err
}

func ExtractionAttempts(err error) int {
	var target *extractionError
	if errors.As(err, &target) {
		return target.attempts
	}
	return 0
}

func ExecuteForCouncilTurn(ctx context.Context, ad Adapter, prompt, priorContext string, log func(string)) (*CouncilTurnArtifact, Result, error) {
	totalAttempts := extractionRetryBudget + 1
	currentPrompt := prompt
	currentPriorContext := priorContext

	for attempt := 1; attempt <= totalAttempts; attempt++ {
		result, err := ad.Execute(ctx, currentPrompt, currentPriorContext)
		if err != nil {
			return nil, result, err
		}
		if err := ResultError(result); err != nil {
			return nil, result, err
		}

		artifact, err := ExtractCouncilTurnArtifact(result.Output)
		if err == nil {
			return artifact, result, nil
		}
		if attempt == totalAttempts {
			return nil, result, &extractionError{attempts: attempt, err: err}
		}

		nextAttempt := attempt + 1
		log(fmt.Sprintf("attempt %d/%d: %v (input_tokens=%d output_tokens=%d)", nextAttempt, totalAttempts, err, result.InputTokens, result.OutputTokens))
		currentPrompt = buildExtractionRetryPrompt(prompt, result.Output, err)
		currentPriorContext = ""
	}

	return nil, Result{}, fmt.Errorf("unreachable extraction retry state")
}

func ExecuteForReviewCouncilTurn(ctx context.Context, ad Adapter, prompt, priorContext string, log func(string)) (*ReviewCouncilTurnArtifact, Result, error) {
	totalAttempts := extractionRetryBudget + 1
	currentPrompt := prompt
	currentPriorContext := priorContext

	for attempt := 1; attempt <= totalAttempts; attempt++ {
		result, err := ad.Execute(ctx, currentPrompt, currentPriorContext)
		if err != nil {
			return nil, result, err
		}
		if err := ResultError(result); err != nil {
			return nil, result, err
		}

		artifact, err := ExtractReviewCouncilTurnArtifact(result.Output)
		if err == nil {
			return artifact, result, nil
		}
		if attempt == totalAttempts {
			return nil, result, &extractionError{attempts: attempt, err: err}
		}

		nextAttempt := attempt + 1
		log(fmt.Sprintf("attempt %d/%d: %v (input_tokens=%d output_tokens=%d)", nextAttempt, totalAttempts, err, result.InputTokens, result.OutputTokens))
		currentPrompt = buildExtractionRetryPrompt(prompt, result.Output, err)
		currentPriorContext = ""
	}

	return nil, Result{}, fmt.Errorf("unreachable extraction retry state")
}

func ResultError(result Result) error {
	if result.ExitCode != 0 {
		return adapterExitError(result)
	}
	if msg, ok := LikelyAuthenticationFailureOutput(result.Output); ok {
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func adapterExitError(result Result) error {
	if result.ExitCode == 0 {
		return nil
	}
	if msg := strings.TrimSpace(result.Output); msg != "" {
		return fmt.Errorf("adapter exited with code %d: %s", result.ExitCode, msg)
	}
	return fmt.Errorf("adapter exited with code %d", result.ExitCode)
}

func LikelyAuthenticationFailureOutput(output string) (string, bool) {
	msg := strings.TrimSpace(output)
	if msg == "" {
		return "", false
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.HasPrefix(lower, "failed to authenticate"):
		return msg, true
	case strings.HasPrefix(lower, "invalid authentication credentials"):
		return msg, true
	case strings.HasPrefix(lower, "authentication failure:"):
		return msg, true
	case strings.HasPrefix(lower, "api error: 401"):
		return msg, true
	case strings.Contains(lower, `"type":"authentication_error"`):
		return msg, true
	case strings.Contains(lower, `"type": "authentication_error"`):
		return msg, true
	default:
		return "", false
	}
}

func buildExtractionRetryPrompt(prompt, output string, err error) string {
	var b strings.Builder
	b.WriteString("The previous response could not be parsed. Re-answer the same task and fix the structured output.\n\n")
	b.WriteString("## Original Task\n\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\n## Parse Error\n\n")
	b.WriteString(strings.TrimSpace(err.Error()))
	b.WriteString("\n\n## Previous Output Tail\n\n")
	b.WriteString(indentRetryBlock(truncateOutputTail(output)))
	b.WriteString("\n\n")
	b.WriteString("Return a corrected response for the same task. Preserve the intent of the prior answer, but fix the required structured block so it parses cleanly.")
	return b.String()
}

func truncateOutputTail(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= extractionOutputTailMax {
		return output
	}
	return output[len(output)-extractionOutputTailMax:]
}

func indentRetryBlock(output string) string {
	if output == "" {
		return "    (empty)"
	}
	lines := strings.Split(output, "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}
