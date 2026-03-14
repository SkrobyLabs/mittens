package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PathMapper.Translate
// ---------------------------------------------------------------------------

func TestPathMapper_Translate_WorkspaceMapping(t *testing.T) {
	m := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}

	got := m.Translate("/Users/peter/project/src/main.go")
	want := "/workspace/src/main.go"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}
}

func TestPathMapper_Translate_ExactRoot(t *testing.T) {
	m := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}

	got := m.Translate("/Users/peter/project")
	want := "/workspace"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}
}

func TestPathMapper_Translate_ExtraDir(t *testing.T) {
	m := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
			{"/Users/peter/other", "/Users/peter/other"},
		},
	}

	got := m.Translate("/Users/peter/other/file.txt")
	want := "/Users/peter/other/file.txt"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}
}

func TestPathMapper_Translate_EscapedSpaces(t *testing.T) {
	m := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/my project", "/workspace"},
		},
	}

	got := m.Translate("/Users/peter/my\\ project/src/main.go")
	want := "/workspace/src/main.go"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}
}

func TestPathMapper_Translate_QuotedPath(t *testing.T) {
	m := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/my project", "/workspace"},
		},
	}

	got := m.Translate("'/Users/peter/my project/file.txt'")
	want := "/workspace/file.txt"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}
}

func TestPathMapper_Translate_UnmappedExistingFile(t *testing.T) {
	dropDir := t.TempDir()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "image.png")
	os.WriteFile(srcFile, []byte("fake-png"), 0644)

	m := &PathMapper{
		mappings:         []pathMapping{{"/Users/peter/project", "/workspace"}},
		dropDir:          dropDir,
		containerDropDir: "/tmp/mittens-drops",
	}

	got := m.Translate(srcFile)
	want := "/tmp/mittens-drops/image.png"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}

	// Verify file was actually copied.
	copied := filepath.Join(dropDir, "image.png")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("copied file not found: %v", err)
	}
	if string(data) != "fake-png" {
		t.Errorf("copied content = %q, want %q", string(data), "fake-png")
	}
}

func TestPathMapper_Translate_UnmappedNonExistent(t *testing.T) {
	m := &PathMapper{
		mappings:         []pathMapping{{"/Users/peter/project", "/workspace"}},
		dropDir:          t.TempDir(),
		containerDropDir: "/tmp/mittens-drops",
	}

	path := "/nonexistent/path/file.txt"
	got := m.Translate(path)
	if got != path {
		t.Errorf("Translate() = %q, want passthrough %q", got, path)
	}
}

