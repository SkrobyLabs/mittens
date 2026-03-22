package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// ParseFlag — none / boolean
// ---------------------------------------------------------------------------

func TestParseFlag_None(t *testing.T) {
	tests := []struct {
		name       string
		flagName   string
		args       []string
		wantConsum int
		wantMatch  bool
		wantEnable bool
		wantAll    bool
	}{
		{
			name:       "simple boolean enable",
			flagName:   "--aws",
			args:       []string{"--aws"},
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: true,
		},
		{
			name:       "no- prefix disables",
			flagName:   "--no-aws",
			args:       []string{"--no-aws"},
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: false,
		},
		{
			name:       "-all suffix sets AllMode",
			flagName:   "--aws-all",
			args:       []string{"--aws-all"},
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: true,
			wantAll:    true,
		},
		{
			name:       "unmatched flag",
			flagName:   "--aws",
			args:       []string{"--gcp"},
			wantConsum: 0,
			wantMatch:  false,
		},
		{
			name:       "empty args",
			flagName:   "--aws",
			args:       nil,
			wantConsum: 0,
			wantMatch:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := &Extension{
				Flags: []ExtensionFlag{{Name: tc.flagName, Arg: "none"}},
			}
			consumed, matched := ext.ParseFlag(tc.args)
			if consumed != tc.wantConsum {
				t.Errorf("consumed = %d, want %d", consumed, tc.wantConsum)
			}
			if matched != tc.wantMatch {
				t.Errorf("matched = %v, want %v", matched, tc.wantMatch)
			}
			if matched {
				if ext.Enabled != tc.wantEnable {
					t.Errorf("Enabled = %v, want %v", ext.Enabled, tc.wantEnable)
				}
				if ext.AllMode != tc.wantAll {
					t.Errorf("AllMode = %v, want %v", ext.AllMode, tc.wantAll)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseFlag — csv
// ---------------------------------------------------------------------------

func TestParseFlag_CSV(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantConsum int
		wantMatch  bool
		wantArgs   []string
	}{
		{
			name:       "single value",
			args:       []string{"--aws", "prod"},
			wantConsum: 2,
			wantMatch:  true,
			wantArgs:   []string{"prod"},
		},
		{
			name:       "comma-separated values",
			args:       []string{"--aws", "dev,staging,prod"},
			wantConsum: 2,
			wantMatch:  true,
			wantArgs:   []string{"dev", "staging", "prod"},
		},
		{
			name:       "missing value",
			args:       []string{"--aws"},
			wantConsum: 0,
			wantMatch:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := &Extension{
				Flags: []ExtensionFlag{{Name: "--aws", Arg: "csv"}},
			}
			consumed, matched := ext.ParseFlag(tc.args)
			if consumed != tc.wantConsum {
				t.Errorf("consumed = %d, want %d", consumed, tc.wantConsum)
			}
			if matched != tc.wantMatch {
				t.Errorf("matched = %v, want %v", matched, tc.wantMatch)
			}
			if matched {
				if len(ext.Args) != len(tc.wantArgs) {
					t.Fatalf("Args = %v, want %v", ext.Args, tc.wantArgs)
				}
				for i, a := range ext.Args {
					if a != tc.wantArgs[i] {
						t.Errorf("Args[%d] = %q, want %q", i, a, tc.wantArgs[i])
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseFlag — enum
// ---------------------------------------------------------------------------

func TestParseFlag_Enum(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		multi      bool
		wantConsum int
		wantMatch  bool
		wantEnable bool
		wantArgs   []string
		wantRawArg string
	}{
		{
			name:       "valid single enum value",
			args:       []string{"--dotnet", "8"},
			wantConsum: 2,
			wantMatch:  true,
			wantEnable: true,
			wantArgs:   []string{"8"},
			wantRawArg: "8",
		},
		{
			name:       "invalid enum value just enables",
			args:       []string{"--dotnet", "99"},
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: true,
			wantArgs:   nil,
		},
		{
			name:       "no value just enables",
			args:       []string{"--dotnet"},
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: true,
			wantArgs:   nil,
		},
		{
			name:       "multi comma-separated valid values",
			args:       []string{"--dotnet", "8,10"},
			multi:      true,
			wantConsum: 2,
			wantMatch:  true,
			wantEnable: true,
			wantArgs:   []string{"8", "10"},
			wantRawArg: "8,10",
		},
		{
			name:       "multi rejects partial invalid",
			args:       []string{"--dotnet", "8,99"},
			multi:      true,
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: true,
			wantArgs:   nil,
		},
		{
			name:       "non-multi rejects comma-separated",
			args:       []string{"--dotnet", "8,10"},
			multi:      false,
			wantConsum: 1,
			wantMatch:  true,
			wantEnable: true,
			wantArgs:   nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := &Extension{
				Flags: []ExtensionFlag{{
					Name:       "--dotnet",
					Arg:        "enum",
					EnumValues: []string{"8", "9", "10"},
					Multi:      tc.multi,
				}},
			}
			consumed, matched := ext.ParseFlag(tc.args)
			if consumed != tc.wantConsum {
				t.Errorf("consumed = %d, want %d", consumed, tc.wantConsum)
			}
			if matched != tc.wantMatch {
				t.Errorf("matched = %v, want %v", matched, tc.wantMatch)
			}
			if ext.Enabled != tc.wantEnable {
				t.Errorf("Enabled = %v, want %v", ext.Enabled, tc.wantEnable)
			}
			if len(ext.Args) != len(tc.wantArgs) {
				t.Fatalf("Args = %v, want %v", ext.Args, tc.wantArgs)
			}
			for i, a := range ext.Args {
				if a != tc.wantArgs[i] {
					t.Errorf("Args[%d] = %q, want %q", i, a, tc.wantArgs[i])
				}
			}
			if ext.RawArg != tc.wantRawArg {
				t.Errorf("RawArg = %q, want %q", ext.RawArg, tc.wantRawArg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseFlag — path
// ---------------------------------------------------------------------------

func TestParseFlag_Path(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantConsum int
		wantMatch  bool
		wantRawArg string
	}{
		{
			name:       "consumes next arg",
			args:       []string{"--firewall", "/etc/my.conf"},
			wantConsum: 2,
			wantMatch:  true,
			wantRawArg: "/etc/my.conf",
		},
		{
			name:       "missing value",
			args:       []string{"--firewall"},
			wantConsum: 0,
			wantMatch:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ext := &Extension{
				Flags: []ExtensionFlag{{Name: "--firewall", Arg: "path"}},
			}
			consumed, matched := ext.ParseFlag(tc.args)
			if consumed != tc.wantConsum {
				t.Errorf("consumed = %d, want %d", consumed, tc.wantConsum)
			}
			if matched != tc.wantMatch {
				t.Errorf("matched = %v, want %v", matched, tc.wantMatch)
			}
			if matched {
				if ext.RawArg != tc.wantRawArg {
					t.Errorf("RawArg = %q, want %q", ext.RawArg, tc.wantRawArg)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ImageTagPart
// ---------------------------------------------------------------------------

func TestImageTagPart(t *testing.T) {
	tests := []struct {
		name   string
		ext    Extension
		want   string
	}{
		{
			name: "simple substitution",
			ext: Extension{
				RawArg: "8",
				Build:  &BuildConfig{ImageTag: "dotnet{{.Arg}}"},
			},
			want: "dotnet8",
		},
		{
			name: "comma sanitized to hyphen",
			ext: Extension{
				RawArg: "8,10",
				Build:  &BuildConfig{ImageTag: "dotnet{{.Arg}}"},
			},
			want: "dotnet8-10",
		},
		{
			name: "nil Build returns empty",
			ext:  Extension{},
			want: "",
		},
		{
			name: "empty ImageTag returns empty",
			ext: Extension{
				Build: &BuildConfig{ImageTag: ""},
			},
			want: "",
		},
		{
			name: "static tag no substitution",
			ext: Extension{
				Build: &BuildConfig{ImageTag: "aws"},
			},
			want: "aws",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ext.ImageTagPart()
			if got != tc.want {
				t.Errorf("ImageTagPart() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildArgs
// ---------------------------------------------------------------------------

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name string
		ext  Extension
		want map[string]string
	}{
		{
			name: "simple substitution",
			ext: Extension{
				RawArg: "8",
				Build: &BuildConfig{
					Args: map[string]string{"DOTNET_VERSION": "{{.Arg}}"},
				},
			},
			want: map[string]string{"DOTNET_VERSION": "8"},
		},
		{
			name: "conditional template with value",
			ext: Extension{
				RawArg: "1.24",
				Build: &BuildConfig{
					Args: map[string]string{"GO_VERSION": "{{if .Arg}}{{.Arg}}{{else}}latest{{end}}"},
				},
			},
			want: map[string]string{"GO_VERSION": "1.24"},
		},
		{
			name: "conditional template without value",
			ext: Extension{
				RawArg: "",
				Build: &BuildConfig{
					Args: map[string]string{"GO_VERSION": "{{if .Arg}}{{.Arg}}{{else}}latest{{end}}"},
				},
			},
			want: map[string]string{"GO_VERSION": "latest"},
		},
		{
			name: "nil Build returns nil",
			ext:  Extension{},
			want: nil,
		},
		{
			name: "empty Args returns nil",
			ext: Extension{
				Build: &BuildConfig{Args: map[string]string{}},
			},
			want: nil,
		},
		{
			name: "static value no template",
			ext: Extension{
				Build: &BuildConfig{
					Args: map[string]string{"FOO": "bar"},
				},
			},
			want: map[string]string{"FOO": "bar"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ext.BuildArgs()
			if tc.want == nil {
				if got != nil {
					t.Errorf("BuildArgs() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("BuildArgs() has %d entries, want %d: %v", len(got), len(tc.want), got)
			}
			for k, wantV := range tc.want {
				if got[k] != wantV {
					t.Errorf("BuildArgs()[%q] = %q, want %q", k, got[k], wantV)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// expandTilde
// ---------------------------------------------------------------------------

func TestExpandTilde(t *testing.T) {
	tests := []struct {
		name string
		path string
		home string
		want string
	}{
		{name: "bare tilde", path: "~", home: "/home/user", want: "/home/user"},
		{name: "tilde slash subdir", path: "~/foo/bar", home: "/home/user", want: "/home/user/foo/bar"},
		{name: "absolute path unchanged", path: "/etc/foo", home: "/home/user", want: "/etc/foo"},
		{name: "empty home", path: "~", home: "", want: ""},
		{name: "relative path unchanged", path: "foo/bar", home: "/home/user", want: "foo/bar"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := expandTilde(tc.path, tc.home)
			if got != tc.want {
				t.Errorf("expandTilde(%q, %q) = %q, want %q", tc.path, tc.home, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExpandedMounts (filesystem-backed)
// ---------------------------------------------------------------------------

func TestExpandedMounts(t *testing.T) {
	tmpDir := t.TempDir()
	home := tmpDir

	// Create a directory and a file for condition checks.
	existingDir := filepath.Join(tmpDir, "existing-dir")
	if err := os.MkdirAll(existingDir, 0755); err != nil {
		t.Fatal(err)
	}
	existingFile := filepath.Join(tmpDir, "existing-file")
	if err := os.WriteFile(existingFile, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}

	ext := &Extension{
		Mounts: []MountConfig{
			{Src: "~/existing-dir", Dst: "/mnt/dir", When: "dir_exists"},
			{Src: "~/existing-file", Dst: "/mnt/file", When: "file_exists", Mode: "rw"},
			{Src: "~/nonexistent", Dst: "/mnt/nope", When: "dir_exists"},
			{Src: "~/existing-file", Dst: "/mnt/wrong", When: "dir_exists"}, // file not dir
			{Src: "~/existing-dir", Dst: "/mnt/always", Env: map[string]string{"K": "V"}},
		},
	}

	containerHome := "/home/testuser"
	mounts := ext.ExpandedMounts(home, containerHome)

	if len(mounts) != 3 {
		t.Fatalf("got %d mounts, want 3: %+v", len(mounts), mounts)
	}

	// First: dir_exists succeeds, mode defaults to "ro".
	if mounts[0].Src != existingDir || mounts[0].Mode != "ro" {
		t.Errorf("mount[0] = %+v, want src=%q mode=ro", mounts[0], existingDir)
	}

	// Second: file_exists succeeds, explicit mode "rw".
	if mounts[1].Src != existingFile || mounts[1].Mode != "rw" {
		t.Errorf("mount[1] = %+v, want src=%q mode=rw", mounts[1], existingFile)
	}

	// Third: no when condition, always included, has env.
	if mounts[2].Env["K"] != "V" {
		t.Errorf("mount[2] Env = %v, want K=V", mounts[2].Env)
	}
}
