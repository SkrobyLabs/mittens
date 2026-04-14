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

func TestSetupNotificationHooksStripsInheritedNotifyHooksWhenNoNotify(t *testing.T) {
	dir := t.TempDir()
	cfg := &config{
		AIDir:            dir,
		AISettingsFile:   "settings.json",
		AISettingsFormat: "json",
		AIStopHookEvent:  "Stop",
		NoNotify:         true,
		BrokerPort:       "12345",
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{
  "hooks": {
    "Notification": [{
      "hooks": [
        {"type":"command","command":"MSG=$(jq -r '.message // \"needs attention\"'); /usr/local/bin/notify.sh notification \"$MSG\""},
        {"type":"command","command":"echo keep-notification"}
      ]
    }],
    "Stop": [{
      "hooks": [
        {"type":"command","command":"/usr/local/bin/notify.sh stop"},
        {"type":"command","command":"echo keep-stop"}
      ]
    }]
  }
}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	setupNotificationHooks(cfg)

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	if !json.Valid(data) {
		t.Fatal("expected valid json output")
	}
	if strings.Contains(text, "/usr/local/bin/notify.sh") {
		t.Fatalf("settings still contain notify hook: %s", text)
	}
	if !strings.Contains(text, "keep-notification") || !strings.Contains(text, "keep-stop") {
		t.Fatalf("settings should preserve unrelated hooks: %s", text)
	}
}

func TestSetupNotificationHooksStripsInheritedNotifyHooksWithoutBroker(t *testing.T) {
	dir := t.TempDir()
	cfg := &config{
		AIDir:            dir,
		AISettingsFile:   "settings.json",
		AISettingsFormat: "json",
		AIStopHookEvent:  "Stop",
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{
  "hooks": {
    "Notification": [{
      "hooks": [{"type":"command","command":"MSG=$(jq -r '.message // \"needs attention\"'); /usr/local/bin/notify.sh notification \"$MSG\""}]
    }],
    "Stop": [{
      "hooks": [{"type":"command","command":"/usr/local/bin/notify.sh stop"}]
    }]
  }
}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	setupNotificationHooks(cfg)

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "/usr/local/bin/notify.sh") {
		t.Fatalf("settings still contain notify hook: %s", string(data))
	}
}
