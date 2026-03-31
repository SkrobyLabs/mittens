package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewSessionID_UUID(t *testing.T) {
	id := newSessionID()

	if len(id) != 36 {
		t.Fatalf("newSessionID() length = %d, want 36; got %q", len(id), id)
	}

	// Must match UUID v4 pattern: 8-4-4-4-12 hex with version=4 and variant=8/9/a/b.
	matched, err := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Errorf("newSessionID() = %q, does not match UUID v4 pattern", id)
	}

	// Two calls must produce different values.
	id2 := newSessionID()
	if id == id2 {
		t.Errorf("two consecutive newSessionID() calls returned the same value: %q", id)
	}
}

func TestSummarizeInput(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		input    string
		want     string
	}{
		{"Read file_path", "Read", `{"file_path":"/foo/bar.go"}`, "/foo/bar.go"},
		{"Edit file_path", "Edit", `{"file_path":"/a/b.go","old_string":"x","new_string":"y"}`, "/a/b.go"},
		{"Write file_path", "Write", `{"file_path":"/tmp/out.txt","content":"hello"}`, "/tmp/out.txt"},
		{"Bash command", "Bash", `{"command":"ls -la"}`, "ls -la"},
		{"Bash long command", "Bash", `{"command":"` + strings.Repeat("x", 100) + `"}`, strings.Repeat("x", 80) + "..."},
		{"Grep pattern", "Grep", `{"pattern":"func Test"}`, "func Test"},
		{"Glob pattern", "Glob", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"Agent description", "Agent", `{"description":"search code"}`, "search code"},
		{"Unknown tool", "WebSearch", `{"query":"test"}`, ""},
		{"Empty input", "Read", `{}`, ""},
		{"Malformed JSON", "Read", `{bad`, ""},
		{"Empty raw", "Read", ``, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeInput(tt.tool, json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("summarizeInput(%q, ...) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

func TestStreamEvent_ToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/foo.go"}}]}}`

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "assistant" {
		t.Errorf("Type = %q, want assistant", ev.Type)
	}
	if len(ev.Message.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(ev.Message.Content))
	}
	block := ev.Message.Content[0]
	if block.Type != "tool_use" {
		t.Errorf("block.Type = %q, want tool_use", block.Type)
	}
	if block.Name != "Read" {
		t.Errorf("block.Name = %q, want Read", block.Name)
	}
	summary := summarizeInput(block.Name, block.Input)
	if summary != "/foo.go" {
		t.Errorf("summary = %q, want /foo.go", summary)
	}
}

func TestStreamEvent_ContentBlockStart(t *testing.T) {
	line := `{"type":"content_block_start","content_block":{"type":"tool_use","name":"Edit","input":{"file_path":"/src/main.go"}}}`

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "content_block_start" {
		t.Errorf("Type = %q, want content_block_start", ev.Type)
	}
	if ev.ContentBlock.Type != "tool_use" {
		t.Errorf("ContentBlock.Type = %q, want tool_use", ev.ContentBlock.Type)
	}
	if ev.ContentBlock.Name != "Edit" {
		t.Errorf("ContentBlock.Name = %q, want Edit", ev.ContentBlock.Name)
	}
	summary := summarizeInput(ev.ContentBlock.Name, ev.ContentBlock.Input)
	if summary != "/src/main.go" {
		t.Errorf("summary = %q, want /src/main.go", summary)
	}
}

func TestStreamEvent_ContentBlockStartEmptyInput(t *testing.T) {
	// content_block_start events may have empty input (deltas come later).
	line := `{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{}}}`

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ContentBlock.Name != "Bash" {
		t.Errorf("ContentBlock.Name = %q, want Bash", ev.ContentBlock.Name)
	}
	summary := summarizeInput(ev.ContentBlock.Name, ev.ContentBlock.Input)
	if summary != "" {
		t.Errorf("summary = %q, want empty for empty input", summary)
	}
}

func TestStreamEvent_UnknownType(t *testing.T) {
	line := `{"type":"ping"}`

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "ping" {
		t.Errorf("Type = %q, want ping", ev.Type)
	}
	// Unknown types should not cause errors — just be parseable.
}

func TestStreamEvent_KnownNoopTypes(t *testing.T) {
	for _, typ := range []string{"content_block_delta", "content_block_stop", "message_start", "message_delta", "message_stop"} {
		line := `{"type":"` + typ + `"}`
		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal %q: %v", typ, err)
		}
		if ev.Type != typ {
			t.Errorf("Type = %q, want %q", ev.Type, typ)
		}
	}
}

func TestStreamEvent_Result(t *testing.T) {
	line := `{"type":"result","result":"task completed successfully"}`

	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "result" {
		t.Errorf("Type = %q, want result", ev.Type)
	}
	if ev.Result != "task completed successfully" {
		t.Errorf("Result = %q, want 'task completed successfully'", ev.Result)
	}
}

// ---------------------------------------------------------------------------
// Session reuse test infrastructure
// ---------------------------------------------------------------------------

// testCmdRecord captures a single command invocation.
type testCmdRecord struct {
	name string
	args []string
}

// testHarness captures command invocations and provides a fake cmdFactory.
type testHarness struct {
	records   []testCmdRecord
	callIdx   int
	tokenFunc func(idx int) (input, output int)
}

func (h *testHarness) factory(ctx context.Context, name string, args ...string) *exec.Cmd {
	h.records = append(h.records, testCmdRecord{name: name, args: append([]string{}, args...)})
	input, output := 1000, 500
	if h.tokenFunc != nil {
		input, output = h.tokenFunc(h.callIdx)
	}
	h.callIdx++

	cs := []string{"-test.run=TestHelperProcess", "--"}
	cs = append(cs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		fmt.Sprintf("FAKE_INPUT_TOKENS=%d", input),
		fmt.Sprintf("FAKE_OUTPUT_TOKENS=%d", output),
	)
	return cmd
}

func newTestClaude(reuse SessionReuseConfig, h *testHarness) *claudeAdapter {
	return &claudeAdapter{
		workDir:    os.TempDir(),
		reuse:      reuse,
		cmdFactory: h.factory,
	}
}

// TestHelperProcess is re-executed as a subprocess by the test harness.
// It emulates the "claude" CLI: --clear exits immediately, otherwise it
// outputs a fake stream-json result event.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
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
	for _, a := range args {
		if a == "--clear" {
			os.Exit(0)
		}
	}
	inputTok := os.Getenv("FAKE_INPUT_TOKENS")
	outputTok := os.Getenv("FAKE_OUTPUT_TOKENS")
	if inputTok == "" {
		inputTok = "1000"
	}
	if outputTok == "" {
		outputTok = "500"
	}
	fmt.Fprintf(os.Stdout, `{"type":"result","result":"done","usage":{"input_tokens":%s,"output_tokens":%s}}`+"\n", inputTok, outputTok)
	os.Exit(0)
}

