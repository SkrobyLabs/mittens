// Package firewall implements the network firewall resolver for mittens.
// The firewall extension is unique: it is enabled by default (DefaultOn: true
// in the YAML manifest). The --no-firewall flag disables it entirely, in
// which case setup is never called. The --firewall flag accepts a custom
// whitelist file path.
package firewall

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Skroby/mittens/extensions/registry"
)

func init() {
	registry.Register("firewall", &registry.Registration{
		List:  listDomains,
		Setup: setup,
	})
}

// DefaultConfPath can be set by the main package to point to the bundled
// firewall.conf that ships alongside the binary. If empty, the list
// resolver will try the well-known install location as a fallback.
var DefaultConfPath string

// listDomains reads the firewall.conf file and returns a sorted list of
// whitelisted domain names (one per line, comments stripped).
func listDomains() ([]string, error) {
	path := resolveConfPath("")
	if path == "" {
		return nil, fmt.Errorf("firewall.conf not found")
	}
	domains, err := readFirewallDomains(path)
	if err != nil {
		return nil, err
	}
	sort.Strings(domains)
	return domains, nil
}

// setup mounts the firewall configuration file into the container and sets
// the MITTENS_FIREWALL environment variable so the entrypoint knows to
// activate iptables + squid.
//
// If the user provided a custom file via --firewall /path/to/file, that
// path is in ctx.Extension.RawArg and takes precedence over the default.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension

	confPath := resolveConfPath(ext.RawArg)
	if confPath == "" {
		return fmt.Errorf("firewall.conf not found (set DefaultConfPath or use --firewall /path/to/file)")
	}

	// Validate the config file exists.
	if _, err := os.Stat(confPath); err != nil {
		return fmt.Errorf("firewall config not found: %s", confPath)
	}

	// Mount the config file read-only into the container.
	*ctx.DockerArgs = append(*ctx.DockerArgs,
		"-v", confPath+":/mnt/claude-config/firewall.conf:ro",
	)

	// Tell the entrypoint to enable the firewall.
	*ctx.DockerArgs = append(*ctx.DockerArgs, "-e", "MITTENS_FIREWALL=true")

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resolveConfPath determines which firewall.conf file to use.
// Priority order:
//  1. customPath (from --firewall flag, i.e. RawArg) if non-empty
//  2. DefaultConfPath (set by main package)
//  3. /etc/mittens/firewall.conf (well-known install location)
func resolveConfPath(customPath string) string {
	if customPath != "" {
		return customPath
	}
	if DefaultConfPath != "" {
		return DefaultConfPath
	}
	// Fallback to well-known install location.
	const fallback = "/etc/mittens/firewall.conf"
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	return ""
}

// readFirewallDomains reads a firewall.conf file and returns non-empty,
// non-comment lines as domain names. Inline comments (# to end of line)
// are stripped and each line is trimmed of surrounding whitespace.
func readFirewallDomains(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var domains []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Strip inline comments.
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		domains = append(domains, line)
	}
	return domains, scanner.Err()
}
