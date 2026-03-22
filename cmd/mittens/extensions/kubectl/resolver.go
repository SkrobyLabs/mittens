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
func listContexts() ([]string, error) {
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
	return contexts, nil
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

	// Extract each context into a separate temp file.
	var tmpFiles []string
	for _, ctxName := range ext.Args {
		out, err := exec.Command("kubectl", "config", "view", "--minify", "--flatten", "--context="+ctxName).Output()
		if err != nil {
			return fmt.Errorf("extracting kubectl context '%s': %w", ctxName, err)
		}
		tmpFile := filepath.Join(staging, ctxName+".yaml")
		if err := os.WriteFile(tmpFile, out, 0600); err != nil {
			return fmt.Errorf("writing kubectl context '%s': %w", ctxName, err)
		}
		tmpFiles = append(tmpFiles, tmpFile)
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

	// Set current-context to the first selected context.
	useCtxCmd := exec.Command("kubectl", "config", "use-context", ext.Args[0])
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