func filterExecute(records []testCmdRecord) []testCmdRecord {
	var out []testCmdRecord
	for _, r := range records {
		if !hasFlag(r, "--clear") {
			out = append(out, r)
		}
	}
	return out
}

func hasFlag(r testCmdRecord, flag string) bool {
	for _, a := range r.args {
		if a == flag {
			return true
		}
	}
	return false
}

func getFlagValue(r testCmdRecord, flag string) string {
	for i, a := range r.args {
		if a == flag && i+1 < len(r.args) {
			return r.args[i+1]
		}
	}
	return ""
}

func countClear(records []testCmdRecord) int {
	n := 0
	for _, r := range records {
		if hasFlag(r, "--clear") {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Session reuse tests
// ---------------------------------------------------------------------------

func TestSessionReuse_Disabled(t *testing.T) {
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{}, h)

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(ctx, "task2", ""); err != nil {
		t.Fatal(err)
	}

	execs := filterExecute(h.records)
	if len(execs) != 2 {
		t.Fatalf("expected 2 execute commands, got %d", len(execs))
	}
	for i, r := range execs {
		if hasFlag(r, "--resume") {
			t.Errorf("call %d: unexpected --resume", i)
		}
		if !hasFlag(r, "--session-id") {
			t.Errorf("call %d: expected --session-id", i)
		}
	}

	// ClearSession should always clear when reuse is disabled.
	if a.sessionID == "" {
		t.Fatal("expected non-empty sessionID after Execute")
	}
	if err := a.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if a.sessionID != "" {
		t.Error("expected sessionID to be cleared")
	}
}

func TestSessionReuse_ReuseWithinTTL(t *testing.T) {
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  3,
		MaxTokens: 100000,
	}, h)

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}
	firstSID := a.sessionID

	if _, err := a.Execute(ctx, "task2", ""); err != nil {
		t.Fatal(err)
	}

	execs := filterExecute(h.records)
	if len(execs) != 2 {
		t.Fatalf("expected 2 execute commands, got %d", len(execs))
	}

	// First call: fresh session.
	if !hasFlag(execs[0], "--session-id") {
		t.Error("call 0: expected --session-id")
	}
	if hasFlag(execs[0], "--resume") {
		t.Error("call 0: unexpected --resume")
	}

	// Second call: reuse via --resume with same session ID.
	if !hasFlag(execs[1], "--resume") {
		t.Error("call 1: expected --resume")
	}
	if hasFlag(execs[1], "--session-id") {
		t.Error("call 1: unexpected --session-id")
	}
	if got := getFlagValue(execs[1], "--resume"); got != firstSID {
		t.Errorf("resume ID = %q, want %q", got, firstSID)
	}

	// Session ID should be unchanged.
	if a.sessionID != firstSID {
		t.Errorf("sessionID changed: %q → %q", firstSID, a.sessionID)
	}
}

