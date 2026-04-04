package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func captureStdoutFor(t *testing.T, run func() error) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = oldStdout })

	if err := run(); err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return strings.TrimSpace(buf.String())
}

func withVersionMetadata(t *testing.T, versionValue, commitValue, dateValue string) {
	t.Helper()

	oldVersion := version
	oldCommit := commit
	oldDate := date
	version = versionValue
	commit = commitValue
	date = dateValue
	t.Cleanup(func() {
		version = oldVersion
		commit = oldCommit
		date = oldDate
	})
}

func TestRunVersion_JSONHasVersionAndCommitOnly(t *testing.T) {
	withVersionMetadata(t, "2.4.6", "commit-abc", "2026-04-01T00:00:00Z")

	out := captureStdoutFor(t, func() error {
		return runMain([]string{"version", "--json"})
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON output %q: %v", out, err)
	}

	if len(got) != 2 {
		t.Fatalf("JSON output has %d keys, want exactly 2", len(got))
	}
	if got["version"] != "2.4.6" {
		t.Fatalf("JSON version = %v, want %q", got["version"], "2.4.6")
	}
	if got["commit"] != "commit-abc" {
		t.Fatalf("JSON commit = %v, want %q", got["commit"], "commit-abc")
	}
}

func TestRunVersion_PlainOutputIsHumanReadable(t *testing.T) {
	withVersionMetadata(t, "1.2.3", "commit-xyz", "2026-03-31T00:00:00Z")

	out := captureStdoutFor(t, func() error {
		return runMain([]string{"version"})
	})
	want := "mittens 1.2.3 (commit: commit-xyz, built: 2026-03-31T00:00:00Z)"
	if out != want {
		t.Fatalf("version output = %q, want %q", out, want)
	}
}

func TestRunHelp_HasVersionCommandEntry(t *testing.T) {
	out := captureStdoutFor(t, func() error {
		printHelp(nil)
		return nil
	})
	if !strings.Contains(out, "version [--json]") {
		t.Fatalf("help output missing version command entry: %s", out)
	}
	if !strings.Contains(out, "Show version information") {
		t.Fatalf("help output missing version command description: %s", out)
	}
	if !strings.Contains(out, "--pool") {
		t.Fatalf("help output missing --pool flag: %s", out)
	}
}

func TestRunMain_VersionAliasesStillSupported(t *testing.T) {
	withVersionMetadata(t, "0.0.1", "commit-alias", "2026-02-01T00:00:00Z")

	plain := captureStdoutFor(t, func() error {
		return runMain([]string{"version"})
	})
	long := captureStdoutFor(t, func() error {
		return runMain([]string{"--version"})
	})
	short := captureStdoutFor(t, func() error {
		return runMain([]string{"-V"})
	})

	if long != plain || short != plain {
		t.Fatalf("version aliases mismatch: plain=%q --version=%q -V=%q", plain, long, short)
	}
}
