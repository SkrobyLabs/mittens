package adapter

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helper process (re-executed as subprocess by the harness)
// ---------------------------------------------------------------------------

func TestGeminiHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GEMINI_HELPER_PROCESS") != "1" {
		return
	}

	if stdout := os.Getenv("FAKE_GEMINI_STDOUT"); stdout != "" {
		os.Stdout.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			os.Stdout.WriteString("\n")
		}
	}
	if stderr := os.Getenv("FAKE_GEMINI_STDERR"); stderr != "" {
		os.Stderr.WriteString(stderr)
	}

	exitCode, err := strconv.Atoi(os.Getenv("FAKE_GEMINI_EXIT_CODE"))
	if err != nil {
		exitCode = 0
	}
	os.Exit(exitCode)
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type geminiTestHarness struct {
	stdout   string
	stderr   string
	exitCode int
	records  []testCmdRecord
}

func (h *geminiTestHarness) factory(ctx context.Context, name string, args ...string) *exec.Cmd {
	h.records = append(h.records, testCmdRecord{name: name, args: append([]string{}, args...)})

	cs := []string{"-test.run=TestGeminiHelperProcess", "--"}
	cs = append(cs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(),
		"GO_WANT_GEMINI_HELPER_PROCESS=1",
		"FAKE_GEMINI_STDOUT="+h.stdout,
		"FAKE_GEMINI_STDERR="+h.stderr,
		"FAKE_GEMINI_EXIT_CODE="+strconv.Itoa(h.exitCode),
	)
	return cmd
}

func newTestGemini(h *geminiTestHarness, model string) *geminiAdapter {
	return &geminiAdapter{
		workDir:    os.TempDir(),
		model:      model,
		cmdFactory: h.factory,
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_ModelNormalization
// ---------------------------------------------------------------------------

func TestGeminiAdapter_ModelNormalization(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"flash", "flash"},
		{"gemini-2.0-flash", "gemini-2.0-flash"},
		{"", "gemini-2.5-pro"},
		{"2.0-flash", "gemini-2.0-flash"},
		{"gemini-pro", "gemini-pro"},
	}

	for _, tc := range tests {
		got := normalizeGeminiModel(tc.input)
		if got != tc.want {
			t.Errorf("normalizeGeminiModel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_Execute_Success
// ---------------------------------------------------------------------------

func TestGeminiAdapter_Execute_Success(t *testing.T) {
	h := &geminiTestHarness{
		stdout:   "Hello from Gemini",
		exitCode: 0,
	}
	a := newTestGemini(h, "gemini-2.0-flash")

	var activities []Activity
	a.onActivity = func(act Activity) { activities = append(activities, act) }

	result, err := a.Execute(context.Background(), "say hello", "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output != "Hello from Gemini" {
		t.Fatalf("Result.Output = %q, want %q", result.Output, "Hello from Gemini")
	}
	if result.ExitCode != 0 {
		t.Fatalf("Result.ExitCode = %d, want 0", result.ExitCode)
	}

	// Should have emitted a completion activity.
	if len(activities) != 1 {
		t.Fatalf("activities count = %d, want 1", len(activities))
	}
	if activities[0].Kind != ActivityKindStatus || activities[0].Phase != ActivityPhaseCompleted {
		t.Fatalf("activity = %+v, want status/completed", activities[0])
	}

	// Command args must include -p, --model, and approval mode.
	if len(h.records) != 1 {
		t.Fatalf("command records = %d, want 1", len(h.records))
	}
	rec := h.records[0]
	if !hasFlag(rec, "-p") {
		t.Errorf("expected -p flag in args: %v", rec.args)
	}
	if !hasFlag(rec, "--model") {
		t.Errorf("expected --model flag in args: %v", rec.args)
	}
	if !hasFlag(rec, "--approval-mode=yolo") {
		t.Errorf("expected --approval-mode=yolo flag in args: %v", rec.args)
	}
	if got := getFlagValue(rec, "--model"); got != "gemini-2.0-flash" {
		t.Errorf("--model = %q, want %q", got, "gemini-2.0-flash")
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_Execute_ModelPropagated — short alias passes through unchanged
// ---------------------------------------------------------------------------

func TestGeminiAdapter_Execute_ModelPropagated(t *testing.T) {
	h := &geminiTestHarness{stdout: "ok", exitCode: 0}
	a := newTestGemini(h, "flash")

	if _, err := a.Execute(context.Background(), "task", ""); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(h.records) != 1 {
		t.Fatalf("command records = %d, want 1", len(h.records))
	}
	got := getFlagValue(h.records[0], "--model")
	if got != "flash" {
		t.Errorf("--model = %q, want flash alias passthrough", got)
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_Execute_NonZeroExit
// ---------------------------------------------------------------------------

func TestGeminiAdapter_Execute_NonZeroExit(t *testing.T) {
	h := &geminiTestHarness{
		stderr:   "something went wrong",
		exitCode: 1,
	}
	a := newTestGemini(h, "")

	result, err := a.Execute(context.Background(), "task", "")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if result.ExitCode != 1 {
		t.Fatalf("Result.ExitCode = %d, want 1", result.ExitCode)
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("error message %q does not contain 'exit 1'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_Execute_AuthError
// ---------------------------------------------------------------------------

func TestGeminiAdapter_Execute_AuthError(t *testing.T) {
	authMessages := []struct {
		name   string
		stderr string
	}{
		{"PERMISSION_DENIED", "PERMISSION_DENIED: caller does not have permission"},
		{"API_KEY_INVALID", "API_KEY_INVALID: provided API key is not valid"},
		{"401", "HTTP 401 Unauthorized"},
		{"403", "HTTP 403 Forbidden"},
		{"invalid API key", "invalid API key provided"},
		{"UNAUTHENTICATED", "UNAUTHENTICATED: request is missing required authentication"},
		{"authentication", "authentication failed"},
	}

	for _, tc := range authMessages {
		t.Run(tc.name, func(t *testing.T) {
			h := &geminiTestHarness{
				stderr:   tc.stderr,
				exitCode: 1,
			}
			a := newTestGemini(h, "")

			_, err := a.Execute(context.Background(), "task", "")
			if err == nil {
				t.Fatal("Execute() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), "auth error") {
				t.Errorf("error %q does not contain 'auth error'", err.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_ClearSession
// ---------------------------------------------------------------------------

func TestGeminiAdapter_ClearSession(t *testing.T) {
	a := &geminiAdapter{}
	if err := a.ClearSession(); err != nil {
		t.Fatalf("ClearSession() error = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_ForceClean
// ---------------------------------------------------------------------------

func TestGeminiAdapter_ForceClean(t *testing.T) {
	a := &geminiAdapter{}
	if err := a.ForceClean(); err != nil {
		t.Fatalf("ForceClean() error = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// TestGeminiAdapter_Healthy
// ---------------------------------------------------------------------------

func TestGeminiAdapter_Healthy(t *testing.T) {
	a := &geminiAdapter{}
	got := a.Healthy()
	_, lookErr := exec.LookPath("gemini")
	want := lookErr == nil
	if got != want {
		t.Errorf("Healthy() = %v, want %v (exec.LookPath result: %v)", got, want, lookErr)
	}
}
