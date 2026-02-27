package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// FileExists / DirExists
// ---------------------------------------------------------------------------

func TestFileExists(t *testing.T) {
	tmp := t.TempDir()

	f := filepath.Join(tmp, "exists.txt")
	if err := os.WriteFile(f, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"existing file", f, true},
		{"directory not a file", tmp, false},
		{"missing path", filepath.Join(tmp, "nope"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileExists(tc.path); got != tc.want {
				t.Errorf("FileExists(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestDirExists(t *testing.T) {
	tmp := t.TempDir()

	f := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(f, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"existing dir", tmp, true},
		{"file not a dir", f, false},
		{"missing path", filepath.Join(tmp, "nope"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DirExists(tc.path); got != tc.want {
				t.Errorf("DirExists(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CopyFile
// ---------------------------------------------------------------------------

func TestCopyFile(t *testing.T) {
	tmp := t.TempDir()

	src := filepath.Join(tmp, "src.txt")
	content := "hello, world!"
	if err := os.WriteFile(src, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmp, "dst.txt")
	if err := CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}

	// Verify content.
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("copied content = %q, want %q", string(data), content)
	}

	// Verify permissions preserved.
	srcInfo, _ := os.Stat(src)
	dstInfo, _ := os.Stat(dst)
	if srcInfo.Mode() != dstInfo.Mode() {
		t.Errorf("mode = %v, want %v", dstInfo.Mode(), srcInfo.Mode())
	}
}

// ---------------------------------------------------------------------------
// CopyDir
// ---------------------------------------------------------------------------

func TestCopyDir(t *testing.T) {
	tmp := t.TempDir()

	// Build a nested tree.
	src := filepath.Join(tmp, "src")
	os.MkdirAll(filepath.Join(src, "sub"), 0755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bbb"), 0644)

	dst := filepath.Join(tmp, "dst")
	if err := CopyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	// Verify files exist with correct content.
	for _, rel := range []string{"a.txt", filepath.Join("sub", "b.txt")} {
		data, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			continue
		}
		srcData, _ := os.ReadFile(filepath.Join(src, rel))
		if string(data) != string(srcData) {
			t.Errorf("%s content mismatch", rel)
		}
	}
}
