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
// items with name as label and subscription ID as value, sorted alphabetically
// with the default subscription first.
func listSubscriptions() ([]registry.ListItem, error) {
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

	type subInfo struct {
		name      string
		id        string
		isDefault bool
	}
	var defaultSub *subInfo
	var others []subInfo
	for _, s := range subs {
		sub, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := sub["name"].(string)
		id, _ := sub["id"].(string)
		if id == "" {
			continue
		}
		info := subInfo{name: name, id: id}
		isDefault, _ := sub["isDefault"].(bool)
		if isDefault {
			info.isDefault = true
			defaultSub = &info
		} else {
			others = append(others, info)
		}
	}

	sort.Slice(others, func(i, j int) bool { return others[i].name < others[j].name })

	// Default subscription goes first, marked with *.
	var result []registry.ListItem
	if defaultSub != nil {
		result = append(result, registry.ListItem{
			Label: "*" + defaultSub.name,
			Value: defaultSub.id,
		})
	}
	for _, s := range others {
		result = append(result, registry.ListItem{
			Label: s.name,
			Value: s.id,
		})
	}
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

	credStagingPath := "/mnt/mittens-creds-azure"

	// --azure-all: stage the entire directory (copy, not direct mount) so
	// we can inject the plaintext MSAL cache extracted from the macOS Keychain.
	if ext.AllMode {
		if info, err := os.Stat(azureDir); err == nil && info.IsDir() {
			staging := ctx.StagingDir
			if staging == "" {
				tmpDir, err := os.MkdirTemp("", "mittens-azure.*")
				if err != nil {
					return fmt.Errorf("creating Azure temp dir: %w", err)
				}
				staging = tmpDir
				*ctx.TempDirs = append(*ctx.TempDirs, tmpDir)
			}
			if err := fileutil.CopyDir(azureDir, staging); err != nil {
				return fmt.Errorf("copying Azure dir: %w", err)
			}
			extractMSALCache(staging, nil)
			*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", staging+":"+credStagingPath+":ro")
			*ctx.CredStagingDirs = append(*ctx.CredStagingDirs, credStagingPath+":.azure")
			registry.LogInfo("Mounting Azure credentials (all subscriptions)")
		} else {
			registry.LogWarn("Azure credentials requested but %s does not exist", azureDir)
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

	// On macOS, MSAL tokens live in the Keychain, not in the file. Extract
	// the plaintext cache so the container's az CLI can use it.
	extractMSALCache(staging, ext.Args)

	*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", staging+":"+credStagingPath+":ro")
	*ctx.CredStagingDirs = append(*ctx.CredStagingDirs, credStagingPath+":.azure")

	// Log with names for readability.
	names := subscriptionNames(profilePath, ext.Args)
	registry.LogInfo("Azure subscriptions: %s", strings.Join(names, ", "))
	return nil
}

// ---------------------------------------------------------------------------
// Azure profile filtering
// ---------------------------------------------------------------------------

// filterAzureProfile reads an Azure profile JSON, keeps only the subscriptions
// matching the given IDs, sets the first as default, and writes the result to
// destPath. Uses map[string]interface{} to preserve all fields without needing
// to define every field in a struct.
func filterAzureProfile(srcPath, destPath string, wantedIDs []string) error {
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

	// Build wanted set (by subscription ID)
	wanted := make(map[string]bool, len(wantedIDs))
	for _, id := range wantedIDs {
		wanted[id] = true
	}

	// Filter subscriptions by ID
	var filtered []interface{}
	for _, s := range subs {
		sub, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := sub["id"].(string)
		if wanted[id] {
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

// subscriptionNames maps subscription IDs to "name (id)" for log output.
// Falls back to the bare ID if the profile can't be read.
func subscriptionNames(profilePath string, ids []string) []string {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return ids
	}
	data = stripBOM(data)
	var raw map[string]interface{}
	if json.Unmarshal(data, &raw) != nil {
		return ids
	}
	subs, _ := raw["subscriptions"].([]interface{})
	nameByID := make(map[string]string)
	for _, s := range subs {
		sub, _ := s.(map[string]interface{})
		id, _ := sub["id"].(string)
		name, _ := sub["name"].(string)
		if id != "" && name != "" {
			nameByID[id] = name
		}
	}
	result := make([]string, len(ids))
	for i, id := range ids {
		if name, ok := nameByID[id]; ok {
			result[i] = name + " (" + id + ")"
		} else {
			result[i] = id
		}
	}
	return result
}

// stripBOM removes a UTF-8 BOM prefix if present. Azure CLI writes UTF-8 BOM
// to azureProfile.json on some platforms.
func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}
