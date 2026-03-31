package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// codexAdapter runs tasks via the Codex CLI in non-interactive exec mode.
type codexAdapter struct {
	workDir       string
	model         string
	skipPermsFlag string
	onToolUse     func(toolName, inputSummary string)
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

	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = a.workDir
	cmd.Stdin = strings.NewReader(fullPrompt)
	cmd.Stdout = io.Discard

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
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
		Output: strings.TrimSpace(string(data)),
	}, nil
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
