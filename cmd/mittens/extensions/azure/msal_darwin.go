//go:build darwin

package azure

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

// extractMSALCache populates the staging directory with a usable MSAL token
// cache. On macOS, Azure CLI stores MSAL tokens in the Keychain. This extracts
// them into a plaintext file the container's az CLI can use.
//
// If the Keychain extraction yields no tokens (e.g. broker-based auth), falls
// back to running az account get-access-token on the host to force token
// acquisition into the staging dir.
func extractMSALCache(staging string, subscriptionIDs []string) {
	registry.LogInfo("Extracting Azure tokens from macOS Keychain (you may be prompted)...")

	out, err := exec.Command("security", "find-generic-password",
		"-s", "Microsoft.Developer.IdentityService",
		"-a", "MSALCache", "-w").Output()
	if err != nil {
		registry.LogWarn("Keychain extraction failed, falling back to az CLI: %v", err)
		forceTokenRefresh(staging, subscriptionIDs)
		return
	}

	data := strings.TrimSpace(string(out))
	if len(data) < 10 {
		forceTokenRefresh(staging, subscriptionIDs)
		return
	}

	dst := filepath.Join(staging, "msal_token_cache.json")
	os.WriteFile(dst, []byte(data), 0600)

	// Check if the extracted cache has actual tokens (not just account refs).
	if !strings.Contains(data, `"AccessToken"`) || !strings.Contains(data, `"secret"`) {
		forceTokenRefresh(staging, subscriptionIDs)
	}
}

// forceTokenRefresh runs az account get-access-token on the host for each
// subscription, writing real tokens into the staging dir's MSAL cache.
func forceTokenRefresh(staging string, subscriptionIDs []string) {
	for _, subID := range subscriptionIDs {
		cmd := exec.Command("az", "account", "get-access-token",
			"--subscription", subID, "--output", "none")
		cmd.Env = append(os.Environ(), "AZURE_CONFIG_DIR="+staging)
		cmd.Run() // best-effort; ignore errors
	}
}
