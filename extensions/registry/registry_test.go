package registry

import (
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
