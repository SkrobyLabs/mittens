package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDetectKitchenServerLoadsProjectMetadata(t *testing.T) {
	k := newTestKitchen(t)
	t.Setenv("KITCHEN_HOME", k.paths.HomeDir)

	if err := writeServeMetadata(k.project, k.repoPath, "http://127.0.0.1:7681", "secret"); err != nil {
		t.Fatalf("writeServeMetadata: %v", err)
	}

	meta, ok, err := detectKitchenServer(k.repoPath)
	if err != nil {
		t.Fatalf("detectKitchenServer: %v", err)
	}
	if !ok {
		t.Fatal("expected detected kitchen server")
	}
	if meta.URL != "http://127.0.0.1:7681" || meta.Token != "secret" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestDetectKitchenServerRemovesStaleMetadata(t *testing.T) {
	k := newTestKitchen(t)
	t.Setenv("KITCHEN_HOME", k.paths.HomeDir)

	path := serveMetadataPath(k.project)
	data, err := json.MarshalIndent(serveMetadata{
		URL:      "http://127.0.0.1:7681",
		PID:      0,
		RepoPath: k.repoPath,
	}, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, ok, err := detectKitchenServer(k.repoPath)
	if err != nil {
		t.Fatalf("detectKitchenServer: %v", err)
	}
	if ok {
		t.Fatal("expected stale metadata to be ignored")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("serve metadata still exists, stat err=%v", err)
	}
}
