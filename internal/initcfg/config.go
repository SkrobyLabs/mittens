// Package initcfg defines the shared configuration struct passed from the
// mittens host binary to the container entrypoint (mittens-init) via a
// mounted JSON file.
package initcfg

import (
	"encoding/json"
	"os"
)

// ContainerConfig holds all settings the host passes to the container entrypoint.
// The host marshals this to JSON, writes it to a temp file, and bind-mounts it
// read-only into the container. The entrypoint unmarshals it at startup.
type ContainerConfig struct {
	// AI provider settings — binary name, config paths, settings keys.
	AI AIConfig `json:"ai"`

	// Feature flags toggled by CLI flags or extension resolvers.
	Flags Flags `json:"flags"`

	// Broker connection details (TCP or Unix socket).
	Broker BrokerConfig `json:"broker"`

	// Runtime context.
	ContainerName string   `json:"containerName"`
	InstanceName  string   `json:"instanceName,omitempty"`
	HostWorkspace string   `json:"hostWorkspace,omitempty"`
	ExtraDirs     []string `json:"extraDirs,omitempty"`
	FirewallExtra []string `json:"firewallExtra,omitempty"`
	ImagePasteKey string   `json:"imagePasteKey,omitempty"`
	MCP           string   `json:"mcp,omitempty"`

	// X11 clipboard (macOS clipboard bridge, Codex).
	X11ClipboardImage      string `json:"x11ClipboardImage,omitempty"`
	X11ClipboardMaxAgeSecs int    `json:"x11ClipboardMaxAgeSecs,omitempty"`
}

// AIConfig describes the AI CLI binary, its config directory layout, and
// settings keys used by mittens-init to stage configuration files.
type AIConfig struct {
	Binary         string   `json:"binary"`
	ConfigDir      string   `json:"configDir"`
	CredFile       string   `json:"credFile"`
	PrefsFile      string   `json:"prefsFile,omitempty"`
	SettingsFile   string   `json:"settingsFile"`
	ProjectFile    string   `json:"projectFile"`
	TrustedDirsKey string   `json:"trustedDirsKey,omitempty"`
	YoloKey        string   `json:"yoloKey,omitempty"`
	MCPServersKey  string   `json:"mcpServersKey,omitempty"`
	TrustedDirsFile string  `json:"trustedDirsFile,omitempty"`
	InitSettingsJQ string   `json:"initSettingsJQ,omitempty"`
	StopHookEvent  string   `json:"stopHookEvent,omitempty"`
	PersistFiles   []string `json:"persistFiles,omitempty"`
	SettingsFormat string   `json:"settingsFormat"`
	ConfigSubdirs  []string `json:"configSubdirs"`
	PluginDir      string   `json:"pluginDir,omitempty"`
	PluginFiles    []string `json:"pluginFiles,omitempty"`
}

// Flags holds boolean feature toggles.
type Flags struct {
	DinD               bool `json:"dind,omitempty"`
	DockerHost         bool `json:"dockerHost,omitempty"`
	Firewall           bool `json:"firewall,omitempty"`
	Verbose            bool `json:"verbose,omitempty"`
	Yolo               bool `json:"yolo,omitempty"`
	NoNotify           bool `json:"noNotify,omitempty"`
	EnableX11Clipboard bool `json:"enableX11Clipboard,omitempty"`
	WSLClipboard       bool `json:"wslClipboard,omitempty"`
	Shell              bool `json:"shell,omitempty"`
	PrintMode          bool `json:"printMode,omitempty"`
}

// BrokerConfig holds connection details for the host credential broker.
type BrokerConfig struct {
	Port  int    `json:"port,omitempty"`
	Sock  string `json:"sock,omitempty"`
	Token string `json:"token,omitempty"`
}

// ConfigPath is the well-known mount path inside the container.
const ConfigPath = "/mnt/mittens-config.json"

// Load reads and unmarshals a ContainerConfig from the given JSON file.
func Load(path string) (*ContainerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ContainerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Write marshals the config to JSON and writes it to the given path.
// Uses 0644 so the file is readable after container privilege drop
// (the host user's UID differs from the container AI user's UID).
func (c *ContainerConfig) Write(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
