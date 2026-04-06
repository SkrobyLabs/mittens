// Package adapter — Gemini CLI adapter
//
// # Gemini CLI — Non-Interactive Driver Research
//
// Binary: gemini  (installed via: npm install -g @google/gemini-cli)
// Source: https://github.com/google-gemini/gemini-cli
//
// ## Invocation
//
//	gemini -p PROMPT --model MODEL --approval-mode=yolo [--output-format FORMAT]
//
// The binary is driven non-interactively with -p/--prompt.  Stdin piping also
// works but -p is preferred so the prompt is not confused with terminal input.
// Working directory is inherited from the process (no explicit flag); set
// cmd.Dir before starting the subprocess.
//
// ## Known Flags  (source: packages/cli/src/config/config.ts)
//
//	-p / --prompt           string   Prompt text; enters headless mode.
//	-m / --model            string   Model name or alias (see Models below).
//	-o / --output-format    string   Output format: text | json | stream-json
//	     --approval-mode    string   Tool approval policy:
//	                                   default   — prompt for each dangerous op
//	                                   auto_edit — auto-approve edit tools
//	                                   yolo      — auto-approve all tools (headless)
//	                                   plan      — read-only mode
//	-s / --sandbox          bool     Run in a sandboxed child process.
//	                                 NOTE: boolean flag — do NOT pass --sandbox=off,
//	                                 that is parsed as a truthy string by yargs.
//	                                 The container env SANDBOX=true already tells
//	                                 the CLI not to fork a sandboxed child (perf).
//	-r / --resume           string   Resume a session: 'latest' or numeric index.
//	-i / --prompt-interactive string Execute prompt then drop into interactive mode.
//	-d / --debug            bool     Debug mode (opens DevTools console).
//
// ## Model Names  (source: packages/core/src/config/models.ts)
//
// Default model: gemini-2.5-pro
//
//	Stable:   gemini-2.5-pro, gemini-2.5-flash, gemini-2.5-flash-lite
//	Preview:  gemini-3-pro-preview, gemini-3-flash-preview
//	Aliases:  auto, pro, flash, flash-lite  (resolved server-side)
//	Env:      GEMINI_MODEL overrides the --model flag
//
// ## Output Formats
//
// ### text (default)
//
//	Plain text on stdout.  Simplest mode; use for headless coding tasks.
//	No token counts, no structured events.
//
// ### json
//
//	Single JSON object on stdout after completion.  Schema not fully documented;
//	likely mirrors the RESULT event from stream-json (see below).
//
// ### stream-json  (NDJSON — one JSON object per line on stdout)
//
//	Each line is a JSON object with a "type" discriminator:
//
//	  {"type":"INIT",        "timestamp":"...","session_id":"...","model":"..."}
//	  {"type":"MESSAGE",     "timestamp":"...","role":"user","content":"..."}
//	  {"type":"TOOL_USE",    "timestamp":"...","tool_name":"...","tool_id":"...","parameters":{...}}
//	  {"type":"TOOL_RESULT", "timestamp":"...","tool_id":"...","status":"success|error","output":"..."}
//	  {"type":"ERROR",       "timestamp":"...","severity":"warning|error","message":"..."}
//	  {"type":"RESULT",      "timestamp":"...","status":"success","stats":{...}}
//
//	The RESULT event's "stats" object carries token usage via the Gemini API's
//	usageMetadata fields:
//	  promptTokenCount      — input tokens (≈ InputTokens)
//	  candidatesTokenCount  — output tokens (≈ OutputTokens)
//	  totalTokenCount       — sum
//
//	The final assistant response text is emitted as a MESSAGE event with
//	role="assistant" before the RESULT event.
//
//	Current adapter implementation uses --output-format=text (plain stdout) for
//	simplicity.  Switching to stream-json would enable token counting and
//	tool-activity events at the cost of NDJSON parsing.
//
// ## Exit Codes
//
//	0    — success
//	1    — generic / unclassified error (yargs parse error, fatal tool error)
//	other— fatal errors export their own exit code via the ExitCodes constant
//	       (FATAL_INPUT_ERROR, FATAL_AUTHENTICATION_ERROR — numeric values are
//	       set at runtime from the @google/gemini-cli-core package; inspect
//	       stderr text to distinguish auth vs runtime failures).
//
// ## Auth Error Detection
//
//	Auth failures surface in stderr as Google API status messages:
//	  PERMISSION_DENIED, UNAUTHENTICATED, API_KEY_INVALID,
//	  HTTP 401, HTTP 403, "invalid API key", "authentication"
//
//	The container persists OAuth state in ~/.gemini/:
//	  oauth_creds.json, google_accounts.json, installation_id, settings.json
//	so repeat runs should not trigger OAuth browser flows.
//
// ## Session Resumption
//
//	--resume latest  (or --resume N) resumes a prior conversation.
//	The Gemini CLI stores sessions on disk; session IDs are opaque strings.
//	ClearSession/ForceClean are no-ops because each Execute call starts a
//	fresh session unless --resume is explicitly passed.
//
// ## Working Directory
//
//	No explicit flag.  The CLI calls process.chdir() at startup based on the
//	current working directory.  Set cmd.Dir on the exec.Cmd before starting.

