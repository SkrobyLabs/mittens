package main

import (
	"encoding/json"
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
	home := os.Getenv("MITTENS_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("/tmp", ".mittens", "runtime.json")
		}
		home = filepath.Join(userHome, ".mittens")
	}
	return filepath.Join(home, "runtime.json")
}

func loadRuntimeMetadata() (runtimeMetadata, bool) {
	path := runtimeMetadataPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return runtimeMetadata{}, false
	}
	var meta runtimeMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return runtimeMetadata{}, false
	}
	if meta.SocketPath == "" || meta.PoolToken == "" {
		return runtimeMetadata{}, false
	}
	if _, err := os.Stat(meta.SocketPath); err != nil {
		return runtimeMetadata{}, false
	}
	return meta, true
}