func TestSessionReuse_TTLExpiry(t *testing.T) {
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  10,
		MaxTokens: 100000,
	}, h)

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}
	firstSID := a.sessionID

	// Artificially expire the session.
	a.lastCompleted = time.Now().Add(-6 * time.Minute)

	if _, err := a.Execute(ctx, "task2", ""); err != nil {
		t.Fatal(err)
	}

	execs := filterExecute(h.records)
	// Both calls should use --session-id (fresh sessions).
	for i, r := range execs {
		if hasFlag(r, "--resume") {
			t.Errorf("call %d: unexpected --resume after TTL expiry", i)
		}
		if !hasFlag(r, "--session-id") {
			t.Errorf("call %d: expected --session-id", i)
		}
	}

	if a.sessionID == firstSID {
		t.Error("expected new sessionID after TTL expiry")
	}
}

func TestSessionReuse_MaxTasksTriggered(t *testing.T) {
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  2,
		MaxTokens: 100000,
	}, h)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := a.Execute(ctx, fmt.Sprintf("task%d", i), ""); err != nil {
			t.Fatal(err)
		}
	}

	execs := filterExecute(h.records)
	if len(execs) != 3 {
		t.Fatalf("expected 3 execute commands, got %d", len(execs))
	}

	// Call 0: fresh (no prior session).
	if !hasFlag(execs[0], "--session-id") {
		t.Error("call 0: expected --session-id")
	}
	// Call 1: reuse (taskCount=1 < maxTasks=2).
	if !hasFlag(execs[1], "--resume") {
		t.Error("call 1: expected --resume")
	}
	// Call 2: fresh (taskCount=2 == maxTasks=2, condition fails).
	if !hasFlag(execs[2], "--session-id") {
		t.Error("call 2: expected --session-id (maxTasks reached)")
	}
	if hasFlag(execs[2], "--resume") {
		t.Error("call 2: unexpected --resume")
	}
}

func TestSessionReuse_MaxTokensTriggered(t *testing.T) {
	h := &testHarness{
		tokenFunc: func(_ int) (int, int) { return 30000, 30000 },
	}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  10,
		MaxTokens: 50000,
	}, h)

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}
	// After first call: sessionTokens = 60000 > maxTokens = 50000.

	if _, err := a.Execute(ctx, "task2", ""); err != nil {
		t.Fatal(err)
	}

	execs := filterExecute(h.records)
	if !hasFlag(execs[0], "--session-id") {
		t.Error("call 0: expected --session-id")
	}
	// Second call must be fresh because tokens exceeded.
	if !hasFlag(execs[1], "--session-id") {
		t.Error("call 1: expected --session-id (maxTokens exceeded)")
	}
	if hasFlag(execs[1], "--resume") {
		t.Error("call 1: unexpected --resume")
	}
}

