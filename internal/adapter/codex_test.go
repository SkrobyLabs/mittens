package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCodexActivities_CommandExecutionAndResponse(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []Activity
	}{
		{
			name: "item.started command_execution",
			line: `{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc pwd","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
			want: []Activity{{
				Kind:    ActivityKindTool,
				Phase:   ActivityPhaseStarted,
				Name:    "command_execution",
				Summary: "/bin/bash -lc pwd",
			}},
		},
		{
			name: "item.completed command_execution",
			line: `{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc pwd","aggregated_output":"/tmp/work\n","exit_code":0,"status":"completed"}}`,
			want: []Activity{{
				Kind:    ActivityKindTool,
				Phase:   ActivityPhaseCompleted,
				Name:    "command_execution",
				Summary: "/tmp/work",
			}},
		},
		{
			name: "item.completed agent_message",
			line: `{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done from stream"}}`,
			want: []Activity{{
				Kind:    ActivityKindStatus,
				Phase:   ActivityPhaseCompleted,
				Name:    "response",
				Summary: "done from stream",
			}},
		},
		{
			name: "top-level command_execution",
			line: `{"type":"command_execution","command":"/bin/bash -lc pwd","aggregated_output":"","exit_code":null,"status":"in_progress"}`,
			want: []Activity{{
				Kind:    ActivityKindTool,
				Phase:   ActivityPhaseStarted,
				Name:    "command_execution",
				Summary: "/bin/bash -lc pwd",
			}},
		},
		{
			name: "top-level command_execution completed",
			line: `{"type":"command_execution","command":"/bin/bash -lc pwd","aggregated_output":"/tmp/work\n","exit_code":0,"status":"completed"}`,
			want: []Activity{{
				Kind:    ActivityKindTool,
				Phase:   ActivityPhaseCompleted,
				Name:    "command_execution",
				Summary: "/tmp/work",
			}},
		},
		{
			name: "top-level agent_message text",
			line: `{"type":"agent_message","text":"done from top level"}`,
			want: []Activity{{
				Kind:    ActivityKindStatus,
				Phase:   ActivityPhaseCompleted,
				Name:    "response",
				Summary: "done from top level",
			}},
		},
		{
			name: "wrapped msg agent_message message alias",
			line: `{"id":"0","msg":{"type":"agent_message","message":"done from wrapped msg"}}`,
			want: []Activity{{
				Kind:    ActivityKindStatus,
				Phase:   ActivityPhaseCompleted,
				Name:    "response",
				Summary: "done from wrapped msg",
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ev codexStreamEvent
			if err := json.Unmarshal([]byte(tc.line), &ev); err != nil {
				t.Fatal(err)
			}

			if got := codexActivities(ev); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("codexActivities() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

type codexTestHarness struct {
	stdoutLines     []string
	stdoutFile      string
	stderr          string
	exitCode        int
	lastMessage     string
	skipLastMessage bool
	records         []testCmdRecord
}

func (h *codexTestHarness) factory(ctx context.Context, name string, args ...string) *exec.Cmd {
	h.records = append(h.records, testCmdRecord{name: name, args: append([]string{}, args...)})

	cs := []string{"-test.run=TestCodexHelperProcess", "--"}
	cs = append(cs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(),
		"GO_WANT_CODEX_HELPER_PROCESS=1",
		"FAKE_CODEX_STDOUT="+strings.Join(h.stdoutLines, "\n"),
		"FAKE_CODEX_STDERR="+h.stderr,
		"FAKE_CODEX_EXIT_CODE="+strconv.Itoa(h.exitCode),
		"FAKE_CODEX_LAST_MESSAGE="+h.lastMessage,
	)
	if h.stdoutFile != "" {
		cmd.Env = append(cmd.Env, "FAKE_CODEX_STDOUT_FILE="+h.stdoutFile)
	}
	if h.skipLastMessage {
		cmd.Env = append(cmd.Env, "FAKE_CODEX_SKIP_LAST_MESSAGE=1")
	}
	return cmd
}

func newTestCodex(h *codexTestHarness) *codexAdapter {
	return &codexAdapter{
		workDir:    os.TempDir(),
		cmdFactory: h.factory,
	}
}

func TestCodexHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}

	outputPath := getFlagValue(testCmdRecord{args: args}, "--output-last-message")
	if outputPath != "" && os.Getenv("FAKE_CODEX_SKIP_LAST_MESSAGE") != "1" {
		if err := os.WriteFile(outputPath, []byte(os.Getenv("FAKE_CODEX_LAST_MESSAGE")), 0600); err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			os.Exit(91)
		}
	}

	if stdout := os.Getenv("FAKE_CODEX_STDOUT"); stdout != "" {
		fmt.Fprint(os.Stdout, stdout)
		if !strings.HasSuffix(stdout, "\n") {
			fmt.Fprintln(os.Stdout)
		}
	}
	if stdoutFile := os.Getenv("FAKE_CODEX_STDOUT_FILE"); stdoutFile != "" {
		data, err := os.ReadFile(stdoutFile)
		if err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			os.Exit(92)
		}
		fmt.Fprint(os.Stdout, string(data))
	}
	if stderr := os.Getenv("FAKE_CODEX_STDERR"); stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
	}

	exitCode, err := strconv.Atoi(os.Getenv("FAKE_CODEX_EXIT_CODE"))
	if err != nil {
		exitCode = 0
	}
	os.Exit(exitCode)
}

func TestCodexExecute_ParsesActivitiesAndPreservesLastMessageExtraction(t *testing.T) {
	h := &codexTestHarness{
		stdoutLines: []string{
			`{"type":"thread.started","thread_id":"thread_123"}`,
			`{"type":"turn.started"}`,
			`not json`,
			`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc pwd","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
			`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"/bin/bash -lc pwd","aggregated_output":"/tmp/work\n","exit_code":0,"status":"completed"}}`,
			`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done from stream"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":7,"output_tokens":3}}`,
		},
		lastMessage: "done from file\n",
	}

	a := newTestCodex(h)

	var activities []Activity
	var toolCalls []string
	a.onActivity = func(activity Activity) {
		activities = append(activities, activity)
	}
	a.onToolUse = func(toolName, inputSummary string) {
		toolCalls = append(toolCalls, toolName+":"+inputSummary)
	}

	result, err := a.Execute(context.Background(), "task", "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Output != "done from file" {
		t.Fatalf("Result.Output = %q, want %q", result.Output, "done from file")
	}
	if result.InputTokens != 7 || result.OutputTokens != 3 {
		t.Fatalf("tokens = (%d, %d), want (7, 3)", result.InputTokens, result.OutputTokens)
	}

	wantActivities := []Activity{
		{
			Kind:    ActivityKindTool,
			Phase:   ActivityPhaseStarted,
			Name:    "command_execution",
			Summary: "/bin/bash -lc pwd",
		},
		{
			Kind:    ActivityKindTool,
			Phase:   ActivityPhaseCompleted,
			Name:    "command_execution",
			Summary: "/tmp/work",
		},
		{
			Kind:    ActivityKindStatus,
			Phase:   ActivityPhaseCompleted,
			Name:    "response",
			Summary: "done from stream",
		},
	}
	if !reflect.DeepEqual(activities, wantActivities) {
		t.Fatalf("activities = %+v, want %+v", activities, wantActivities)
	}

	if !reflect.DeepEqual(toolCalls, []string{"command_execution:/bin/bash -lc pwd"}) {
		t.Fatalf("toolCalls = %+v, want %+v", toolCalls, []string{"command_execution:/bin/bash -lc pwd"})
	}

	if len(h.records) != 1 {
		t.Fatalf("command records = %d, want 1", len(h.records))
	}
	if !hasFlag(h.records[0], "--json") {
		t.Fatalf("expected codex args to include --json: %+v", h.records[0].args)
	}
	if getFlagValue(h.records[0], "--output-last-message") == "" {
		t.Fatalf("expected --output-last-message flag in args: %+v", h.records[0].args)
	}
}

func TestCodexExecute_ParsesTopLevelActivitiesAndPreservesLastMessageExtraction(t *testing.T) {
	h := &codexTestHarness{
		stdoutLines: []string{
			`{"type":"thread.started","thread_id":"thread_456"}`,
			`{"type":"turn.started"}`,
			`{"type":"command_execution","command":"git status","status":"in_progress"}`,
			`{"type":"command_execution","command":"git status","aggregated_output":"On branch main\n","exit_code":0,"status":"completed"}`,
			`{"id":"0","msg":{"type":"agent_message","message":"done from wrapped stream"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":5}}`,
		},
		lastMessage: "done from file\n",
	}

	a := newTestCodex(h)

	var activities []Activity
	var toolCalls []string
	a.onActivity = func(activity Activity) {
		activities = append(activities, activity)
	}
	a.onToolUse = func(toolName, inputSummary string) {
		toolCalls = append(toolCalls, toolName+":"+inputSummary)
	}

	result, err := a.Execute(context.Background(), "task", "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Output != "done from file" {
		t.Fatalf("Result.Output = %q, want %q", result.Output, "done from file")
	}
	if result.InputTokens != 11 || result.OutputTokens != 5 {
		t.Fatalf("tokens = (%d, %d), want (11, 5)", result.InputTokens, result.OutputTokens)
	}

	wantActivities := []Activity{
		{
			Kind:    ActivityKindTool,
			Phase:   ActivityPhaseStarted,
			Name:    "command_execution",
			Summary: "git status",
		},
		{
			Kind:    ActivityKindTool,
			Phase:   ActivityPhaseCompleted,
			Name:    "command_execution",
			Summary: "On branch main",
		},
		{
			Kind:    ActivityKindStatus,
			Phase:   ActivityPhaseCompleted,
			Name:    "response",
			Summary: "done from wrapped stream",
		},
	}
	if !reflect.DeepEqual(activities, wantActivities) {
		t.Fatalf("activities = %+v, want %+v", activities, wantActivities)
	}

	if !reflect.DeepEqual(toolCalls, []string{"command_execution:git status"}) {
		t.Fatalf("toolCalls = %+v, want %+v", toolCalls, []string{"command_execution:git status"})
	}
}

