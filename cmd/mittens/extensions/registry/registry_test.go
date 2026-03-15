package registry

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// ---------------------------------------------------------------------------
// LoadExtensions (in-memory FS)
// ---------------------------------------------------------------------------

func TestLoadExtensions(t *testing.T) {
	fs := fstest.MapFS{
		"extensions/aws/extension.yaml": &fstest.MapFile{
			Data: []byte(`name: aws
description: AWS credentials
flags:
  - name: "--aws"
    arg: csv
  - name: "--aws-all"
  - name: "--no-aws"
`),
		},
		"extensions/firewall/extension.yaml": &fstest.MapFile{
			Data: []byte(`name: firewall
description: Network firewall
default_on: true
flags:
  - name: "--firewall"
    arg: path
  - name: "--no-firewall"
`),
		},
	}

	exts, err := LoadExtensions(fs)
	if err != nil {
		t.Fatal(err)
	}

	if len(exts) != 2 {
		t.Fatalf("got %d extensions, want 2", len(exts))
	}

	// Extensions should be sorted by name.
	if exts[0].Name != "aws" {
		t.Errorf("exts[0].Name = %q, want aws", exts[0].Name)
	}
	if exts[1].Name != "firewall" {
		t.Errorf("exts[1].Name = %q, want firewall", exts[1].Name)
	}

	// Default-on extension should be enabled.
	if exts[0].Enabled {
		t.Error("aws should not be enabled by default")
	}
	if !exts[1].Enabled {
		t.Error("firewall should be enabled by default (default_on: true)")
	}

	// Flag count.
	if len(exts[0].Flags) != 3 {
		t.Errorf("aws has %d flags, want 3", len(exts[0].Flags))
	}
}

func TestLoadExtensions_InvalidYAML(t *testing.T) {
	fs := fstest.MapFS{
		"extensions/bad/extension.yaml": &fstest.MapFile{
			Data: []byte("name: bad\nflags: [invalid: yaml: {{{\n"),
		},
	}

	_, err := LoadExtensions(fs)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadExtensions_Empty(t *testing.T) {
	fs := fstest.MapFS{}

	exts, err := LoadExtensions(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 0 {
		t.Errorf("expected 0 extensions, got %d", len(exts))
	}
}

// ---------------------------------------------------------------------------
// Register overwrite behavior
// ---------------------------------------------------------------------------

func TestRegister_OverwriteDoesNotPanic(t *testing.T) {
	// First registration.
	Register("test-overwrite", &Registration{
		List: func() ([]string, error) { return []string{"a"}, nil },
	})
	// Second registration should overwrite without panicking.
	Register("test-overwrite", &Registration{
		List: func() ([]string, error) { return []string{"b"}, nil },
	})

	lr := GetListResolver("test-overwrite")
	if lr == nil {
		t.Fatal("expected list resolver after overwrite")
	}
	items, err := lr()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != "b" {
		t.Errorf("expected [b], got %v", items)
	}

	// Clean up.
	delete(resolvers, "test-overwrite")
}

// ---------------------------------------------------------------------------
// LoadAllExtensions
// ---------------------------------------------------------------------------

func TestLoadAllExtensions_EmbedFallback(t *testing.T) {
	embeddedFS := fstest.MapFS{
		"extensions/aws/extension.yaml": &fstest.MapFile{
			Data: []byte("name: aws\ndescription: AWS creds\n"),
		},
	}

	// Non-existent bundled and user dirs: should fall back to embed.
	exts, err := LoadAllExtensions("/nonexistent/bundled", "/nonexistent/user", embeddedFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 {
		t.Fatalf("expected 1 extension, got %d", len(exts))
	}
	if exts[0].Name != "aws" {
		t.Errorf("expected aws, got %s", exts[0].Name)
	}
	if exts[0].Source != "built-in" {
		t.Errorf("expected source built-in, got %s", exts[0].Source)
	}
}

func TestLoadAllExtensions_DiskOverridesEmbed(t *testing.T) {
	tmpDir := t.TempDir()
	bundledDir := filepath.Join(tmpDir, "bundled")
	awsDir := filepath.Join(bundledDir, "aws")
	if err := os.MkdirAll(awsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awsDir, "extension.yaml"), []byte("name: aws\ndescription: Disk AWS\n"), 0644); err != nil {
		t.Fatal(err)
	}

	embeddedFS := fstest.MapFS{
		"extensions/aws/extension.yaml": &fstest.MapFile{
			Data: []byte("name: aws\ndescription: Embed AWS\n"),
		},
	}

	exts, err := LoadAllExtensions(bundledDir, "/nonexistent", embeddedFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 {
		t.Fatalf("expected 1, got %d", len(exts))
	}
	if exts[0].Description != "Disk AWS" {
		t.Errorf("expected disk version, got %q", exts[0].Description)
	}
}

func TestLoadAllExtensions_UserYAMLOnly(t *testing.T) {
	tmpDir := t.TempDir()
	userDir := filepath.Join(tmpDir, "user")
	customDir := filepath.Join(userDir, "custom-db")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "extension.yaml"), []byte("name: custom-db\ndescription: Custom database config\n"), 0644); err != nil {
		t.Fatal(err)
	}

	embeddedFS := fstest.MapFS{}

	exts, err := LoadAllExtensions("/nonexistent", userDir, embeddedFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 {
		t.Fatalf("expected 1, got %d", len(exts))
	}
	if exts[0].Name != "custom-db" {
		t.Errorf("expected custom-db, got %s", exts[0].Name)
	}
	if exts[0].Source != "user" {
		t.Errorf("expected source user, got %s", exts[0].Source)
	}
}

func TestLoadAllExtensions_UserOverridesBuiltIn(t *testing.T) {
	tmpDir := t.TempDir()
	userDir := filepath.Join(tmpDir, "user")
	awsDir := filepath.Join(userDir, "aws")
	if err := os.MkdirAll(awsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awsDir, "extension.yaml"), []byte("name: aws\ndescription: Custom AWS\n"), 0644); err != nil {
		t.Fatal(err)
	}

	embeddedFS := fstest.MapFS{
		"extensions/aws/extension.yaml": &fstest.MapFile{
			Data: []byte("name: aws\ndescription: Built-in AWS\n"),
		},
	}

	exts, err := LoadAllExtensions("/nonexistent", userDir, embeddedFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 {
		t.Fatalf("expected 1, got %d", len(exts))
	}
	if exts[0].Description != "Custom AWS" {
		t.Errorf("expected user version, got %q", exts[0].Description)
	}
	if exts[0].Source != "user (overrides built-in)" {
		t.Errorf("expected 'user (overrides built-in)', got %q", exts[0].Source)
	}
}
