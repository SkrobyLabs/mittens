package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ProjectDir
// ---------------------------------------------------------------------------

func TestProjectDir(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		wantShort bool   // true => result is just sanitized (≤200)
		wantExact string // if non-empty, exact match
	}{
		{
			name:      "short path sanitized",
			workspace: "/Users/bob/src",
			wantShort: true,
			wantExact: "-Users-bob-src",
		},
		{
			name:      "special chars replaced",
			workspace: "/foo/bar baz@1.0",
			wantShort: true,
			wantExact: "-foo-bar-baz-1-0",
		},
		{
			name:      "empty string",
			workspace: "",
			wantShort: true,
			wantExact: "",
		},
		{
			name:      "long path gets hash suffix",
			workspace: "/" + strings.Repeat("a", 250),
			wantShort: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectDir(tc.workspace)
			if tc.wantExact != "" {
				if got != tc.wantExact {
					t.Errorf("ProjectDir(%q) = %q, want %q", tc.workspace, got, tc.wantExact)
				}
			}
			if tc.wantShort {
				if len(got) > 200 {
					t.Errorf("expected short result, got len=%d", len(got))
				}
			} else {
				// Long path: first 200 chars + base36 suffix.
				if len(got) <= 200 {
					t.Errorf("expected truncated+hash result, got len=%d", len(got))
				}
				// Verify it starts with 200 chars of sanitized input.
				prefix := got[:200]
				for _, c := range prefix {
					if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
						t.Errorf("unexpected char %q in prefix", c)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// javaHashCode
// ---------------------------------------------------------------------------

func TestJavaHashCode(t *testing.T) {
	tests := []struct {
		input string
		want  int32
	}{
		{"", 0},
		{"hello", 99162322},
		{"a", 97},
		// Long string to exercise overflow/wrapping.
		{strings.Repeat("z", 100), func() int32 {
			var h int32
			for range 100 {
				h = h*31 + 'z'
			}
			return h
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := javaHashCode(tc.input)
			if got != tc.want {
				t.Errorf("javaHashCode(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// int32ToBase36
// ---------------------------------------------------------------------------

func TestInt32ToBase36(t *testing.T) {
	tests := []struct {
		name  string
		input int32
		want  string
	}{
		{"zero", 0, "0"},
		{"positive", 36, "10"},
		{"negative", -1, "-1"},
		{"min int32", math.MinInt32, "-zik0zk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := int32ToBase36(tc.input)
			if got != tc.want {
				t.Errorf("int32ToBase36(%d) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LoadProjectConfig / SaveProjectConfig (filesystem-backed)
// ---------------------------------------------------------------------------

func TestLoadProjectConfig(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/test/workspace"
	projDir := filepath.Join(tmpHome, "projects", ProjectDir(workspace))
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := "# a comment\n--aws dev\n\n# another comment\n--dind\n--dotnet 8\n"
	if err := os.WriteFile(filepath.Join(projDir, "config"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	flags, err := LoadProjectConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"--aws", "dev", "--dind", "--dotnet", "8"}
	if len(flags) != len(want) {
		t.Fatalf("got %v, want %v", flags, want)
	}
	for i, f := range flags {
		if f != want[i] {
			t.Errorf("flags[%d] = %q, want %q", i, f, want[i])
		}
	}
}

func TestLoadProjectConfig_Missing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	flags, err := LoadProjectConfig("/nonexistent/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if flags != nil {
		t.Errorf("expected nil for missing config, got %v", flags)
	}
}

func TestSaveProjectConfig_RoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/test/roundtrip"
	lines := []string{"--aws dev", "--dind", "--dotnet 8"}

	if err := SaveProjectConfig(workspace, lines); err != nil {
		t.Fatal(err)
	}

	// Read back raw to verify header comment is preserved.
	data, err := os.ReadFile(projectConfigPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# mittens project config") {
		t.Error("header comment not preserved")
	}

	// Load and verify flags.
	flags, err := LoadProjectConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--aws", "dev", "--dind", "--dotnet", "8"}
	if len(flags) != len(want) {
		t.Fatalf("got %v, want %v", flags, want)
	}
	for i, f := range flags {
		if f != want[i] {
			t.Errorf("flags[%d] = %q, want %q", i, f, want[i])
		}
	}
}

func TestLoadProfileConfig_Missing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	pc, err := LoadProfileConfig("/test/no-such-project")
	if err != nil {
		t.Fatal(err)
	}
	if pc == nil {
		t.Fatal("expected profile config object")
	}
	if len(pc.Profiles) != 0 {
		t.Fatalf("expected empty profiles map, got %v", pc.Profiles)
	}
}

// ---------------------------------------------------------------------------
// readConfigLines
// ---------------------------------------------------------------------------

func TestReadConfigLines(t *testing.T) {
	// Happy path: blank lines, comments, and real flags.
	tmp := filepath.Join(t.TempDir(), "config")
	content := "\n# comment\n--verbose\n--go 1.24\n\n# another comment\n--aws foo\n"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := readConfigLines(tmp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--verbose", "--go 1.24", "--aws foo"}
	if len(lines) != len(want) {
		t.Fatalf("got %v, want %v", lines, want)
	}
	for i, l := range lines {
		if l != want[i] {
			t.Errorf("lines[%d] = %q, want %q", i, l, want[i])
		}
	}

	// Missing file returns (nil, nil).
	lines, err = readConfigLines(filepath.Join(t.TempDir(), "no-such-file"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if lines != nil {
		t.Fatalf("expected nil lines for missing file, got %v", lines)
	}
}

// ---------------------------------------------------------------------------
// splitConfigFlags
// ---------------------------------------------------------------------------

func TestSplitConfigFlags(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "single flag",
			input: []string{"--verbose"},
			want:  []string{"--verbose"},
		},
		{
			name:  "flag with value",
			input: []string{"--go 1.24"},
			want:  []string{"--go", "1.24"},
		},
		{
			name:  "multiple lines mixed",
			input: []string{"--aws profile1,profile2", "--verbose"},
			want:  []string{"--aws", "profile1,profile2", "--verbose"},
		},
		{
			name:  "nil input",
			input: nil,
		},
		{
			name:  "empty slice",
			input: []string{},
		},
		{
			name:  "double-quoted value with spaces",
			input: []string{`--azure "Quix ISV Programme"`},
			want:  []string{"--azure", "Quix ISV Programme"},
		},
		{
			name:  "quoted csv values",
			input: []string{`--azure "sub-id-1,sub-id-2"`},
			want:  []string{"--azure", "sub-id-1,sub-id-2"},
		},
		{
			name:  "mixed quoted and unquoted",
			input: []string{`--azure "some value" --verbose`},
			want:  []string{"--azure", "some value", "--verbose"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitConfigFlags(tc.input)
			if tc.want == nil {
				if len(got) != 0 {
					t.Fatalf("expected empty result, got %v", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// writeConfigFile
// ---------------------------------------------------------------------------

func TestWriteConfigFile(t *testing.T) {
	// Write to a path where the parent subdir doesn't exist yet (tests MkdirAll).
	path := filepath.Join(t.TempDir(), "subdir", "config")

	if err := writeConfigFile(path, "# test header\n", []string{"--verbose", "--go 1.24"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# test header\n--verbose\n--go 1.24\n"
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

// ---------------------------------------------------------------------------
// SaveProfileConfig / LoadProfileConfig round-trip
// ---------------------------------------------------------------------------

func TestSaveProfileConfigRoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("MITTENS_HOME", tmpHome)

	workspace := "/test/profiles"
	pc := &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {
			"fast":    {Model: "haiku", Effort: "low"},
			"deep": {Model: "opus", Effort: "max"},
		},
	}}

	if err := SaveProfileConfig(workspace, pc); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProfileConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}

	if got := loaded.Profiles["claude"]["fast"]; got.Model != "haiku" || got.Effort != "low" {
		t.Fatalf("loaded fast preset = %v", got)
	}
	if got := loaded.Profiles["claude"]["deep"]; got.Model != "opus" || got.Effort != "max" {
		t.Fatalf("loaded deep preset = %v", got)
	}
}
