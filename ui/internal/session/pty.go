package session

import (
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// PtyHandle wraps a PTY file descriptor and the underlying command.
type PtyHandle struct {
	File *os.File
	Cmd  *exec.Cmd
}

// StartPty starts a command in a new PTY with the given initial size.
func StartPty(cmd *exec.Cmd, rows, cols uint16) (*PtyHandle, error) {
	sz := &pty.Winsize{Rows: rows, Cols: cols}
	f, err := pty.StartWithSize(cmd, sz)
	if err != nil {
		return nil, err
	}
	return &PtyHandle{File: f, Cmd: cmd}, nil
}

// Resize changes the PTY window size.
func (p *PtyHandle) Resize(rows, cols uint16) error {
	return pty.Setsize(p.File, &pty.Winsize{Rows: rows, Cols: cols})
}

// ReadLoop reads from the PTY and writes to the given writer until EOF.
func (p *PtyHandle) ReadLoop(w io.Writer) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.File.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// WriteInput writes raw bytes to the PTY (terminal input).
func (p *PtyHandle) WriteInput(data []byte) (int, error) {
	return p.File.Write(data)
}

// Close closes the PTY file descriptor.
func (p *PtyHandle) Close() error {
	return p.File.Close()
}
