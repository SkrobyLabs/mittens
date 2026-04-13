package main

import (
	"fmt"
	"log"
	"os"

	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

// config holds all configuration for the container entrypoint.
type config struct {
	ConfigMount  string
	AIUsername   string
	AIHome       string
	AIConfigDir  string
	AIDir        string
	AICredFile   string
	AIPrefsFile  string
	AISettingsFile  string
	AIProjectFile   string
	AITrustedDirsKey  string
	AIYoloKey         string
	AIMCPServersKey   string
	AITrustedDirsFile string
	AIInitSettingsJQ  string
	AIStopHookEvent   string
	AIPersistFiles    []string
	AISettingsFormat  string
	AIConfigSubdirs   []string
	AIPluginDir       string
	AIPluginFiles     []string
	AIBinary          string

	FirewallConf string

	// Flags
	DinD               bool
	DockerHost         bool
	Firewall           bool
	Verbose            bool
	Yolo               bool
	NoNotify           bool
	EnableX11Clipboard bool
	WSLClipboard       bool
	Shell              bool
	PrintMode          bool

	// Broker
	BrokerPort  string
	BrokerSock  string
	BrokerToken string

	// Misc
	ContainerName   string
	InstanceName    string
	HostWorkspace   string
	ExtraDirs       []string
	FirewallExtra   []string
	ImagePasteKey   string
	MCP             string
	CredStagingDirs  []string // "staging_path:target_dir" entries
	ExtensionPrompts []initcfg.ExtensionPrompt

	// X11 clipboard
	X11ClipboardImage      string
	X11ClipboardMaxAgeSecs int
}

func loadConfig() *config {
	path := os.Getenv("MITTENS_CONFIG")
	if path == "" {
		path = initcfg.ConfigPath
	}

	jcfg, err := initcfg.Load(path)
	if err != nil {
		log.Fatalf("[mittens-init] failed to load config from %s: %v", path, err)
	}

	username := envOr("AI_USERNAME", "claude")
	home := "/home/" + username

	return &config{
		ConfigMount:  "/mnt/mittens-staging",
		AIUsername:    username,
		AIHome:       home,
		AIConfigDir:  jcfg.AI.ConfigDir,
		AIDir:        home + "/" + jcfg.AI.ConfigDir,
		AICredFile:   jcfg.AI.CredFile,
		AIPrefsFile:  jcfg.AI.PrefsFile,
		AISettingsFile:  jcfg.AI.SettingsFile,
		AIProjectFile:   jcfg.AI.ProjectFile,
		AITrustedDirsKey:  jcfg.AI.TrustedDirsKey,
		AIYoloKey:         jcfg.AI.YoloKey,
		AIMCPServersKey:   jcfg.AI.MCPServersKey,
		AITrustedDirsFile: jcfg.AI.TrustedDirsFile,
		AIInitSettingsJQ:  jcfg.AI.InitSettingsJQ,
		AIStopHookEvent:   jcfg.AI.StopHookEvent,
		AIPersistFiles:    jcfg.AI.PersistFiles,
		AISettingsFormat:  jcfg.AI.SettingsFormat,
		AIConfigSubdirs:   jcfg.AI.ConfigSubdirs,
		AIPluginDir:       jcfg.AI.PluginDir,
		AIPluginFiles:     jcfg.AI.PluginFiles,
		AIBinary:          jcfg.AI.Binary,

		FirewallConf: "/mnt/mittens-staging/firewall.conf",

		DinD:               jcfg.Flags.DinD,
		DockerHost:         jcfg.Flags.DockerHost,
		Firewall:           jcfg.Flags.Firewall,
		Verbose:            jcfg.Flags.Verbose,
		Yolo:               jcfg.Flags.Yolo,
		NoNotify:           jcfg.Flags.NoNotify,
		EnableX11Clipboard: jcfg.Flags.EnableX11Clipboard,
		WSLClipboard:       jcfg.Flags.WSLClipboard,
		Shell:              jcfg.Flags.Shell,
		PrintMode:          jcfg.Flags.PrintMode,

		BrokerPort:  fmt.Sprintf("%d", jcfg.Broker.Port),
		BrokerSock:  jcfg.Broker.Sock,
		BrokerToken: jcfg.Broker.Token,

		ContainerName:   jcfg.ContainerName,
		InstanceName:    jcfg.InstanceName,
		HostWorkspace:   jcfg.HostWorkspace,
		ExtraDirs:       jcfg.ExtraDirs,
		FirewallExtra:   jcfg.FirewallExtra,
		ImagePasteKey:   jcfg.ImagePasteKey,
		MCP:             jcfg.MCP,
		CredStagingDirs:  jcfg.CredStagingDirs,
		ExtensionPrompts: jcfg.ExtensionPrompts,

		X11ClipboardImage:      jcfg.X11ClipboardImage,
		X11ClipboardMaxAgeSecs: jcfg.X11ClipboardMaxAgeSecs,
	}
}

func (c *config) hasBroker() bool {
	return (c.BrokerPort != "" && c.BrokerPort != "0") || c.BrokerSock != ""
}

func (c *config) settingsFilePath() string {
	return c.AIDir + "/" + c.AISettingsFile
}

func (c *config) stagingConfigDir() string {
	return c.ConfigMount + "/" + c.AIConfigDir
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
