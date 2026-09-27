// Package kubectl implements the Kubernetes config resolver for mittens.
// It lists available kubectl contexts, extracts and merges selected contexts
// into a single kubeconfig, and adds API server hostnames to the firewall.
package kubectl

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func init() {
	registry.Register("kubectl", &registry.Registration{
		List:  listContexts,
		Setup: setup,
	})
}

// listContexts runs `kubectl config get-contexts -o name` and returns
// the context names sorted alphabetically.
func listContexts() ([]registry.ListItem, error) {
	out, err := exec.Command("kubectl", "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl config get-contexts: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}

	var contexts []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			contexts = append(contexts, line)
		}
	}
	sort.Strings(contexts)
	var items []registry.ListItem
	for _, c := range contexts {
		items = append(items, registry.ListItem{Label: c, Value: c})
	}
	return items, nil
}

// setup creates a merged, filtered kubeconfig containing only the requested
// contexts, extracts API server hostnames for firewall rules, and mounts the
// result into the container.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension

	// No contexts selected: nothing to do.
	if len(ext.Args) == 0 {
		return nil
	}

	staging := ctx.StagingDir
	available, err := listContexts()
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(available))
	for _, item := range available {
		known[item.Value] = true
	}

	// Extract each context into a separate temp file.
	var tmpFiles []string
	var selected []string
	for _, ctxName := range ext.Args {
		if !known[ctxName] {
			registry.LogWarn("kubectl context %q is not available; skipping", ctxName)
			continue
		}
		out, err := exec.Command("kubectl", "config", "view", "--minify", "--flatten", "--context="+ctxName).Output()
		if err != nil {
			return fmt.Errorf("extracting kubectl context '%s': %w", ctxName, err)
		}
		tmpFile := filepath.Join(staging, ctxName+".yaml")
		if err := os.WriteFile(tmpFile, out, 0600); err != nil {
			return fmt.Errorf("writing kubectl context '%s': %w", ctxName, err)
		}
		tmpFiles = append(tmpFiles, tmpFile)
		selected = append(selected, ctxName)
	}
	// Never merge an empty selection: kubectl would read the host's default config.
	if len(tmpFiles) == 0 {
		return nil
	}

	// Merge all extracted configs into a single kubeconfig by setting
	// KUBECONFIG to all temp files and flattening.
	mergedKubeconfig := strings.Join(tmpFiles, ":")
	mergeCmd := exec.Command("kubectl", "config", "view", "--flatten")
	mergeCmd.Env = append(os.Environ(), "KUBECONFIG="+mergedKubeconfig)
	mergedOut, err := mergeCmd.Output()
	if err != nil {
		return fmt.Errorf("merging kubeconfigs: %w", err)
	}

	configPath := filepath.Join(staging, "config")
	if err := os.WriteFile(configPath, mergedOut, 0600); err != nil {
		return fmt.Errorf("writing merged kubeconfig: %w", err)
	}

	// Set current-context to the first available selected context.
	useCtxCmd := exec.Command("kubectl", "config", "use-context", selected[0])
	useCtxCmd.Env = append(os.Environ(), "KUBECONFIG="+configPath)
	_ = useCtxCmd.Run() // best-effort

	// Extract API server hostnames for firewall rules.
	hosts := registry.ExtractUniqueHosts(configPath, `server:\s*(https?://[^\s]+)`)
	for _, h := range hosts {
		*ctx.FirewallExtra = append(*ctx.FirewallExtra, h)
	}

	// Mount the merged config file.
	*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", configPath+":"+ctx.ContainerHome+"/.kube/config:ro")
	return nil
}
