// Package azure implements the Azure credential resolver for mittens.
// It registers itself with the registry at init time.
package azure

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

func init() {
	registry.Register("azure", &registry.Registration{
		List:  listSubscriptions,
		Setup: setup,
	})
}

// ---------------------------------------------------------------------------
// List resolver
// ---------------------------------------------------------------------------

// listSubscriptions parses ~/.azure/azureProfile.json and returns subscription
// names sorted alphabetically, with the default subscription first (prefixed
// with "*").
func listSubscriptions() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	profilePath := filepath.Join(home, ".azure", "azureProfile.json")

	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, fmt.Errorf("reading azureProfile.json: %w", err)
	}
	data = stripBOM(data)

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing azureProfile.json: %w", err)
	}

	subsRaw, ok := raw["subscriptions"]
	if !ok {
		return nil, nil
	}

	subs, ok := subsRaw.([]interface{})
	if !ok {
		return nil, nil
	}

	var defaultName string
	var names []string
	for _, s := range subs {
		sub, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := sub["name"].(string)
		if name == "" {
			continue
		}
		isDefault, _ := sub["isDefault"].(bool)
		if isDefault {
			defaultName = name
		} else {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	// Default subscription goes first, prefixed with *
	var result []string
	if defaultName != "" {
		result = append(result, "*"+defaultName)
	}
	result = append(result, names...)
	return result, nil
}

// ---------------------------------------------------------------------------
// Setup resolver
// ---------------------------------------------------------------------------

// setup mounts Azure credentials into the container, optionally filtered to
// only the requested subscriptions.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension
	home := ctx.Home
	azureDir := filepath.Join(home, ".azure")

	// --azure-all: mount entire directory
	if ext.AllMode {
		if info, err := os.Stat(azureDir); err == nil && info.IsDir() {
			*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", azureDir+":/home/claude/.azure:ro")
			fmt.Fprintf(os.Stderr, "[mittens] Mounting Azure credentials (all subscriptions)\n")
		} else {
			fmt.Fprintf(os.Stderr, "[mittens] WARN: Azure credentials requested but %s does not exist\n", azureDir)
		}
		return nil
	}

	// No subscriptions selected: nothing to do
	if len(ext.Args) == 0 {
		return nil
	}

	// Create staging directory
	staging := ctx.StagingDir
	if staging == "" {
		tmpDir, err := os.MkdirTemp("", "mittens-azure.*")
		if err != nil {
			return fmt.Errorf("creating Azure temp dir: %w", err)
		}
		staging = tmpDir
		*ctx.TempDirs = append(*ctx.TempDirs, tmpDir)
	}

	// Filter azureProfile.json
	profilePath := filepath.Join(azureDir, "azureProfile.json")
	if err := filterAzureProfile(profilePath, filepath.Join(staging, "azureProfile.json"), ext.Args); err != nil {
		return fmt.Errorf("filtering Azure profile: %w", err)
	}

	// Copy supporting files if they exist
	supportFiles := []string{
		"msal_token_cache.json",
		"msal_token_cache.bin",
		"service_principal_entries.json",
		"az.json",
		"clouds.config",
		"az.sess",
	}
	for _, name := range supportFiles {
		src := filepath.Join(azureDir, name)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(staging, name)
			if err := fileutil.CopyFile(src, dst); err == nil {
				os.Chmod(dst, 0600)
			}
		}
	}

	*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", staging+":/home/claude/.azure:ro")
	fmt.Fprintf(os.Stderr, "[mittens] Azure subscriptions: %s\n", strings.Join(ext.Args, ", "))
	return nil
}

// ---------------------------------------------------------------------------
// Azure profile filtering
// ---------------------------------------------------------------------------

// filterAzureProfile reads an Azure profile JSON, keeps only the named
// subscriptions, sets the first as default, and writes the result to destPath.
// Uses map[string]interface{} to preserve all fields without needing to define
// every field in a struct.
func filterAzureProfile(srcPath, destPath string, wantedNames []string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	data = stripBOM(data)

	// Parse into generic map to preserve all fields
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing azureProfile.json: %w", err)
	}

	subsRaw, ok := raw["subscriptions"]
	if !ok {
		// No subscriptions field; write as-is
		return os.WriteFile(destPath, data, 0600)
	}

	subs, ok := subsRaw.([]interface{})
	if !ok {
		return fmt.Errorf("subscriptions field is not an array")
	}

	// Build wanted set
	wanted := make(map[string]bool, len(wantedNames))
	for _, n := range wantedNames {
		wanted[n] = true
	}

	// Filter subscriptions
	var filtered []interface{}
	for _, s := range subs {
		sub, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := sub["name"].(string)
		if wanted[name] {
			sub["isDefault"] = false
			filtered = append(filtered, sub)
		}
	}

	// Set first as default with state Enabled
	if len(filtered) > 0 {
		if first, ok := filtered[0].(map[string]interface{}); ok {
			first["isDefault"] = true
			first["state"] = "Enabled"
		}
	}

	raw["subscriptions"] = filtered

	// Re-serialize with indentation for readability
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("serializing filtered profile: %w", err)
	}

	return os.WriteFile(destPath, append(out, '\n'), 0600)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stripBOM removes a UTF-8 BOM prefix if present. Azure CLI writes UTF-8 BOM
// to azureProfile.json on some platforms.
func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}
