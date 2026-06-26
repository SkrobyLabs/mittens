package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyJQAssignment(t *testing.T) {
	obj := make(map[string]interface{})

	applyJQAssignment(obj, ".general.enableAutoUpdate = false")
	applyJQAssignment(obj, ".general.enableAutoUpdateNotification = false")

	general, ok := obj["general"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'general' to be a map")
	}
	if general["enableAutoUpdate"] != false {
		t.Errorf("expected enableAutoUpdate=false, got %v", general["enableAutoUpdate"])
	}
	if general["enableAutoUpdateNotification"] != false {
		t.Errorf("expected enableAutoUpdateNotification=false, got %v", general["enableAutoUpdateNotification"])
	}
}

func TestApplyJQAssignmentString(t *testing.T) {
	obj := make(map[string]interface{})
	applyJQAssignment(obj, `.theme = "dark"`)

	if obj["theme"] != "dark" {
		t.Errorf("expected theme='dark', got %v", obj["theme"])
	}
}

func TestApplyJQAssignmentNumber(t *testing.T) {
	obj := make(map[string]interface{})
	applyJQAssignment(obj, ".timeout = 30")

	if obj["timeout"] != float64(30) {
		t.Errorf("expected timeout=30, got %v (%T)", obj["timeout"], obj["timeout"])
	}
}

func TestSetJSONKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// Create initial file.
	os.WriteFile(path, []byte(`{"existing": true}`), 0644)

	setJSONKey(path, "yolo", true)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}

	if obj["yolo"] != true {
		t.Errorf("expected yolo=true, got %v", obj["yolo"])
	}
	if obj["existing"] != true {
		t.Errorf("expected existing=true to be preserved")
	}
}

func TestReadWriteJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Read non-existent file should return empty map.
	obj := readJSONFile(path)
	if len(obj) != 0 {
		t.Errorf("expected empty map for non-existent file")
	}

	// Write and read back.
	obj["key"] = "value"
	obj["nested"] = map[string]interface{}{"a": float64(1)}
	writeJSONFile(path, obj)

	result := readJSONFile(path)
	if result["key"] != "value" {
		t.Errorf("expected key='value', got %v", result["key"])
	}
}

func TestNormalizeEmptyJSONObjectFileRewritesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	normalizeEmptyJSONObjectFile(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("file contents = %q, want %q", string(data), "{}\n")
	}
}