func TestSessionReuse_ClearSessionDefersWhenActive(t *testing.T) {
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  3,
		MaxTokens: 100000,
	}, h)

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}

	sid := a.sessionID
	clearsBefore := countClear(h.records)

	if err := a.ClearSession(); err != nil {
		t.Fatal(err)
	}

	// No additional --clear command should have been issued.
	if countClear(h.records) != clearsBefore {
		t.Error("ClearSession() issued --clear; expected deferred no-op")
	}
	// Session state should be preserved.
	if a.sessionID != sid {
		t.Error("sessionID changed after deferred ClearSession()")
	}
	if a.taskCount != 1 {
		t.Errorf("taskCount = %d, want 1", a.taskCount)
	}
}

func TestSessionReuse_ForceCleanAlwaysClears(t *testing.T) {
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  3,
		MaxTokens: 100000,
	}, h)

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}
	if a.sessionID == "" {
		t.Fatal("expected non-empty sessionID")
	}

	if err := a.ForceClean(); err != nil {
		t.Fatal(err)
	}
	if a.sessionID != "" {
		t.Error("expected sessionID to be cleared after ForceClean()")
	}
	if a.taskCount != 0 {
		t.Errorf("taskCount = %d, want 0", a.taskCount)
	}
	if a.sessionTokens != 0 {
		t.Errorf("sessionTokens = %d, want 0", a.sessionTokens)
	}
}

// ---------------------------------------------------------------------------
// OnLog callback tests
// ---------------------------------------------------------------------------

func TestClaudeAdapter_LogHelper(t *testing.T) {
	var msgs []string
	a := &claudeAdapter{
		onLog: func(s string) { msgs = append(msgs, s) },
	}

	a.log("hello %s %d", "world", 42)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 log message, got %d", len(msgs))
	}
	if msgs[0] != "hello world 42" {
		t.Errorf("log message = %q, want %q", msgs[0], "hello world 42")
	}
}

func TestClaudeAdapter_LogHelperNilCallback(t *testing.T) {
	a := &claudeAdapter{} // onLog is nil
	// Must not panic.
	a.log("should not panic %d", 1)
}

func TestClaudeAdapter_OnLogFreshSession(t *testing.T) {
	var logs []string
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{}, h)
	a.onLog = func(s string) { logs = append(logs, s) }

	if _, err := a.Execute(context.Background(), "task1", ""); err != nil {
		t.Fatal(err)
	}

	if len(logs) == 0 {
		t.Fatal("expected log messages, got none")
	}

	// Should see the reuse verdict and fresh session log.
	hasVerdict, hasFresh := false, false
	for _, m := range logs {
		if strings.Contains(m, "session reuse:") {
			hasVerdict = true
		}
		if strings.Contains(m, "starting fresh session") {
			hasFresh = true
		}
	}
	if !hasVerdict {
		t.Error("missing 'session reuse:' verdict log")
	}
	if !hasFresh {
		t.Error("missing 'starting fresh session' log")
	}
}

func TestClaudeAdapter_OnLogResumeSession(t *testing.T) {
	var logs []string
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  5,
		MaxTokens: 100000,
	}, h)
	a.onLog = func(s string) { logs = append(logs, s) }

	ctx := context.Background()
	if _, err := a.Execute(ctx, "task1", ""); err != nil {
		t.Fatal(err)
	}
	// Clear logs from first call.
	logs = nil

	if _, err := a.Execute(ctx, "task2", ""); err != nil {
		t.Fatal(err)
	}

	hasResume := false
	for _, m := range logs {
		if strings.Contains(m, "resuming session") {
			hasResume = true
		}
	}
	if !hasResume {
		t.Error("missing 'resuming session' log on reuse path")
	}
}

