package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// ComputeImageTag
// ---------------------------------------------------------------------------

func TestComputeImageTag(t *testing.T) {
	tests := []struct {
		name       string
		extensions []*registry.Extension
		want       string
	}{
		{
			name:       "no extensions returns latest",
			extensions: nil,
			want:       "latest",
		},
		{
			name: "one extension with tag",
			extensions: []*registry.Extension{
				{
					Enabled: true,
					RawArg:  "8",
					Build:   &registry.BuildConfig{ImageTag: "dotnet{{.Arg}}"},
				},
			},
			want: "dotnet8",
		},
		{
			name: "multiple sorted",
			extensions: []*registry.Extension{
				{
					Enabled: true,
					Build:   &registry.BuildConfig{ImageTag: "kubectl"},
				},
				{
					Enabled: true,
					Build:   &registry.BuildConfig{ImageTag: "aws"},
				},
			},
			want: "aws-kubectl",
		},
		{
			name: "disabled skipped",
			extensions: []*registry.Extension{
				{
					Enabled: false,
					Build:   &registry.BuildConfig{ImageTag: "aws"},
				},
				{
					Enabled: true,
					Build:   &registry.BuildConfig{ImageTag: "gcp"},
				},
			},
			want: "gcp",
		},
		{
			name: "no build config returns latest",
			extensions: []*registry.Extension{
				{Enabled: true},
			},
			want: "latest",
		},
		{
			name: "all disabled returns latest",
			extensions: []*registry.Extension{
				{
					Enabled: false,
					Build:   &registry.BuildConfig{ImageTag: "aws"},
				},
			},
			want: "latest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeImageTag(tc.extensions)
			if got != tc.want {
				t.Errorf("ComputeImageTag() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PrepareExtendedBuildContext
// ---------------------------------------------------------------------------

func TestPrepareExtendedBuildContext(t *testing.T) {
	// Set up a source context dir with container/Dockerfile.
	sourceDir := t.TempDir()
	containerDir := filepath.Join(sourceDir, "container")
	if err := os.MkdirAll(containerDir, 0755); err != nil {
		t.Fatalf("creating container dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(containerDir, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatalf("writing Dockerfile: %v", err)
	}

	// Set up an external extension dir with build.sh and extension.yaml.
	extBase := t.TempDir()
	extDir := filepath.Join(extBase, "test-ext")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatalf("creating ext dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "build.sh"), []byte("#!/bin/sh\necho hello\n"), 0755); err != nil {
		t.Fatalf("writing build.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "extension.yaml"), []byte("name: test-ext\n"), 0644); err != nil {
		t.Fatalf("writing extension.yaml: %v", err)
	}

	exts := []*registry.Extension{
		{
			Name:    "test-ext",
			Enabled: true,
			Source:  "user",
			Build:   &registry.BuildConfig{Script: "build.sh"},
		},
	}

	tmpDir, cleanup, err := PrepareExtendedBuildContext(sourceDir, extBase, exts)
	if err != nil {
		t.Fatalf("PrepareExtendedBuildContext() error: %v", err)
	}
	if tmpDir == "" {
		t.Fatal("expected non-empty tmpDir")
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup func")
	}

	// Verify container/Dockerfile was copied.
	df := filepath.Join(tmpDir, "container", "Dockerfile")
	if _, err := os.Stat(df); err != nil {
		t.Fatalf("Dockerfile not found in build context: %v", err)
	}

	// Verify the external extension dir was copied.
	extBuild := filepath.Join(tmpDir, "extensions", "test-ext", "build.sh")
	if _, err := os.Stat(extBuild); err != nil {
		t.Fatalf("extension build.sh not found in build context: %v", err)
	}
	extYaml := filepath.Join(tmpDir, "extensions", "test-ext", "extension.yaml")
	if _, err := os.Stat(extYaml); err != nil {
		t.Fatalf("extension.yaml not found in build context: %v", err)
	}

	// Call cleanup and verify the temp dir is removed.
	cleanup()
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("expected tmpDir to be removed after cleanup, got err: %v", err)
	}
}

func TestPrepareExtendedBuildContext_NoExternalBuild(t *testing.T) {
	// When no external extensions have build scripts, should return empty.
	sourceDir := t.TempDir()
	exts := []*registry.Extension{
		{
			Name:    "builtin-ext",
			Enabled: true,
			Source:  "built-in",
			Build:   &registry.BuildConfig{Script: "build.sh"},
		},
	}

	tmpDir, cleanup, err := PrepareExtendedBuildContext(sourceDir, "", exts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpDir != "" {
		t.Errorf("expected empty tmpDir, got %q", tmpDir)
	}
	if cleanup != nil {
		t.Error("expected nil cleanup func")
	}
}

// ---------------------------------------------------------------------------
// CurrentUserIDs
// ---------------------------------------------------------------------------

func TestCurrentUserIDs(t *testing.T) {
	t.Run("mock hook", func(t *testing.T) {
		orig := platformCurrentUserIDs
		defer func() { platformCurrentUserIDs = orig }()

		platformCurrentUserIDs = func() (int, int) { return 1234, 5678 }

		uid, gid := CurrentUserIDs()
		if uid != 1234 {
			t.Errorf("uid = %d, want 1234", uid)
		}
		if gid != 5678 {
			t.Errorf("gid = %d, want 5678", gid)
		}
	})

	t.Run("default matches os", func(t *testing.T) {
		uid, gid := CurrentUserIDs()
		if uid != os.Getuid() {
			t.Errorf("uid = %d, want %d", uid, os.Getuid())
		}
		if gid != os.Getgid() {
			t.Errorf("gid = %d, want %d", gid, os.Getgid())
		}
	})
}
