package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const serveMetadataFileName = "serve.json"

type serveMetadata struct {
	URL       string    `json:"url"`
	Token     string    `json:"token,omitempty"`
	PID       int       `json:"pid"`
	RepoPath  string    `json:"repoPath"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func serveMetadataPath(project ProjectPaths) string {
	return filepath.Join(project.RootDir, serveMetadataFileName)
}

func writeServeMetadata(project ProjectPaths, repoPath, url, token string) error {
	meta := serveMetadata{
		URL:       strings.TrimSpace(url),
		Token:     strings.TrimSpace(token),
		PID:       os.Getpid(),
		RepoPath:  strings.TrimSpace(repoPath),
		UpdatedAt: time.Now().UTC(),
	}
	if meta.URL == "" {
		return fmt.Errorf("serve metadata url must not be empty")
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encode serve metadata: %w", err)
	}
	path := serveMetadataPath(project)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write serve metadata temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install serve metadata: %w", err)
	}
	return nil
}

func removeServeMetadata(project ProjectPaths) error {
	err := os.Remove(serveMetadataPath(project))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func loadServeMetadata(project ProjectPaths) (serveMetadata, bool) {
	path := serveMetadataPath(project)
	data, err := os.ReadFile(path)
	if err != nil {
		return serveMetadata{}, false
	}
	var meta serveMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		_ = os.Remove(path)
		return serveMetadata{}, false
	}
	if strings.TrimSpace(meta.URL) == "" || meta.PID <= 0 {
		_ = os.Remove(path)
		return serveMetadata{}, false
	}
	if !processAlive(meta.PID) {
		_ = os.Remove(path)
		return serveMetadata{}, false
	}
	return meta, true
}

func detectKitchenServer(repoPath string) (serveMetadata, bool, error) {
	if baseURL := strings.TrimSpace(os.Getenv("KITCHEN_API_URL")); baseURL != "" {
		return serveMetadata{
			URL:       baseURL,
			Token:     strings.TrimSpace(os.Getenv("KITCHEN_API_TOKEN")),
			PID:       0,
			RepoPath:  strings.TrimSpace(repoPath),
			UpdatedAt: time.Now().UTC(),
		}, true, nil
	}
	paths, err := DefaultKitchenPaths()
	if err != nil {
		return serveMetadata{}, false, err
	}
	repoRoot, err := resolveRepoRoot(repoPath)
	if err != nil {
		return serveMetadata{}, false, err
	}
	project, err := paths.Project(repoRoot)
	if err != nil {
		return serveMetadata{}, false, err
	}
	meta, ok := loadServeMetadata(project)
	if !ok {
		return serveMetadata{}, false, nil
	}
	if meta.RepoPath != "" && meta.RepoPath != repoRoot {
		return serveMetadata{}, false, nil
	}
	return meta, true, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
