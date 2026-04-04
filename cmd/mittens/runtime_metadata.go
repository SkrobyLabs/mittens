package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type runtimeMetadata struct {
	SocketPath  string    `json:"socketPath"`
	BrokerToken string    `json:"brokerToken"`
	PoolToken   string    `json:"poolToken"`
	SessionID   string    `json:"sessionId"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func runtimeMetadataPath() string {
	return filepath.Join(ConfigHome(), "runtime.json")
}

func writeRuntimeMetadata(meta runtimeMetadata) error {
	if meta.SocketPath == "" || meta.PoolToken == "" {
		return fmt.Errorf("runtime metadata requires socketPath and poolToken")
	}
	meta.UpdatedAt = time.Now().UTC()

	path := runtimeMetadataPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create runtime metadata dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime metadata: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write runtime metadata temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename runtime metadata: %w", err)
	}
	return nil
}

func clearRuntimeMetadata() error {
	err := os.Remove(runtimeMetadataPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
