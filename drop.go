package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Skroby/mittens/internal/fileutil"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// Bracketed paste mode escape sequences.
var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

const maxDropFileSize = 100 << 20 // 100 MB

// pathMapping maps a host path prefix to a container path prefix.
type pathMapping struct {
	hostPrefix      string
	containerPrefix string
}

// PathMapper translates host paths to container paths and copies unmapped files.
type PathMapper struct {
	mappings         []pathMapping
	dropDir          string // host-side temp directory for file copies
	containerDropDir string // always /tmp/mittens-drops
}

// Translate converts a host path to a container path.
// For paths within mapped volumes, it does a prefix replacement.
// For other paths that exist on disk, it copies the file to the drop zone.
// Returns the original string if the path doesn't exist on disk.
func (m *PathMapper) Translate(hostPath string) string {
	// Unescape backslash-escaped spaces (macOS Terminal.app drag-drop).
	cleaned := strings.ReplaceAll(hostPath, "\\ ", " ")

	// Strip surrounding quotes if present.
	if len(cleaned) >= 2 {
		if (cleaned[0] == '\'' && cleaned[len(cleaned)-1] == '\'') ||
			(cleaned[0] == '"' && cleaned[len(cleaned)-1] == '"') {
			cleaned = cleaned[1 : len(cleaned)-1]
		}
	}

	// Try prefix mappings (longest match first — mappings should be ordered).
	for _, pm := range m.mappings {
		if strings.HasPrefix(cleaned, pm.hostPrefix) {
			rel := strings.TrimPrefix(cleaned, pm.hostPrefix)
			result := pm.containerPrefix + rel
			// Re-escape spaces if the original had escaped spaces.
			if strings.Contains(hostPath, "\\ ") {
				result = strings.ReplaceAll(result, " ", "\\ ")
			}
			return result
		}
	}

	// Path is outside all mounts — copy to drop zone if it exists on disk.
	if m.dropDir == "" {
		return hostPath
	}

	info, err := os.Stat(cleaned)
	if err != nil || info.IsDir() {
		return hostPath // not a file, pass through unchanged
	}
	if info.Size() > maxDropFileSize {
		return hostPath // too large, skip
	}

	dst := filepath.Join(m.dropDir, filepath.Base(cleaned))
	// Handle filename collisions.
	if _, err := os.Stat(dst); err == nil {
		ext := filepath.Ext(dst)
		base := strings.TrimSuffix(filepath.Base(dst), ext)
		for i := 1; ; i++ {
			candidate := filepath.Join(m.dropDir, base+"_"+dropItoa(i)+ext)
			if _, err := os.Stat(candidate); err != nil {
				dst = candidate
				break
			}
		}
	}

	if err := fileutil.CopyFile(cleaned, dst); err != nil {
		return hostPath // copy failed, pass through
	}

	containerPath := m.containerDropDir + "/" + filepath.Base(dst)
	if strings.Contains(hostPath, "\\ ") {
		containerPath = strings.ReplaceAll(containerPath, " ", "\\ ")
	}
	return containerPath
}

// dropItoa is a simple int-to-string without importing strconv.
func dropItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// DropProxy wraps an io.Reader (typically os.Stdin) and intercepts
// bracketed paste sequences to translate host paths to container paths.
type DropProxy struct {
	inner  io.Reader
	mapper *PathMapper

	state    dropState
	escBuf   []byte       // accumulates potential escape sequence bytes
	pasteBuf bytes.Buffer // accumulates pasted content
	outBuf   bytes.Buffer // buffered translated output ready to be read
}

type dropState int

const (
	stateNormal  dropState = iota
	stateEscSeq            // seen ESC, accumulating escape sequence
	statePasting           // inside bracketed paste
)

// NewDropProxy creates a new DropProxy that translates paths in pasted content.
func NewDropProxy(inner io.Reader, mapper *PathMapper) *DropProxy {
	return &DropProxy{
		inner:  inner,
		mapper: mapper,
	}
}

