package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateAllProjects(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	projects := filepath.Join(tmpHome, "projects")

	// A legacy project with a flag-line config and no policy.yaml.
	legacyDir := filepath.Join(projects, "legacy-proj")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "config"), []byte("--network-host\n--no-firewall\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An already-structured project that must be left untouched.
	v2Dir := filepath.Join(projects, "v2-proj")
	if err := os.MkdirAll(v2Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []byte("provider:\n  name: claude\n")
	if err := os.WriteFile(filepath.Join(v2Dir, "policy.yaml"), existing, 0o644); err != nil {
		t.Fatal(err)
	}

	d := &doctorReport{}
	d.migrateAllProjects(nil)

	if d.problems != 0 {
		t.Fatalf("migrateAllProjects reported %d problems, want 0", d.problems)
	}

	// Legacy project should now have a policy.yaml reflecting the converted flags.
	data, err := os.ReadFile(filepath.Join(legacyDir, "policy.yaml"))
	if err != nil {
		t.Fatalf("expected migrated policy.yaml: %v", err)
	}
	got := string(data)
	for _, want := range []string{"mode: host", "firewall: disabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("migrated policy missing %q:\n%s", want, got)
		}
	}

	// The already-structured project must be untouched.
	after, err := os.ReadFile(filepath.Join(v2Dir, "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(existing) {
		t.Errorf("v2 policy.yaml was modified:\n%s", after)
	}
}
