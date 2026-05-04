package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistConfigSnapshotCopiesSelectedRuntimeState(t *testing.T) {
	snapshotDir := t.TempDir()
	hostConfigDir := filepath.Join(t.TempDir(), ".codex")

	write := func(rel, contents string) {
		path := filepath.Join(snapshotDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("history.jsonl", "history\n")
	write("sessions/2026/04/session.jsonl", "session\n")
	write("state_5.sqlite", "sqlite\n")
	write("config.toml", "should-not-copy\n")

	if err := persistConfigSnapshot(snapshotDir, hostConfigDir, []string{"history.jsonl"}, []string{"sessions"}, []string{"state_*.sqlite*"}, false); err != nil {
		t.Fatalf("persistConfigSnapshot: %v", err)
	}

	for _, rel := range []string{"history.jsonl", "sessions/2026/04/session.jsonl", "state_5.sqlite"} {
		if _, err := os.Stat(filepath.Join(hostConfigDir, rel)); err != nil {
			t.Fatalf("expected persisted path %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(hostConfigDir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml should not have been persisted, err=%v", err)
	}
}
