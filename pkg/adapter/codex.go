package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const codexStreamLineLimit = 16 * 1024 * 1024

// codexAdapter runs tasks via the Codex CLI in non-interactive exec mode.
type codexAdapter struct {
	workDir       string
	model         string
	skipPermsFlag string
	onActivity    func(Activity)
	onToolUse     func(toolName, inputSummary string)
	cmdFactory    func(ctx context.Context, name string, args ...string) *exec.Cmd // nil -> exec.CommandContext
}

func (a *codexAdapter) newCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	if a.cmdFactory != nil {
		return a.cmdFactory(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

type codexStreamEvent struct {
	Type string     `json:"type,omitempty"`
	ID   string     `json:"id,omitempty"`
	Item codexItem  `json:"item,omitempty"`
	Msg  *codexItem `json:"msg,omitempty"`
	// Some codex exec --json variants emit item-like payloads directly at the
	// top level instead of under `item`, and older wrappers have been observed
	// to rename agent_message text to `message`.
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"`
	Text             string `json:"text,omitempty"`
	Message          string `json:"message,omitempty"`
	Usage            struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type codexItem struct {
	ID               string `json:"id,omitempty"`
	Type             string `json:"type,omitempty"`
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"`
	Text             string `json:"text,omitempty"`
	Message          string `json:"message,omitempty"`
}

func (a *codexAdapter) Execute(ctx context.Context, prompt string, priorContext string) (Result, error) {
	fullPrompt := BuildPrompt(prompt, priorContext)

	outFile, err := os.CreateTemp("", "mittens-codex-output-*.txt")
	if err != nil {
		return Result{}, fmt.Errorf("create codex output file: %w", err)
	}
	outPath := outFile.Name()
	if err := outFile.Close(); err != nil {
		_ = os.Remove(outPath)
		return Result{}, fmt.Errorf("close codex output file: %w", err)
	}
	defer os.Remove(outPath)

	args := []string{"exec"}
	if a.skipPermsFlag != "" {
		args = append(args, a.skipPermsFlag)
	}
	args = append(args,
		"--json",
		"--color", "never",
		"--cd", a.workDir,
		"--ephemeral",
		"--skip-git-repo-check",
		"--output-last-message", outPath,
	)
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	args = append(args, "-")

	cmd := a.newCmd(ctx, "codex", args...)
	cmd.Dir = a.workDir
	cmd.Stdin = strings.NewReader(fullPrompt)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start codex: %w", err)
	}

	var inputTokens, outputTokens int
	if err := consumeCodexStdout(stdout, func(ev codexStreamEvent) {
		for _, activity := range codexActivities(ev) {
			emitActivity(a.onActivity, a.onToolUse, activity)
		}
		if ev.Type == "turn.completed" {
			inputTokens = ev.Usage.InputTokens
			outputTokens = ev.Usage.OutputTokens
		}
	}); err != nil {
		_ = cmd.Wait()
		return Result{}, fmt.Errorf("read codex output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return Result{}, fmt.Errorf("execute codex (exit %d): %s", exitErr.ExitCode(), msg)
		}
		return Result{}, fmt.Errorf("execute codex: %w", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return Result{}, fmt.Errorf("read codex output: %w", err)
	}

	return Result{
		Output:       strings.TrimSpace(string(data)),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}, nil
}

func consumeCodexStdout(stdout io.Reader, onEvent func(codexStreamEvent)) error {
	reader := bufio.NewReader(stdout)
	lineBuf := make([]byte, 0, 64*1024)
	droppingLine := false

	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 && !droppingLine {
			if len(lineBuf)+len(fragment) > codexStreamLineLimit {
				lineBuf = lineBuf[:0]
				droppingLine = true
			} else {
				lineBuf = append(lineBuf, fragment...)
			}
		}

		if err == bufio.ErrBufferFull {
			continue
		}

		if !droppingLine {
			consumeCodexStreamLine(lineBuf, onEvent)
		}
		lineBuf = lineBuf[:0]
		droppingLine = false

		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func consumeCodexStreamLine(line []byte, onEvent func(codexStreamEvent)) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}

	var ev codexStreamEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	onEvent(ev)
}

func codexActivities(ev codexStreamEvent) []Activity {
	item, phase, ok := codexActivityItem(ev)
	if !ok {
		return nil
	}
	if activity, ok := codexItemActivity(item, phase); ok {
		return []Activity{activity}
	}
	return nil
}

func codexActivityItem(ev codexStreamEvent) (codexItem, ActivityPhase, bool) {
	switch ev.Type {
	case "item.started":
		return ev.Item, ActivityPhaseStarted, ev.Item.Type != ""
	case "item.completed":
		return ev.Item, ActivityPhaseCompleted, ev.Item.Type != ""
	}

	if ev.Msg != nil && ev.Msg.Type != "" {
		item := *ev.Msg
		if item.ID == "" {
			item.ID = ev.ID
		}
		if phase, ok := codexTopLevelPhase(item); ok {
			return item, phase, true
		}
	}

	item := codexItem{
		ID:               ev.ID,
		Type:             ev.Type,
		Command:          ev.Command,
		AggregatedOutput: ev.AggregatedOutput,
		ExitCode:         ev.ExitCode,
		Status:           ev.Status,
		Text:             ev.Text,
		Message:          ev.Message,
	}
	if phase, ok := codexTopLevelPhase(item); ok {
		return item, phase, true
	}

	return codexItem{}, "", false
}

func codexItemActivity(item codexItem, phase ActivityPhase) (Activity, bool) {
	switch item.Type {
	case "command_execution":
		return Activity{
			Kind:    ActivityKindTool,
			Phase:   phase,
			Name:    "command_execution",
			Summary: summarizeCodexCommand(item, phase),
		}, true
	case "agent_message", "assistant_message":
		if phase != ActivityPhaseCompleted {
			return Activity{}, false
		}
		if summary := shortSummary(item.responseText()); summary != "" {
			return Activity{
				Kind:    ActivityKindStatus,
				Phase:   ActivityPhaseCompleted,
				Name:    "response",
				Summary: summary,
			}, true
		}
	}
	return Activity{}, false
}

func codexTopLevelPhase(item codexItem) (ActivityPhase, bool) {
	switch item.Type {
	case "agent_message", "assistant_message":
		return ActivityPhaseCompleted, true
	case "command_execution":
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "in_progress", "started", "running":
			return ActivityPhaseStarted, true
		case "completed", "failed", "declined", "done", "finished":
			return ActivityPhaseCompleted, true
		}
		if item.ExitCode != nil || strings.TrimSpace(item.AggregatedOutput) != "" {
			return ActivityPhaseCompleted, true
		}
		if strings.TrimSpace(item.Command) != "" {
			return ActivityPhaseStarted, true
		}
	}
	return "", false
}

func (item codexItem) responseText() string {
	if item.Text != "" {
		return item.Text
	}
	return item.Message
}

func summarizeCodexCommand(item codexItem, phase ActivityPhase) string {
	switch phase {
	case ActivityPhaseStarted:
		return shortSummary(item.Command)
	case ActivityPhaseCompleted:
		if summary := shortSummary(item.AggregatedOutput); summary != "" {
			return summary
		}
		if item.ExitCode != nil {
			return fmt.Sprintf("exit %d", *item.ExitCode)
		}
		return shortSummary(item.Command)
	default:
		return ""
	}
}

func (a *codexAdapter) ClearSession() error {
	// codex exec --ephemeral does not retain session state between tasks.
	return nil
}

func (a *codexAdapter) ForceClean() error {
	// codex exec --ephemeral does not retain session state between tasks.
	return nil
}

func (a *codexAdapter) Healthy() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}
