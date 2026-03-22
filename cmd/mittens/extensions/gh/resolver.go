package gh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/fileutil"
	"gopkg.in/yaml.v3"
)

func init() {
	registry.Register("gh", &registry.Registration{
		Setup: setup,
	})
}

// setup copies the gh config directory to a staging area and enriches
// hosts.yml with OAuth tokens extracted from the host's credential store.
//
// gh 2.x stores tokens in the system keyring (macOS Keychain, etc.) rather
// than in hosts.yml. Inside a Docker container there is no keyring, so we
// extract the tokens on the host via `gh auth token` and embed them in
// the hosts.yml that gets mounted into the container.
func setup(ctx *registry.SetupContext) error {
	ghDir := filepath.Join(ctx.Home, ".config", "gh")
	if _, err := os.Stat(ghDir); err != nil {
		return nil // no gh config on host, nothing to do
	}

	staging := ctx.StagingDir

	// Copy entire gh config directory to staging.
	if err := fileutil.CopyDir(ghDir, staging); err != nil {
		// Fall back to direct read-only mount (original behavior).
		*ctx.DockerArgs = append(*ctx.DockerArgs,
			"-v", ghDir+":"+ctx.ContainerHome+"/.config/gh:ro")
		return nil
	}

	// Try to enrich hosts.yml with tokens from the host keyring.
	hostsPath := filepath.Join(staging, "hosts.yml")
	if err := enrichHostsWithTokens(hostsPath); err != nil {
		fmt.Fprintf(os.Stderr, "[mittens] gh: token extraction: %v (gh may not authenticate inside container)\n", err)
	}

	*ctx.DockerArgs = append(*ctx.DockerArgs,
		"-v", staging+":"+ctx.ContainerHome+"/.config/gh:ro")
	return nil
}

// enrichHostsWithTokens reads hosts.yml, extracts tokens from the host's
// credential store via `gh auth token`, and writes them back into the file
// so they're available without a keyring.
func enrichHostsWithTokens(hostsPath string) error {
	data, err := os.ReadFile(hostsPath)
	if err != nil {
		return fmt.Errorf("reading hosts.yml: %w", err)
	}

	// Parse as map[hostname] -> map[key] -> value.
	var hosts map[string]interface{}
	if err := yaml.Unmarshal(data, &hosts); err != nil {
		return fmt.Errorf("parsing hosts.yml: %w", err)
	}

	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh not found on host PATH")
	}

	enriched := false
	for host, rawConfig := range hosts {
		config, ok := rawConfig.(map[string]interface{})
		if !ok {
			continue
		}

		activeUser, _ := config["user"].(string)

		// Collect usernames from the "users" map.
		rawUsers, ok := config["users"].(map[string]interface{})
		if !ok {
			continue
		}

		for username := range rawUsers {
			token, err := extractGHToken(host, username)
			if err != nil || token == "" {
				continue
			}

			// Ensure the user entry is a map (may be nil for empty YAML values).
			userMap, _ := rawUsers[username].(map[string]interface{})
			if userMap == nil {
				userMap = make(map[string]interface{})
			}
			userMap["oauth_token"] = token
			rawUsers[username] = userMap
			enriched = true

			// Set the top-level oauth_token for the active user.
			if username == activeUser {
				config["oauth_token"] = token
			}
		}
	}

	if !enriched {
		return fmt.Errorf("no tokens could be extracted")
	}

	out, err := yaml.Marshal(hosts)
	if err != nil {
		return fmt.Errorf("marshaling hosts.yml: %w", err)
	}
	return os.WriteFile(hostsPath, out, 0600)
}

// extractGHToken runs `gh auth token` on the host to get the OAuth token
// for a specific user on a specific host. This works because the mittens
// binary runs on the host where the keyring is accessible.
func extractGHToken(host, username string) (string, error) {
	cmd := exec.Command("gh", "auth", "token", "-h", host, "-u", username)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
