//go:build windows

package main

import "golang.org/x/term"

type terminalModeState struct {
	state *term.State
}

func makeInputRawPreserveOutput(fd int) (*terminalModeState, error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return &terminalModeState{state: state}, nil
}

func restoreTerminalMode(fd int, state *terminalModeState) error {
	if state == nil || state.state == nil {
		return nil
	}
	return term.Restore(fd, state.state)
}
