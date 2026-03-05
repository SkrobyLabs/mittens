package helm

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Skroby/mittens/extensions/registry"
)

func init() {
	registry.Register("helm", &registry.Registration{
		Setup: setup,
	})
}

func setup(ctx *registry.SetupContext) error {
	reposFile := filepath.Join(ctx.Home, ".config", "helm", "repositories.yaml")
	hosts := extractRepoHosts(reposFile)
	if len(hosts) > 0 {
		*ctx.FirewallExtra = append(*ctx.FirewallExtra, hosts...)
		fmt.Fprintf(os.Stderr, "[mittens] helm: added %d custom repo domain(s) to firewall\n", len(hosts))
	}
	return nil
}

// extractRepoHosts reads a Helm repositories.yaml and returns unique hostnames
// from all repository URL entries.
func extractRepoHosts(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	re := regexp.MustCompile(`(?m)^\s*-?\s*url:\s*(https?://\S+)`)
	matches := re.FindAllStringSubmatch(string(data), -1)

	seen := make(map[string]bool)
	var hosts []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		u, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		host := u.Hostname()
		if host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	return hosts
}
