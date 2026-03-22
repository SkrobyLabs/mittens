// Package gcp implements the GCP credential resolver for mittens.
// It lists gcloud configurations and stages filtered credentials into
// the container.
package gcp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

func init() {
	registry.Register("gcp", &registry.Registration{
		List:  listConfigs,
		Setup: setup,
	})
}

// listConfigs returns available gcloud configuration names by scanning
// ~/.config/gcloud/configurations/config_* files and stripping the prefix.
func listConfigs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configsDir := filepath.Join(home, ".config", "gcloud", "configurations")

	entries, err := os.ReadDir(configsDir)
	if err != nil {
		return nil, fmt.Errorf("reading gcloud configurations: %w", err)
	}

	var configs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "config_") {
			configs = append(configs, strings.TrimPrefix(name, "config_"))
		}
	}
	sort.Strings(configs)
	return configs, nil
}

// setup mounts GCP credentials into the container. In AllMode the entire
// ~/.config/gcloud directory is mounted read-only. Otherwise only selected
// configurations and supporting credential files are staged.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension
	home := ctx.Home
	gcloudDir := filepath.Join(home, ".config", "gcloud")

	// --gcp-all: mount entire directory
	if ext.AllMode {
		if info, err := os.Stat(gcloudDir); err == nil && info.IsDir() {
			*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", gcloudDir+":/home/claude/.config/gcloud:ro")
		} else {
			fmt.Fprintf(os.Stderr, "[mittens] warning: GCP credentials requested but %s does not exist\n", gcloudDir)
		}
		return nil
	}

	// No configs selected: nothing to do.
	if len(ext.Args) == 0 {
		return nil
	}

	configsDir := filepath.Join(gcloudDir, "configurations")
	staging := ctx.StagingDir

	// Create configurations subdirectory in staging.
	stagingConfigsDir := filepath.Join(staging, "configurations")
	if err := os.MkdirAll(stagingConfigsDir, 0755); err != nil {
		return fmt.Errorf("creating staging configurations dir: %w", err)
	}

	// Copy only the requested configuration files.
	for _, cfg := range ext.Args {
		src := filepath.Join(configsDir, "config_"+cfg)
		dst := filepath.Join(stagingConfigsDir, "config_"+cfg)
		if err := fileutil.CopyFile(src, dst); err != nil {
			fmt.Fprintf(os.Stderr, "[mittens] warning: GCP config '%s' not found: %v\n", cfg, err)
		}
	}

	// Write active_config with the first selected configuration name.
	activeConfig := filepath.Join(staging, "active_config")
	if err := os.WriteFile(activeConfig, []byte(ext.Args[0]+"\n"), 0644); err != nil {
		return fmt.Errorf("writing active_config: %w", err)
	}

	// Copy credential databases wholesale (SQLite, cannot filter per-config).
	for _, dbFile := range []string{"credentials.db", "access_tokens.db"} {
		src := filepath.Join(gcloudDir, dbFile)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(staging, dbFile)
			if err := fileutil.CopyFile(src, dst); err == nil {
				_ = os.Chmod(dst, 0600)
			}
		}
	}

	// Copy application_default_credentials.json if present.
	adcFile := filepath.Join(gcloudDir, "application_default_credentials.json")
	if _, err := os.Stat(adcFile); err == nil {
		dst := filepath.Join(staging, "application_default_credentials.json")
		if err := fileutil.CopyFile(adcFile, dst); err == nil {
			_ = os.Chmod(dst, 0600)
		}
	}

	// Copy legacy_credentials directory if present.
	legacyDir := filepath.Join(gcloudDir, "legacy_credentials")
	if info, err := os.Stat(legacyDir); err == nil && info.IsDir() {
		dst := filepath.Join(staging, "legacy_credentials")
		if err := fileutil.CopyDir(legacyDir, dst); err != nil {
			fmt.Fprintf(os.Stderr, "[mittens] warning: failed to copy legacy_credentials: %v\n", err)
		}
	}

	// Mount the staging directory as gcloud config.
	*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", staging+":/home/claude/.config/gcloud:ro")
	return nil
}
