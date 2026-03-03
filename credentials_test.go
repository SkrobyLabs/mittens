package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// expiresAt
// ---------------------------------------------------------------------------

func TestExpiresAt(t *testing.T) {
	tests := []struct {
		name string
		json string
		want int64
	}{
		{
			name: "valid integer expiresAt",
			json: `{"expiresAt": 1700000000}`,
			want: 1700000000,
		},
		{
			name: "valid float expiresAt",
			json: `{"expiresAt": 1700000000.123}`,
			want: 1700000000,
		},
		{
			name: "missing field",
			json: `{"token": "abc"}`,
			want: 0,
		},
		{
			name: "invalid JSON",
			json: `not json`,
			want: 0,
		},
		{
			name: "empty string",
			json: "",
			want: 0,
		},
		{
			name: "expiresAt is string not number",
			json: `{"expiresAt": "not-a-number"}`,
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := expiresAt(tc.json)
			if got != tc.want {
				t.Errorf("expiresAt(%q) = %d, want %d", tc.json, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// freshestCredential
// ---------------------------------------------------------------------------

func TestFreshestCredential(t *testing.T) {
	tests := []struct {
		name      string
		sources   []credentialSource
		wantJSON  string
		wantLabel string
	}{
		{
			name: "picks highest expiresAt",
			sources: []credentialSource{
				{json: `{"expiresAt": 100}`, label: "old"},
				{json: `{"expiresAt": 300}`, label: "newest"},
				{json: `{"expiresAt": 200}`, label: "mid"},
			},
			wantJSON:  `{"expiresAt": 300}`,
			wantLabel: "newest",
		},
		{
			name: "single source",
			sources: []credentialSource{
				{json: `{"expiresAt": 42}`, label: "only"},
			},
			wantJSON:  `{"expiresAt": 42}`,
			wantLabel: "only",
		},
		{
			name:      "empty list",
			sources:   nil,
			wantJSON:  "",
			wantLabel: "",
		},
		{
			name: "same expiry picks first",
			sources: []credentialSource{
				{json: `{"expiresAt": 100}`, label: "first"},
				{json: `{"expiresAt": 100}`, label: "second"},
			},
			wantJSON:  `{"expiresAt": 100}`,
			wantLabel: "first",
		},
		{
			name: "invalid JSON treated as 0 expiry",
			sources: []credentialSource{
				{json: `not json`, label: "bad"},
				{json: `{"expiresAt": 1}`, label: "good"},
			},
			wantJSON:  `{"expiresAt": 1}`,
			wantLabel: "good",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotJSON, gotLabel := freshestCredential(tc.sources)
			if gotJSON != tc.wantJSON {
				t.Errorf("json = %q, want %q", gotJSON, tc.wantJSON)
			}
			if gotLabel != tc.wantLabel {
				t.Errorf("label = %q, want %q", gotLabel, tc.wantLabel)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FileCredentialStore — round-trip Extract/Persist
// ---------------------------------------------------------------------------

func TestFileCredentialStore_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".claude", ".credentials.json")

	store := &FileCredentialStore{path: path}

	// Extract from missing file returns empty, no error.
	data, err := store.Extract()
	if err != nil {
		t.Fatal(err)
	}
	if data != "" {
		t.Errorf("expected empty for missing file, got %q", data)
	}

	// Persist creates directories and writes.
	creds := `{"accessToken":"tok","expiresAt":1700000000}`
	if err := store.Persist(creds); err != nil {
		t.Fatal(err)
	}

	// Extract reads it back.
	data, err = store.Extract()
	if err != nil {
		t.Fatal(err)
	}
	if data != creds {
		t.Errorf("Extract() = %q, want %q", data, creds)
	}

	// Verify file permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %v, want 0600", info.Mode().Perm())
	}
}

func TestFileCredentialStore_Label(t *testing.T) {
	store := &FileCredentialStore{path: "/home/user/.claude/.credentials.json"}
	if store.Label() != "/home/user/.claude/.credentials.json" {
		t.Errorf("Label() = %q", store.Label())
	}
}

// ---------------------------------------------------------------------------
// CredentialManager.Setup — stages freshest credential to temp file
// ---------------------------------------------------------------------------

func TestCredentialManager_Setup(t *testing.T) {
	tmp := t.TempDir()

	// Create two file stores with different expiry times.
	oldPath := filepath.Join(tmp, "old.json")
	os.WriteFile(oldPath, []byte(`{"accessToken":"old","expiresAt":100}`), 0644)

	newPath := filepath.Join(tmp, "new.json")
	os.WriteFile(newPath, []byte(`{"accessToken":"new","expiresAt":999}`), 0644)

	mgr := &CredentialManager{
		stores: []CredentialStore{
			&FileCredentialStore{path: oldPath},
			&FileCredentialStore{path: newPath},
		},
	}

	if err := mgr.Setup(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Cleanup()

	// TmpFile should be set.
	if mgr.TmpFile() == "" {
		t.Fatal("TmpFile() is empty after Setup")
	}

	// Temp file should contain the freshest credentials.
	data, err := os.ReadFile(mgr.TmpFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"accessToken":"new"`) {
		t.Errorf("temp file should have newest creds, got %q", string(data))
	}

	// Temp file should have restricted permissions.
	info, err := os.Stat(mgr.TmpFile())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("temp file permissions = %v, want 0600", info.Mode().Perm())
	}
}

func TestCredentialManager_Setup_NoCredentials(t *testing.T) {
	tmp := t.TempDir()

	mgr := &CredentialManager{
		stores: []CredentialStore{
			&FileCredentialStore{path: filepath.Join(tmp, "nonexistent.json")},
		},
	}

	if err := mgr.Setup(); err != nil {
		t.Fatal(err)
	}

	if mgr.TmpFile() != "" {
		t.Errorf("TmpFile() should be empty when no credentials, got %q", mgr.TmpFile())
	}
}

func TestCredentialManager_Cleanup(t *testing.T) {
	tmp := t.TempDir()
	credPath := filepath.Join(tmp, "creds.json")
	os.WriteFile(credPath, []byte(`{"expiresAt":100}`), 0644)

	mgr := &CredentialManager{
		stores: []CredentialStore{
			&FileCredentialStore{path: credPath},
		},
	}

	if err := mgr.Setup(); err != nil {
		t.Fatal(err)
	}

	tmpFile := mgr.TmpFile()
	if tmpFile == "" {
		t.Fatal("expected temp file")
	}

	// Verify temp file exists.
	if _, err := os.Stat(tmpFile); err != nil {
		t.Fatalf("temp file should exist: %v", err)
	}

	mgr.Cleanup()

	// Temp file should be removed.
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("temp file should be removed after Cleanup")
	}
	if mgr.TmpFile() != "" {
		t.Error("TmpFile() should be empty after Cleanup")
	}
}

func TestCredentialManager_PersistAll(t *testing.T) {
	tmp := t.TempDir()

	pathA := filepath.Join(tmp, "a.json")
	pathB := filepath.Join(tmp, "b.json")

	mgr := &CredentialManager{
		stores: []CredentialStore{
			&FileCredentialStore{path: pathA},
			&FileCredentialStore{path: pathB},
		},
	}

	creds := `{"accessToken":"refreshed","expiresAt":9999}`
	mgr.PersistAll(creds)

	// Both stores should have the credentials.
	for _, path := range []string{pathA, pathB} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if string(data) != creds {
			t.Errorf("%s = %q, want %q", path, data, creds)
		}
	}
}

func TestCredentialManager_Stores(t *testing.T) {
	stores := []CredentialStore{
		&FileCredentialStore{path: "/a"},
		&FileCredentialStore{path: "/b"},
	}
	mgr := &CredentialManager{stores: stores}
	got := mgr.Stores()
	if len(got) != 2 {
		t.Fatalf("Stores() returned %d stores, want 2", len(got))
	}
	if got[0].Label() != "/a" || got[1].Label() != "/b" {
		t.Errorf("Stores() returned wrong stores: %v, %v", got[0].Label(), got[1].Label())
	}
}

func TestCredentialManager_Setup_PicksFreshest(t *testing.T) {
	tmp := t.TempDir()

	// Store A: expired. Store B: valid. Store C: freshest.
	pathA := filepath.Join(tmp, "a.json")
	os.WriteFile(pathA, []byte(`{"expiresAt":10}`), 0644)

	pathB := filepath.Join(tmp, "b.json")
	os.WriteFile(pathB, []byte(`{"expiresAt":50}`), 0644)

	pathC := filepath.Join(tmp, "c.json")
	os.WriteFile(pathC, []byte(`{"accessToken":"winner","expiresAt":100}`), 0644)

	mgr := &CredentialManager{
		stores: []CredentialStore{
			&FileCredentialStore{path: pathA},
			&FileCredentialStore{path: pathB},
			&FileCredentialStore{path: pathC},
		},
	}
	if err := mgr.Setup(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Cleanup()

	data, _ := os.ReadFile(mgr.TmpFile())
	if !strings.Contains(string(data), `"accessToken":"winner"`) {
		t.Errorf("should pick store C (highest expiry), got %q", data)
	}
}