func TestNormalizeEmptyJSONObjectFilePreservesNonEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	if err := os.WriteFile(path, []byte("{\"existing\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	normalizeEmptyJSONObjectFile(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"existing\":true}\n" {
		t.Fatalf("file contents changed: %q", string(data))
	}
}

func TestCopyConfigFilesCopiesPersistedDirsAndGlobs(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	home := filepath.Join(root, "home")
	aidir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(filepath.Join(staging, ".codex", "sessions", "2026", "04"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(aidir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".codex", "config.toml"), []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".codex", "sessions", "2026", "04", "session.jsonl"), []byte("session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".codex", "state_5.sqlite"), []byte("sqlite\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		ConfigMount:     staging,
		AIConfigDir:     ".codex",
		AIDir:           aidir,
		AISettingsFile:  "config.toml",
		AIProjectFile:   "AGENTS.md",
		AIPersistDirs:   []string{"sessions"},
		AIPersistGlobs:  []string{"state_*.sqlite*"},
		AIConfigSubdirs: []string{},
		AIPluginFiles:   []string{},
		AIPersistFiles:  nil,
	}

	copyConfigFiles(cfg)

	for _, rel := range []string{"config.toml", "sessions/2026/04/session.jsonl", "state_5.sqlite"} {
		if _, err := os.Stat(filepath.Join(aidir, rel)); err != nil {
			t.Fatalf("expected copied path %s: %v", rel, err)
		}
	}
}

func TestCopyConfigFilesCopiesWholePluginDir(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	aidir := filepath.Join(root, "home", ".claude")
	// Installed plugin payload lives under cache/ — this is where enabled
	// plugins load their hooks from and must come along.
	cacheHook := filepath.Join(staging, ".claude", "plugins", "cache", "mp", "plug", "1.0.0", "hooks", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(cacheHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheHook, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".claude", "plugins", "installed_plugins.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(aidir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		ConfigMount:    staging,
		AIConfigDir:    ".claude",
		AIDir:          aidir,
		AISettingsFile: "settings.json",
		AIProjectFile:  "CLAUDE.md",
		AIPluginDir:    "plugins",
	}

	copyConfigFiles(cfg)

	for _, rel := range []string{"plugins/cache/mp/plug/1.0.0/hooks/hooks.json", "plugins/installed_plugins.json"} {
		if _, err := os.Stat(filepath.Join(aidir, rel)); err != nil {
			t.Fatalf("expected copied plugin path %s: %v", rel, err)
		}
	}
}

func TestRewriteHostConfigPathsRemapsConfigDirButNotWorkspace(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "claude")
	aidir := filepath.Join(home, ".claude")
	pluginDir := filepath.Join(aidir, "plugins")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// installPath under the host config dir must be remapped; an
	// identity-mounted workspace path under the host home must NOT be.
	registry := `{"installPath":"/Users/alice/.claude/plugins/cache/mp/plug/1.0.0"}`
	if err := os.WriteFile(filepath.Join(pluginDir, "installed_plugins.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := `{"extraKnownMarketplaces":{"x":{"source":{"path":"/Users/alice/.claude/plugins/marketplaces/x"}}},"workspace":"/Users/alice/Documents/proj"}`
	if err := os.WriteFile(filepath.Join(aidir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		HostHome:       "/Users/alice",
		AIHome:         home,
		AIConfigDir:    ".claude",
		AIDir:          aidir,
		AISettingsFile: "settings.json",
		AIPluginDir:    "plugins",
		AIPluginFiles:  []string{"installed_plugins.json"},
	}

	rewriteHostConfigPaths(cfg)

	gotReg, _ := os.ReadFile(filepath.Join(pluginDir, "installed_plugins.json"))
	wantInstall := aidir + "/plugins/cache/mp/plug/1.0.0"
	if !strings.Contains(string(gotReg), wantInstall) {
		t.Fatalf("installPath not remapped: %s", gotReg)
	}
	gotSet, _ := os.ReadFile(filepath.Join(aidir, "settings.json"))
	if !strings.Contains(string(gotSet), aidir+"/plugins/marketplaces/x") {
		t.Fatalf("marketplace path not remapped: %s", gotSet)
	}
	if !strings.Contains(string(gotSet), "/Users/alice/Documents/proj") {
		t.Fatalf("identity-mounted workspace path was wrongly remapped: %s", gotSet)
	}
}

func TestCopyConfigFilesDoesNotNormalizeEmptyTomlSettings(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	aidir := filepath.Join(root, "home", ".codex")
	if err := os.MkdirAll(filepath.Join(staging, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(aidir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ".codex", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config{
		ConfigMount:      staging,
		AIConfigDir:      ".codex",
		AIDir:            aidir,
		AISettingsFile:   "config.toml",
		AIProjectFile:    "AGENTS.md",
		AISettingsFormat: "toml",
	}

	copyConfigFiles(cfg)

	data, err := os.ReadFile(filepath.Join(aidir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "" {
		t.Fatalf("config.toml contents = %q, want empty", string(data))
	}
}

func TestCredExpiresAt(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected int64
	}{
		{
			name:     "Claude root expiresAt",
			json:     `{"expiresAt": 1700000000000}`,
			expected: 1700000000000,
		},
		{
			name:     "Codex expires_at",
			json:     `{"expires_at": 1700000000000}`,
			expected: 1700000000000,
		},
		{
			name:     "Gemini expiry_date",
			json:     `{"expiry_date": 1700000000000}`,
			expected: 1700000000000,
		},
		{
			name:     "nested claudeAiOauth",
			json:     `{"claudeAiOauth": {"expiresAt": 1700000000000}}`,
			expected: 1700000000000,
		},
		{
			name:     "highest wins",
			json:     `{"expiresAt": 100, "claudeAiOauth": {"expiresAt": 1700000000000}}`,
			expected: 1700000000000,
		},
		{
			name:     "empty",
			json:     `{}`,
			expected: 0,
		},
		{
			name:     "invalid",
			json:     `not json`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := credExpiresAt([]byte(tt.json))
			if got != tt.expected {
				t.Errorf("credExpiresAt(%s) = %d, want %d", tt.json, got, tt.expected)
			}
		})
	}
}