func TestPathMapper_Translate_FilenameCollision(t *testing.T) {
	dropDir := t.TempDir()
	srcDir := t.TempDir()

	// Create two files with the same name in different dirs.
	os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("first"), 0644)
	os.WriteFile(filepath.Join(dropDir, "file.txt"), []byte("existing"), 0644) // pre-existing collision

	m := &PathMapper{
		dropDir:          dropDir,
		containerDropDir: "/tmp/mittens-drops",
	}

	got := m.Translate(filepath.Join(srcDir, "file.txt"))
	want := "/tmp/mittens-drops/file_1.txt"
	if got != want {
		t.Errorf("Translate() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// splitPastePaths
// ---------------------------------------------------------------------------

func TestSplitPastePaths_SinglePath(t *testing.T) {
	paths := splitPastePaths("/Users/peter/file.txt")
	if len(paths) != 1 || paths[0] != "/Users/peter/file.txt" {
		t.Errorf("splitPastePaths = %v, want [/Users/peter/file.txt]", paths)
	}
}

func TestSplitPastePaths_MultiplePaths(t *testing.T) {
	paths := splitPastePaths("/Users/peter/a.txt /Users/peter/b.txt")
	if len(paths) != 2 {
		t.Fatalf("splitPastePaths = %v, want 2 paths", paths)
	}
	if paths[0] != "/Users/peter/a.txt" || paths[1] != "/Users/peter/b.txt" {
		t.Errorf("splitPastePaths = %v", paths)
	}
}

func TestSplitPastePaths_EscapedSpaces(t *testing.T) {
	paths := splitPastePaths("/Users/peter/my\\ file.txt")
	if len(paths) != 1 || paths[0] != "/Users/peter/my\\ file.txt" {
		t.Errorf("splitPastePaths = %v, want [/Users/peter/my\\ file.txt]", paths)
	}
}

func TestSplitPastePaths_QuotedPath(t *testing.T) {
	paths := splitPastePaths("'/Users/peter/my file.txt'")
	if len(paths) != 1 || paths[0] != "'/Users/peter/my file.txt'" {
		t.Errorf("splitPastePaths = %v", paths)
	}
}

func TestSplitPastePaths_MixedContent(t *testing.T) {
	paths := splitPastePaths("hello /Users/peter/file.txt world")
	if len(paths) != 1 || paths[0] != "/Users/peter/file.txt" {
		t.Errorf("splitPastePaths = %v, want [/Users/peter/file.txt]", paths)
	}
}

func TestSplitPastePaths_NonAbsolutePath(t *testing.T) {
	paths := splitPastePaths("relative/path.txt")
	if len(paths) != 0 {
		t.Errorf("splitPastePaths = %v, want empty (non-absolute paths ignored)", paths)
	}
}

// ---------------------------------------------------------------------------
// DropProxy — end-to-end
// ---------------------------------------------------------------------------

func TestDropProxy_NormalInput(t *testing.T) {
	input := "hello world"
	mapper := &PathMapper{}
	proxy := NewDropProxy(strings.NewReader(input), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != input {
		t.Errorf("output = %q, want %q", string(out), input)
	}
}

func TestDropProxy_PasteWithPathTranslation(t *testing.T) {
	hostPath := "/Users/peter/project/src/main.go"
	containerPath := "/workspace/src/main.go"
	paste := string(pasteStart) + hostPath + string(pasteEnd)

	mapper := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}
	proxy := NewDropProxy(strings.NewReader(paste), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	want := string(pasteStart) + containerPath + string(pasteEnd)
	if string(out) != want {
		t.Errorf("output = %q, want %q", string(out), want)
	}
}

func TestDropProxy_PasteWithMultiplePaths(t *testing.T) {
	content := "/Users/peter/project/a.go /Users/peter/project/b.go"
	paste := string(pasteStart) + content + string(pasteEnd)

	mapper := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}
	proxy := NewDropProxy(strings.NewReader(paste), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	want := string(pasteStart) + "/workspace/a.go /workspace/b.go" + string(pasteEnd)
	if string(out) != want {
		t.Errorf("output = %q, want %q", string(out), want)
	}
}

func TestDropProxy_MixedNormalAndPaste(t *testing.T) {
	input := "before" + string(pasteStart) + "/Users/peter/project/file.go" + string(pasteEnd) + "after"
	mapper := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}
	proxy := NewDropProxy(strings.NewReader(input), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	want := "before" + string(pasteStart) + "/workspace/file.go" + string(pasteEnd) + "after"
	if string(out) != want {
		t.Errorf("output = %q, want %q", string(out), want)
	}
}

func TestDropProxy_NonPasteEscapeSequence(t *testing.T) {
	// A normal escape sequence (e.g. arrow key) should pass through unchanged.
	input := "\x1b[A" // up arrow
	mapper := &PathMapper{}
	proxy := NewDropProxy(strings.NewReader(input), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != input {
		t.Errorf("output = %q, want %q", string(out), input)
	}
}

func TestDropProxy_PasteWithNoPath(t *testing.T) {
	content := "just some text"
	paste := string(pasteStart) + content + string(pasteEnd)

	mapper := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}
	proxy := NewDropProxy(strings.NewReader(paste), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	want := string(pasteStart) + content + string(pasteEnd)
	if string(out) != want {
		t.Errorf("output = %q, want %q", string(out), want)
	}
}

func TestDropProxy_SmallReads(t *testing.T) {
	// Simulate byte-at-a-time reads from the inner reader.
	hostPath := "/Users/peter/project/file.go"
	paste := string(pasteStart) + hostPath + string(pasteEnd)

	mapper := &PathMapper{
		mappings: []pathMapping{
			{"/Users/peter/project", "/workspace"},
		},
	}
	proxy := NewDropProxy(newSlowReader([]byte(paste)), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	want := string(pasteStart) + "/workspace/file.go" + string(pasteEnd)
	if string(out) != want {
		t.Errorf("output = %q, want %q", string(out), want)
	}
}

// slowReader returns one byte at a time.
type slowReader struct {
	data []byte
	pos  int
}

func newSlowReader(data []byte) *slowReader {
	return &slowReader{data: data}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.pos]
	r.pos++
	if r.pos >= len(r.data) {
		return 1, io.EOF
	}
	return 1, nil
}

// ---------------------------------------------------------------------------
// DropProxy — drag-and-drop with unmapped file copy
// ---------------------------------------------------------------------------

func TestDropProxy_UnmappedFileCopied(t *testing.T) {
	dropDir := t.TempDir()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "photo.jpg")
	os.WriteFile(srcFile, []byte("jpeg-data"), 0644)

	mapper := &PathMapper{
		mappings:         []pathMapping{{"/Users/peter/project", "/workspace"}},
		dropDir:          dropDir,
		containerDropDir: "/tmp/mittens-drops",
	}
	paste := string(pasteStart) + srcFile + string(pasteEnd)
	proxy := NewDropProxy(strings.NewReader(paste), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	want := string(pasteStart) + "/tmp/mittens-drops/photo.jpg" + string(pasteEnd)
	if string(out) != want {
		t.Errorf("output = %q, want %q", string(out), want)
	}

	// Verify file copied.
	data, _ := os.ReadFile(filepath.Join(dropDir, "photo.jpg"))
	if string(data) != "jpeg-data" {
		t.Errorf("copied content = %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// assembleDockerArgs — drop zone mount
// ---------------------------------------------------------------------------

func TestAssembleDockerArgs_DropZoneMount(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		Provider:          DefaultProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-drop",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Drop zone should be mounted.
	if a.dropDir == "" {
		t.Fatal("dropDir not set")
	}
	wantMount := a.dropDir + ":/tmp/mittens-drops:ro"
	if !argPairExists(args, "-v", wantMount) {
		t.Errorf("missing drop zone mount, want -v %s", wantMount)
	}

	// dropDir should be tracked for cleanup.
	found := false
	for _, d := range a.tempDirs {
		if d == a.dropDir {
			found = true
			break
		}
	}
	if !found {
		t.Error("dropDir not tracked in tempDirs for cleanup")
	}
}

// ---------------------------------------------------------------------------
// newDropProxy — mapping construction
// ---------------------------------------------------------------------------

func TestNewStdinProxy_NilWithoutDropDir(t *testing.T) {
	a := &App{
		WorkspaceMountSrc: "/tmp/ws",
	}
	stdin, cleanup := a.newStdinProxy()
	if stdin != nil || cleanup != nil {
		t.Error("newStdinProxy should return nil when dropDir is empty")
	}
}

// ---------------------------------------------------------------------------
// itoa
// ---------------------------------------------------------------------------

func TestDropItoa(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{100, "100"},
	}
	for _, tc := range tests {
		got := dropItoa(tc.n)
		if got != tc.want {
			t.Errorf("dropItoa(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// DropProxy with bytes.Reader (simulates paste with EOF at end)
// ---------------------------------------------------------------------------

func TestDropProxy_IncompleteEscapeAtEOF(t *testing.T) {
	// An ESC byte at the very end — should be flushed.
	input := []byte("text\x1b")
	mapper := &PathMapper{}
	proxy := NewDropProxy(bytes.NewReader(input), mapper)

	out, err := io.ReadAll(proxy)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(input) {
		t.Errorf("output = %q, want %q", string(out), string(input))
	}
}
