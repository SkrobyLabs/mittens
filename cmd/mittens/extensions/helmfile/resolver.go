package helmfile

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func init() {
	registry.Register("helmfile", &registry.Registration{
		Setup: setup,
	})
}

// setup scans the workspace for helmfile configuration files and extracts
// chart repository hostnames so the firewall whitelists them automatically.
// Mirrors helm/resolver.go but reads from the project root rather than $HOME.
func setup(ctx *registry.SetupContext) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	pattern := `(?m)^\s*url:\s*(https?://\S+)`
	candidates := helmfileCandidates(cwd)

	seen := make(map[string]bool)
	var hosts []string
	for _, path := range candidates {
		for _, h := range registry.ExtractUniqueHosts(path, pattern) {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}

	if len(hosts) > 0 {
		*ctx.FirewallExtra = append(*ctx.FirewallExtra, hosts...)
		registry.LogInfo("helmfile: added %d chart repo domain(s) to firewall", len(hosts))
	}
	return nil
}

// helmfileCandidates returns paths to helmfile config files mittens should
// scan for chart repository URLs. Covers the conventional layout:
//
//   - helmfile.yaml / helmfile.yaml.gotmpl
//   - helmfile-*.yaml / helmfile-*.yaml.gotmpl at the repo root
//   - helmfile.d/*.yaml{,.gotmpl}
//   - helmfiles/*.yaml{,.gotmpl} (Quix-style)
//
// Missing files are silently skipped by ExtractUniqueHosts.
func helmfileCandidates(root string) []string {
	var paths []string

	for _, name := range []string{"helmfile.yaml", "helmfile.yaml.gotmpl"} {
		paths = append(paths, filepath.Join(root, name))
	}

	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if !strings.HasPrefix(n, "helmfile-") {
				continue
			}
			if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yaml.gotmpl") {
				paths = append(paths, filepath.Join(root, n))
			}
		}
	}

	for _, dir := range []string{"helmfile.d", "helmfiles"} {
		full := filepath.Join(root, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yaml.gotmpl") {
				paths = append(paths, filepath.Join(full, n))
			}
		}
	}

	return paths
}
