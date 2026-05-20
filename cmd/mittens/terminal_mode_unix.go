//go:build unix

package main

import (
	"golang.org/x/sys/unix"
)

type terminalModeState struct {
	termios unix.Termios
}

func makeInputRawPreserveOutput(fd int) (*terminalModeState, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, err
	}

	newState := *old
	newState.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	newState.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	newState.Cflag &^= unix.CSIZE | unix.PARENB
	newState.Cflag |= unix.CS8
	newState.Cc[unix.VMIN] = 1
	newState.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &newState); err != nil {
		return nil, err
	}
	return &terminalModeState{termios: *old}, nil
}

func restoreTerminalMode(fd int, state *terminalModeState) error {
	if state == nil {
		return nil
	}
	return unix.IoctlSetTermios(fd, ioctlWriteTermios, &state.termios)
}
