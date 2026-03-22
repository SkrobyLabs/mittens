package helm

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func init() {
	registry.Register("helm", &registry.Registration{
		Setup: setup,
	})
}

func setup(ctx *registry.SetupContext) error {
	reposFile := filepath.Join(ctx.Home, ".config", "helm", "repositories.yaml")
	hosts := registry.ExtractUniqueHosts(reposFile, `(?m)^\s*-?\s*url:\s*(https?://\S+)`)
	if len(hosts) > 0 {
		*ctx.FirewallExtra = append(*ctx.FirewallExtra, hosts...)
		fmt.Fprintf(os.Stderr, "[mittens] helm: added %d custom repo domain(s) to firewall\n", len(hosts))
	}
	return nil
}
