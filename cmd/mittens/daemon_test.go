package main

import (
	"path/filepath"
	"testing"
)

func TestParseDaemonArgs_DefaultSocket(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())

	socketPath, filtered, err := parseDaemonArgs(nil)
	if err != nil {
		t.Fatalf("parseDaemonArgs: %v", err)
	}
	if socketPath != filepath.Join(ConfigHome(), "runtime.sock") {
		t.Fatalf("socketPath = %q, want default runtime socket", socketPath)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered = %v, want empty", filtered)
	}
}

func TestParseDaemonArgs_CustomSocketAndFlags(t *testing.T) {
	socketPath, filtered, err := parseDaemonArgs([]string{"--socket", "/tmp/custom.sock", "--provider", "codex"})
	if err != nil {
		t.Fatalf("parseDaemonArgs: %v", err)
	}
	if socketPath != "/tmp/custom.sock" {
		t.Fatalf("socketPath = %q, want /tmp/custom.sock", socketPath)
	}
	if len(filtered) != 2 || filtered[0] != "--provider" || filtered[1] != "codex" {
		t.Fatalf("filtered = %v, want provider args preserved", filtered)
	}
}