func TestClaudeAdapter_OnLogBookkeeping(t *testing.T) {
	var logs []string
	h := &testHarness{
		tokenFunc: func(_ int) (int, int) { return 100, 200 },
	}
	a := newTestClaude(SessionReuseConfig{}, h)
	a.onLog = func(s string) { logs = append(logs, s) }

	if _, err := a.Execute(context.Background(), "task1", ""); err != nil {
		t.Fatal(err)
	}

	hasBookkeeping := false
	for _, m := range logs {
		if strings.Contains(m, "task 1") && strings.Contains(m, "tokens") {
			hasBookkeeping = true
		}
	}
	if !hasBookkeeping {
		t.Error("missing bookkeeping log with task count and tokens")
	}
}

func TestClaudeAdapter_OnLogForceClean(t *testing.T) {
	var logs []string
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{}, h)
	a.onLog = func(s string) { logs = append(logs, s) }
	a.sessionID = "test-session-123"

	if err := a.ForceClean(); err != nil {
		t.Fatal(err)
	}

	hasForceClean := false
	for _, m := range logs {
		if strings.Contains(m, "force-cleaning session") && strings.Contains(m, "test-session-123") {
			hasForceClean = true
		}
	}
	if !hasForceClean {
		t.Errorf("missing 'force-cleaning session' log; got %v", logs)
	}
}

func TestClaudeAdapter_OnLogClearSession(t *testing.T) {
	var logs []string
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  3,
		MaxTokens: 100000,
	}, h)
	a.onLog = func(s string) { logs = append(logs, s) }

	// Set up expired state so ClearSession actually clears.
	a.sessionID = "expired-session"
	a.lastCompleted = time.Now().Add(-10 * time.Minute) // TTL expired

	if err := a.ClearSession(); err != nil {
		t.Fatal(err)
	}

	hasClear, hasForceClean := false, false
	for _, m := range logs {
		if strings.Contains(m, "clearing session") {
			hasClear = true
		}
		if strings.Contains(m, "force-cleaning") {
			hasForceClean = true
		}
	}
	if !hasClear {
		t.Errorf("missing 'clearing session' log; got %v", logs)
	}
	if !hasForceClean {
		t.Errorf("missing 'force-cleaning' log from delegated ForceClean; got %v", logs)
	}
}

func TestClaudeAdapter_OnLogClearSessionDeferred(t *testing.T) {
	var logs []string
	h := &testHarness{}
	a := newTestClaude(SessionReuseConfig{
		Enabled:   true,
		TTL:       5 * time.Minute,
		MaxTasks:  3,
		MaxTokens: 100000,
	}, h)
	a.onLog = func(s string) { logs = append(logs, s) }

	// Set up valid reuse state so ClearSession defers.
	a.sessionID = "active-session"
	a.lastCompleted = time.Now()
	a.taskCount = 1
	a.sessionTokens = 500

	if err := a.ClearSession(); err != nil {
		t.Fatal(err)
	}

	// Should produce no log messages — deferred clear is a no-op.
	for _, m := range logs {
		if strings.Contains(m, "clearing") || strings.Contains(m, "force-cleaning") {
			t.Errorf("unexpected log on deferred ClearSession: %q", m)
		}
	}
}

func TestSessionReuse_TokenTracking(t *testing.T) {
	h := &testHarness{
		tokenFunc: func(_ int) (int, int) { return 1234, 5678 },
	}
	a := newTestClaude(SessionReuseConfig{}, h)

	result, err := a.Execute(context.Background(), "task1", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens != 1234 {
		t.Errorf("InputTokens = %d, want 1234", result.InputTokens)
	}
	if result.OutputTokens != 5678 {
		t.Errorf("OutputTokens = %d, want 5678", result.OutputTokens)
	}
	// Verify cumulative tracking on the adapter.
	if a.sessionTokens != 1234+5678 {
		t.Errorf("sessionTokens = %d, want %d", a.sessionTokens, 1234+5678)
	}
}