// Read implements io.Reader. Normal keystrokes pass through directly.
// Bracketed paste content is buffered, paths are translated, then forwarded.
func (d *DropProxy) Read(p []byte) (int, error) {
	// Drain any buffered output first.
	if d.outBuf.Len() > 0 {
		return d.outBuf.Read(p)
	}

	// Read from inner.
	buf := make([]byte, len(p))
	n, err := d.inner.Read(buf)
	if n == 0 && err != nil {
		// EOF/error with no data — flush any incomplete state.
		if len(d.escBuf) > 0 {
			d.outBuf.Write(d.escBuf)
			d.escBuf = d.escBuf[:0]
		}
		if d.pasteBuf.Len() > 0 {
			d.outBuf.Write(d.pasteBuf.Bytes())
			d.pasteBuf.Reset()
		}
		d.state = stateNormal
		if d.outBuf.Len() > 0 {
			rn, _ := d.outBuf.Read(p)
			return rn, err
		}
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	data := buf[:n]

	for i := 0; i < len(data); i++ {
		b := data[i]

		switch d.state {
		case stateNormal:
			if b == 0x1b { // ESC
				d.state = stateEscSeq
				d.escBuf = append(d.escBuf[:0], b)
			} else {
				d.outBuf.WriteByte(b)
			}

		case stateEscSeq:
			d.escBuf = append(d.escBuf, b)
			if len(d.escBuf) <= len(pasteStart) {
				if d.escBuf[len(d.escBuf)-1] == pasteStart[len(d.escBuf)-1] {
					// Still matching paste start sequence.
					if len(d.escBuf) == len(pasteStart) {
						// Full paste start detected — switch to pasting mode.
						d.state = statePasting
						d.pasteBuf.Reset()
						d.escBuf = d.escBuf[:0]
					}
				} else {
					// Not a paste start — flush accumulated bytes and return to normal.
					d.outBuf.Write(d.escBuf)
					d.escBuf = d.escBuf[:0]
					d.state = stateNormal
				}
			} else {
				// Escape sequence longer than paste start — not a paste, flush.
				d.outBuf.Write(d.escBuf)
				d.escBuf = d.escBuf[:0]
				d.state = stateNormal
			}

		case statePasting:
			d.pasteBuf.WriteByte(b)
			// Check if paste buffer ends with pasteEnd sequence.
			if d.pasteBuf.Len() >= len(pasteEnd) {
				tail := d.pasteBuf.Bytes()[d.pasteBuf.Len()-len(pasteEnd):]
				if bytes.Equal(tail, pasteEnd) {
					// Remove the paste end sequence from content.
					content := d.pasteBuf.Bytes()[:d.pasteBuf.Len()-len(pasteEnd)]
					translated := d.translatePaths(string(content))
					// Emit: pasteStart + translated + pasteEnd
					d.outBuf.Write(pasteStart)
					d.outBuf.WriteString(translated)
					d.outBuf.Write(pasteEnd)
					d.pasteBuf.Reset()
					d.state = stateNormal
				}
			}
		}
	}

	// On EOF/error, flush any incomplete escape or paste state.
	if err != nil {
		if len(d.escBuf) > 0 {
			d.outBuf.Write(d.escBuf)
			d.escBuf = d.escBuf[:0]
		}
		if d.pasteBuf.Len() > 0 {
			d.outBuf.Write(d.pasteBuf.Bytes())
			d.pasteBuf.Reset()
		}
		d.state = stateNormal
	}

	if d.outBuf.Len() > 0 {
		n, _ := d.outBuf.Read(p)
		return n, err
	}
	if err != nil {
		return 0, err
	}
	// All input consumed into state buffers; need more data.
	return 0, nil
}

// translatePaths finds absolute paths in the pasted content and translates them.
func (d *DropProxy) translatePaths(content string) string {
	// Split on whitespace boundaries to find potential paths.
	// Handles: single path, multiple paths separated by spaces/newlines.
	// Also handles backslash-escaped spaces in paths.
	paths := splitPastePaths(content)
	if len(paths) == 0 {
		return content
	}

	result := content
	for _, p := range paths {
		translated := d.mapper.Translate(p)
		if translated != p {
			result = strings.Replace(result, p, translated, 1)
		}
	}
	return result
}

// splitPastePaths extracts potential absolute file paths from pasted content.
// Handles:
// - Simple paths: /Users/foo/bar.png
// - Backslash-escaped spaces: /Users/foo/my\ file.png
// - Quoted paths: '/Users/foo/my file.png' or "/Users/foo/my file.png"
func splitPastePaths(content string) []string {
	var paths []string
	i := 0
	for i < len(content) {
		// Skip whitespace.
		if content[i] == ' ' || content[i] == '\t' || content[i] == '\n' || content[i] == '\r' {
			i++
			continue
		}

		// Quoted path.
		if (content[i] == '\'' || content[i] == '"') && i+1 < len(content) {
			quote := content[i]
			end := strings.IndexByte(content[i+1:], quote)
			if end >= 0 {
				path := content[i : i+2+end]
				// Check if the inner content starts with /
				inner := content[i+1 : i+1+end]
				if len(inner) > 0 && inner[0] == '/' {
					paths = append(paths, path)
				}
				i += 2 + end
				continue
			}
		}

		// Unquoted path starting with /.
		if content[i] == '/' {
			// Consume until unescaped whitespace or end.
			start := i
			for i < len(content) {
				if content[i] == '\\' && i+1 < len(content) && content[i+1] == ' ' {
					i += 2 // skip escaped space
					continue
				}
				if content[i] == ' ' || content[i] == '\t' || content[i] == '\n' || content[i] == '\r' {
					break
				}
				i++
			}
			paths = append(paths, content[start:i])
			continue
		}

		// Non-path content — skip to next whitespace.
		for i < len(content) && content[i] != ' ' && content[i] != '\t' && content[i] != '\n' && content[i] != '\r' {
			i++
		}
	}
	return paths
}

// StartPTYProxy creates a pty pair so Docker still sees a real TTY on stdin.
// A goroutine reads os.Stdin through the DropProxy and writes to the pty master.
// Returns the pty slave (to use as Docker's stdin) and a cleanup function.
func StartPTYProxy(proxy *DropProxy) (slave *os.File, cleanup func(), err error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, nil, fmt.Errorf("stdin is not a terminal")
	}

	master, slave, err := pty.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("pty open: %w", err)
	}

	// Inherit terminal size.
	if sz, err := pty.GetsizeFull(os.Stdin); err == nil {
		_ = pty.Setsize(master, sz)
	}

	// Set real stdin to raw mode so keystrokes pass through immediately.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		master.Close()
		slave.Close()
		return nil, nil, fmt.Errorf("make raw: %w", err)
	}

	// Forward SIGWINCH to resize the pty.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			if sz, err := pty.GetsizeFull(os.Stdin); err == nil {
				_ = pty.Setsize(master, sz)
			}
		}
	}()

	// Goroutine: os.Stdin → DropProxy → pty master.
	go func() {
		_, _ = io.Copy(master, proxy)
		master.Close()
	}()

	cleanup = func() {
		signal.Stop(sigCh)
		close(sigCh)
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		master.Close()
		slave.Close()
	}

	return slave, cleanup, nil
}
