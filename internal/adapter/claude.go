package adapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// claudeAdapter runs tasks via the Claude Code CLI.
type claudeAdapter struct {
	workDir       string
	model         string
	skipPermsFlag string
	onToolUse     func(toolName, inputSummary string)
	onLog         func(string)
	reuse         SessionReuseConfig
	cmdFactory    func(ctx context.Context, name string, args ...string) *exec.Cmd // nil → exec.CommandContext

	// Session reuse state.
	sessionID     string    // hex ID of current session
	lastCompleted time.Time // when last task finished
	taskCount     int       // tasks run in this session
	sessionTokens int       // cumulative tokens in this session
}

// newCmd creates an *exec.Cmd, using cmdFactory if set.
func (a *claudeAdapter) newCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	if a.cmdFactory != nil {
		return a.cmdFactory(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func (a *claudeAdapter) log(format string, args ...any) {
	if a.onLog != nil {
		a.onLog(fmt.Sprintf(format, args...))
	}
}

// streamEvent is a minimal struct for parsing NDJSON lines from
// claude --output-format stream-json.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Result  string `json:"result,omitempty"`
	// content_block_start events carry tool info here.
	ContentBlock struct {
		Type  string          `json:"type"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content_block,omitempty"`
	// assistant events carry tool info here (aggregated format).
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content,omitempty"`
	} `json:"message,omitempty"`
	// result events carry token usage.
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// newSessionID generates a random UUID v4 session identifier.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 2
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (a *claudeAdapter) Execute(ctx context.Context, prompt string, priorContext string) (Result, error) {
	fullPrompt := BuildPrompt(prompt, priorContext)

	// Decide whether to reuse the existing session.
	shouldReuse := a.reuse.Enabled &&
		a.sessionID != "" &&
		time.Since(a.lastCompleted) <= a.reuse.TTL &&
		a.taskCount < a.reuse.MaxTasks &&
		a.sessionTokens < a.reuse.MaxTokens

	a.log("session reuse: enabled=%v sessionID=%v ttl=%v taskCount=%d/%d tokens=%d/%d → reuse=%v",
		a.reuse.Enabled, a.sessionID != "", time.Since(a.lastCompleted) <= a.reuse.TTL,
		a.taskCount, a.reuse.MaxTasks, a.sessionTokens, a.reuse.MaxTokens, shouldReuse)

	var args []string
	if a.skipPermsFlag != "" {
		args = append(args, a.skipPermsFlag)
	}
	args = append(args, "--print", "--output-format", "stream-json", "--verbose")
	if a.model != "" {
		args = append(args, "--model", a.model)
	}

	if shouldReuse {
		args = append(args, "--resume", a.sessionID)
		fullPrompt = sessionBreakPrefix + fullPrompt
		a.log("resuming session %s", a.sessionID)
	} else {
		// Start a fresh session.
		_ = a.ForceClean()
		a.sessionID = newSessionID()
		a.taskCount = 0
		a.sessionTokens = 0
		args = append(args, "--session-id", a.sessionID)
		a.log("starting fresh session %s", a.sessionID)
	}

	args = append(args, fullPrompt)

	cmd := a.newCmd(ctx, "claude", args...)
	cmd.Dir = a.workDir

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start claude: %w", err)
	}

	var output string
	var inputTokens, outputTokens int
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
	seenTypes := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev streamEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}

		switch ev.Type {
		case "assistant":
			// Aggregated assistant events (some Claude Code versions).
			if a.onToolUse != nil {
				for _, block := range ev.Message.Content {
					if block.Type == "tool_use" && block.Name != "" {
						summary := summarizeInput(block.Name, block.Input)
						a.onToolUse(block.Name, summary)
					}
				}
			}
		case "content_block_start":
			// Streaming content block events — tool_use info is here.
			if a.onToolUse != nil && ev.ContentBlock.Type == "tool_use" && ev.ContentBlock.Name != "" {
				summary := summarizeInput(ev.ContentBlock.Name, ev.ContentBlock.Input)
				a.onToolUse(ev.ContentBlock.Name, summary)
			}
		case "result":
			output = ev.Result
			inputTokens = ev.Usage.InputTokens
			outputTokens = ev.Usage.OutputTokens
		case "content_block_delta", "content_block_stop", "message_start", "message_delta", "message_stop":
			// Known streaming events — no action needed.
		default:
			if ev.Type != "" && !seenTypes[ev.Type] {
				seenTypes[ev.Type] = true
				if a.onToolUse != nil {
					a.onToolUse("_unknown_event", ev.Type)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		cmd.Wait()
		return Result{}, fmt.Errorf("scan claude output: %w", err)
	}

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return Result{}, fmt.Errorf("execute claude: %w", err)
		}
	}

	if exitCode != 0 && output == "" {
		if errText := strings.TrimSpace(stderrBuf.String()); errText != "" {
			return Result{ExitCode: exitCode}, fmt.Errorf("claude exited %d: %s", exitCode, errText)
		}
	}

	// Update session reuse bookkeeping on success.
	if exitCode == 0 {
		a.taskCount++
		a.lastCompleted = time.Now()
		a.sessionTokens += inputTokens + outputTokens
		a.log("session %s: task %d, tokens %d", a.sessionID, a.taskCount, a.sessionTokens)
	}

	return Result{
		Output:       strings.TrimSpace(output),
		ExitCode:     exitCode,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}

// summarizeInput extracts a human-readable summary from tool input JSON.
func summarizeInput(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}

	var key string
	switch toolName {
	case "Read", "Edit", "Write":
		key = "file_path"
	case "Bash":
		key = "command"
	case "Grep":
		key = "pattern"
	case "Glob":
		key = "pattern"
	case "Agent":
		key = "description"
	default:
		return ""
	}

	val, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(val, &s) != nil {
		return ""
	}
	if toolName == "Bash" && len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}

func (a *claudeAdapter) ClearSession() error {
	if a.reuse.Enabled &&
		a.sessionID != "" &&
		time.Since(a.lastCompleted) <= a.reuse.TTL &&
		a.taskCount < a.reuse.MaxTasks &&
		a.sessionTokens < a.reuse.MaxTokens {
		return nil // defer clear — session might be reused
	}
	a.log("clearing session: reuse conditions not met")
	return a.ForceClean()
}

func (a *claudeAdapter) ForceClean() error {
	a.log("force-cleaning session %s", a.sessionID)
	a.sessionID = ""
	a.taskCount = 0
	a.sessionTokens = 0
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := a.newCmd(ctx, "claude", "--clear")
	cmd.Dir = a.workDir
	return cmd.Run()
}

func (a *claudeAdapter) Healthy() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}
