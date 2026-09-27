package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSanitizeExtensionSource(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"https://user:secret@example.com/team/ext.git?token=private#hidden", "https://example.com/team/ext.git"},
		{"ssh://git:secret@example.com/team/ext.git?token=private#hidden", "ssh://example.com/team/ext.git"},
		{"git@example.com:team/ext.git?token=private#hidden", "example.com:team/ext.git"},
		{"file:///local/repo.git?token=private#hidden", "file:///local/repo.git"},
		{"https://user:secret@%invalid/ext.git?token=private", "unknown URL"},
		{"/local/source", "/local/source"},
		{"/local/source\n\x1b", "/local/source"},
	} {
		if got := SanitizeExtensionSource(tc.source); got != tc.want {
			t.Errorf("sanitized source = %q, want %q", got, tc.want)
		}
	}
}

func TestValidateExtensionName(t *testing.T) {
	for _, name := range []string{"aws", "custom-v2", "Custom_2.0"} {
		if err := ValidateExtensionName(name); err != nil {
			t.Errorf("valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", `..\outside`, "a/b", "-option", "name\n", "C:drive"} {
		if err := ValidateExtensionName(name); err == nil {
			t.Errorf("invalid name %q accepted", name)
		}
	}
}

func TestLoadExternalProvenanceAndKeepBuiltInOverrides(t *testing.T) {
	userDir := t.TempDir()
	extDir := filepath.Join(userDir, "aws")
	if err := os.Mkdir(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "extension.yaml"), []byte("name: aws\ndescription: custom AWS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	metadata := `{"source":"https://user:secret@example.com/ext.git?token=private#hidden","revision":"` + revision + `"}`
	if err := os.WriteFile(filepath.Join(extDir, ProvenanceFile), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	embedded := fstest.MapFS{"extensions/aws/extension.yaml": {Data: []byte("name: aws\ndescription: built-in\n")}}
	exts, err := LoadAllExtensions(filepath.Join(t.TempDir(), "absent"), userDir, embedded)
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 || exts[0].Description != "custom AWS" || exts[0].Source != "user (overrides built-in)" {
		t.Fatalf("override behavior changed: %+v", exts)
	}
	if exts[0].Provenance == nil || exts[0].Provenance.Source != "https://example.com/ext.git" || exts[0].Provenance.Revision != revision {
		t.Fatalf("source metadata = %+v", exts[0].Provenance)
	}
	if got := exts[0].ProvenanceLabel(); got != "source: https://example.com/ext.git; revision: "+revision {
		t.Fatalf("source label = %q", got)
	}
	if err := os.Remove(filepath.Join(extDir, ProvenanceFile)); err != nil {
		t.Fatal(err)
	}
	exts, err = LoadAllExtensions(filepath.Join(t.TempDir(), "absent"), userDir, embedded)
	if err != nil || len(exts) != 1 {
		t.Fatalf("loading legacy extension: %v, %v", exts, err)
	}
	if !strings.Contains(exts[0].ProvenanceLabel(), "source: unknown; revision: unknown") {
		t.Fatalf("legacy provenance not identified as unknown: %q", exts[0].ProvenanceLabel())
	}
}