package adapter

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

var _ Adapter = (*geminiAdapter)(nil)

// geminiAdapter runs tasks via the Gemini CLI in non-interactive mode.
type geminiAdapter struct {
	workDir    string
	model      string
	onActivity func(Activity)
	onToolUse  func(toolName, inputSummary string)
	cmdFactory func(ctx context.Context, name string, args ...string) *exec.Cmd // nil → exec.CommandContext
}

func (g *geminiAdapter) newCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	if g.cmdFactory != nil {
		return g.cmdFactory(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

// normalizeGeminiModel ensures the model string has the "gemini-" prefix and
// falls back to the upstream default (gemini-2.5-pro) when empty.
// Short aliases recognised by the CLI (auto/pro/flash/flash-lite) are passed
// through unchanged.
func normalizeGeminiModel(model string) string {
	if model == "" {
		return "gemini-2.5-pro"
	}
	// Pass through known short aliases and any string that already looks like a
	// full model name.
	switch model {
	case "auto", "pro", "flash", "flash-lite":
		return model
	}
	if !strings.HasPrefix(model, "gemini-") {
		return "gemini-" + model
	}
	return model
}

// geminiAuthPatterns are substrings that indicate an authentication/permission
// error from the Gemini CLI or API, enabling the failure classifier to detect
// FailureAuth.
var geminiAuthPatterns = []string{
	"PERMISSION_DENIED",
	"UNAUTHENTICATED",
	"API_KEY_INVALID",
	"invalid API key",
	"authentication",
	"401",
	"403",
}

func isGeminiAuthError(text string) bool {
	lower := strings.ToLower(text)
	for _, pat := range geminiAuthPatterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

func (g *geminiAdapter) Execute(ctx context.Context, prompt string, priorContext string) (Result, error) {
	fullPrompt := BuildPrompt(prompt, priorContext)

	model := normalizeGeminiModel(g.model)

	// --approval-mode=yolo suppresses all interactive tool-approval prompts,
	// which is required for headless operation.  --sandbox is a boolean flag
	// and should NOT be passed as --sandbox=off (yargs treats the string "off"
	// as truthy); sandbox behaviour is already controlled by the SANDBOX env
	// var injected by the container (GeminiProvider.ContainerEnv).
	args := []string{
		"-p", fullPrompt,
		"--model", model,
		"--approval-mode=yolo",
	}

	cmd := g.newCmd(ctx, "gemini", args...)
	cmd.Dir = g.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start gemini: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			combined := stdout.String() + "\n" + stderr.String()
			if isGeminiAuthError(combined) {
				return Result{ExitCode: exitErr.ExitCode()},
					fmt.Errorf("gemini auth error (exit %d): %s",
						exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
			}
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return Result{ExitCode: exitErr.ExitCode()},
				fmt.Errorf("execute gemini (exit %d): %s", exitErr.ExitCode(), msg)
		}
		return Result{}, fmt.Errorf("execute gemini: %w", err)
	}

	output := strings.TrimSpace(stdout.String())

	// Emit a completion activity with a short summary of the response.
	if summary := shortSummary(output); summary != "" {
		emitActivity(g.onActivity, g.onToolUse, Activity{
			Kind:    ActivityKindStatus,
			Phase:   ActivityPhaseCompleted,
			Name:    "response",
			Summary: summary,
		})
	}

	return Result{
		Output: output,
	}, nil
}

func (g *geminiAdapter) ClearSession() error {
	// Gemini CLI is stateless — no session to clear.
	return nil
}

func (g *geminiAdapter) ForceClean() error {
	// Gemini CLI is stateless — nothing to clean.
	return nil
}

func (g *geminiAdapter) Healthy() bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}
