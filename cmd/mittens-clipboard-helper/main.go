// Command clipboard-sync is a Windows-native clipboard image poller.
//
// It polls the Windows clipboard every second for image data, converts it
// to PNG, and writes it to a shared directory. This mirrors the behavior of
// clipboard-sync.sh (macOS) so the container-side xclip shim works unchanged.
//
// Usage: clipboard-sync.exe <output-dir>
package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: clipboard-sync <output-dir>\n")
		os.Exit(1)
	}

	dir := os.Args[1]

	// Create output directory structure.
	clientsDir := filepath.Join(dir, "clients")
	if err := os.MkdirAll(clientsDir, 0o700); err != nil {
		fatal("cannot create output directory: %v", err)
	}

	// Write PID file.
	pidFile := filepath.Join(dir, "clipboard-sync.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		fatal("cannot write PID file: %v", err)
	}
	defer os.Remove(pidFile)

	// Handle signals for graceful cleanup.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		os.Remove(pidFile)
		os.Exit(0)
	}()

	var lastHash string
	var lastSeqNum uint32
	var lastClientSeen time.Time
	const idleTimeout = 5 * time.Minute
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[clipboard-sync] "+format+"\n", args...)
	}

	logf("started, output dir: %s", dir)
	lastClientSeen = time.Now() // grace period: treat startup as activity

	for {
		time.Sleep(1 * time.Second)

		// Write heartbeat.
		writeEpoch(filepath.Join(dir, "clipboard.heartbeat"))

		// Check for active clients; exit if idle too long.
		if hasActiveClients(clientsDir) {
			lastClientSeen = time.Now()
		} else if time.Since(lastClientSeen) > idleTimeout {
			logf("no active clients for %v, exiting", idleTimeout)
			os.Remove(pidFile)
			os.Exit(0)
		}

		// Check if the clipboard has changed using the sequence number.
		// This avoids opening/locking the clipboard when nothing changed.
		seqNum := clipboardSequenceNumber()
		if seqNum == lastSeqNum && lastSeqNum != 0 {
			// Clipboard unchanged — just refresh the timestamp so the
			// container-side freshness check passes.
			if lastHash != "" {
				writeEpoch(filepath.Join(dir, "clipboard.updated_at"))
			}
			continue
		}
		logf("clipboard changed: seq %d → %d", lastSeqNum, seqNum)
		lastSeqNum = seqNum

		// Check if clipboard has image data before opening it.
		if !hasClipboardImage() {
			logf("no image format on clipboard, refreshing timestamp (hasImage=%v)", lastHash != "")
			// No image on clipboard, but if we previously captured one,
			// keep refreshing updated_at so the container can still use it.
			if lastHash != "" {
				writeEpoch(filepath.Join(dir, "clipboard.updated_at"))
			}
			continue
		}

		// Clipboard changed and has image data — read it.
		logf("reading clipboard image...")
		pngData, err := readClipboardImage()
		if err != nil {
			logf("read error: %v", err)
			if lastHash != "" {
				writeEpoch(filepath.Join(dir, "clipboard.updated_at"))
			}
			continue
		}

		logf("read %d bytes of PNG data", len(pngData))
		writeFile(filepath.Join(dir, "clipboard.error"), "")
		writeFile(filepath.Join(dir, "clipboard.state"), "image\n")

		// Only write if the image actually changed (by MD5 hash).
		newHash := fmt.Sprintf("%x", md5.Sum(pngData))
		if newHash != lastHash {
			logf("new image hash: %s (was: %s), writing clipboard.png", newHash, lastHash)
			tmpFile := filepath.Join(dir, fmt.Sprintf("clipboard.png.tmp.%d", os.Getpid()))
			if err := os.WriteFile(tmpFile, pngData, 0o600); err == nil {
				if err := os.Rename(tmpFile, filepath.Join(dir, "clipboard.png")); err != nil {
					logf("rename error: %v", err)
					os.Remove(tmpFile)
				} else {
					lastHash = newHash
				}
			} else {
				logf("write error: %v", err)
			}
		} else {
			logf("image unchanged (hash: %s)", newHash)
		}
		writeEpoch(filepath.Join(dir, "clipboard.updated_at"))
	}
}

// writeEpoch writes the current Unix epoch (seconds) to a file.
func writeEpoch(path string) {
	writeFile(path, strconv.FormatInt(time.Now().Unix(), 10)+"\n")
}

// writeFile writes content to a file, ignoring errors (best-effort).
func writeFile(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0o600)
}

// syncClientDirs copies clipboard state files to all registered client directories.
func syncClientDirs(dir, clientsDir string) {
	entries, err := os.ReadDir(clientsDir)
	if err != nil {
		return
	}

	stateFiles := []string{
		"clipboard.state",
		"clipboard.error",
		"clipboard.updated_at",
		"clipboard.png",
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".path") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(clientsDir, entry.Name()))
		if err != nil {
			continue
		}
		target := strings.TrimSpace(string(data))
		if target == "" {
			continue
		}

		// Check if target directory exists.
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			// Stale registration — remove.
			os.Remove(filepath.Join(clientsDir, entry.Name()))
			continue
		}

		for _, name := range stateFiles {
			src := filepath.Join(dir, name)
			dst := filepath.Join(target, name)
			if _, err := os.Stat(src); err == nil {
				installFileAtomic(src, dst)
			} else {
				os.Remove(dst)
			}
		}
	}
}

// installFileAtomic copies src to dst via a temporary file + rename.
func installFileAtomic(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp.%d", dst, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
	}
}

// hasActiveClients returns true if any client heartbeat file in the clients
// directory has been updated within the last 2 minutes.
func hasActiveClients(clientsDir string) bool {
	entries, err := os.ReadDir(clientsDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".heartbeat") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) <= 2*time.Minute {
			return true
		}
		// Stale heartbeat — clean it up.
		os.Remove(filepath.Join(clientsDir, entry.Name()))
	}
	return false
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[clipboard-sync] "+format+"\n", a...)
	os.Exit(1)
}