func TestCodexExecute_NonZeroExitPreservesErrorHandling(t *testing.T) {
	h := &codexTestHarness{
		stdoutLines: []string{
			`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"git status","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
		},
		stderr:          "permission denied\n",
		exitCode:        23,
		skipLastMessage: true,
	}

	a := newTestCodex(h)

	var activities []Activity
	a.onActivity = func(activity Activity) {
		activities = append(activities, activity)
	}

	_, err := a.Execute(context.Background(), "task", "")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if got := err.Error(); got != "execute codex (exit 23): permission denied" {
		t.Fatalf("error = %q, want %q", got, "execute codex (exit 23): permission denied")
	}

	wantActivities := []Activity{{
		Kind:    ActivityKindTool,
		Phase:   ActivityPhaseStarted,
		Name:    "command_execution",
		Summary: "git status",
	}}
	if !reflect.DeepEqual(activities, wantActivities) {
		t.Fatalf("activities = %+v, want %+v", activities, wantActivities)
	}
}

func TestCodexExecute_LargeValidJSONLineStillParsesAndReturnsLastMessage(t *testing.T) {
	largeOutput := strings.Repeat("abcdef", 900000)
	h := &codexTestHarness{
		stdoutFile: writeCodexStdoutFixture(t,
			mustMarshalCodexLine(t, map[string]any{
				"type": "item.completed",
				"item": map[string]any{
					"id":                "item_0",
					"type":              "command_execution",
					"command":           "build-project",
					"aggregated_output": largeOutput,
					"exit_code":         0,
					"status":            "completed",
				},
			}),
			`{"type":"turn.completed","usage":{"input_tokens":13,"output_tokens":8}}`,
		),
		lastMessage: "final answer from file\n",
	}

	a := newTestCodex(h)

	var activities []Activity
	a.onActivity = func(activity Activity) {
		activities = append(activities, activity)
	}

	result, err := a.Execute(context.Background(), "task", "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Output != "final answer from file" {
		t.Fatalf("Result.Output = %q, want %q", result.Output, "final answer from file")
	}
	if result.InputTokens != 13 || result.OutputTokens != 8 {
		t.Fatalf("tokens = (%d, %d), want (13, 8)", result.InputTokens, result.OutputTokens)
	}
	if len(activities) != 1 {
		t.Fatalf("activities length = %d, want 1", len(activities))
	}
	if activities[0].Kind != ActivityKindTool || activities[0].Phase != ActivityPhaseCompleted || activities[0].Name != "command_execution" {
		t.Fatalf("activity = %+v, want completed command_execution", activities[0])
	}
	if len(activities[0].Summary) != 123 || !strings.HasSuffix(activities[0].Summary, "...") {
		t.Fatalf("activity summary = %q, want truncated large summary", activities[0].Summary)
	}
}

func TestCodexExecute_OversizedInvalidLineIsSkippedWithoutBreakingSuccess(t *testing.T) {
	h := &codexTestHarness{
		stdoutFile: writeCodexStdoutFixture(t,
			strings.Repeat("not-json-", codexStreamLineLimit/9+1),
			`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done after noisy line"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":17,"output_tokens":9}}`,
		),
		lastMessage: "final answer from file\n",
	}

	a := newTestCodex(h)

	var activities []Activity
	a.onActivity = func(activity Activity) {
		activities = append(activities, activity)
	}

	result, err := a.Execute(context.Background(), "task", "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Output != "final answer from file" {
		t.Fatalf("Result.Output = %q, want %q", result.Output, "final answer from file")
	}
	if result.InputTokens != 17 || result.OutputTokens != 9 {
		t.Fatalf("tokens = (%d, %d), want (17, 9)", result.InputTokens, result.OutputTokens)
	}

	wantActivities := []Activity{{
		Kind:    ActivityKindStatus,
		Phase:   ActivityPhaseCompleted,
		Name:    "response",
		Summary: "done after noisy line",
	}}
	if !reflect.DeepEqual(activities, wantActivities) {
		t.Fatalf("activities = %+v, want %+v", activities, wantActivities)
	}
}

func mustMarshalCodexLine(t *testing.T, payload any) string {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(data)
}

func writeCodexStdoutFixture(t *testing.T, lines ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "codex-stdout.jsonl")
	data := strings.Join(lines, "\n")
	if !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
	return path
}
