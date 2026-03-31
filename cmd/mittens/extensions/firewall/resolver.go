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

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
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

// EmbeddedConf can be set by the main package to provide the embedded
// firewall.conf content. When the on-disk file cannot be found, the
// resolver extracts this to a temp file so it can be bind-mounted.
var EmbeddedConf []byte

// DevMode is set to true when --firewall-dev is passed. It switches the
// firewall from the strict whitelist to the developer-friendly superset.
var DevMode bool

// EmbeddedDevConf can be set by the main package to provide the embedded
// firewall-dev.conf content (developer-friendly whitelist).
var EmbeddedDevConf []byte

// listDomains reads the firewall.conf file and returns a sorted list of
// whitelisted domain names (one per line, comments stripped).
func listDomains() ([]registry.ListItem, error) {
	var domains []string
	var err error

	// In DevMode, use the developer-friendly conf if available.
	if DevMode && len(EmbeddedDevConf) > 0 {
		domains, err = parseFirewallDomains(string(EmbeddedDevConf))
	} else {
		path := resolveConfPath("")

		// Fall back to parsing the embedded content directly (no temp file
		// needed here since we only need the domain list, not a mount path).
		if path == "" || !fileExists(path) {
			if len(EmbeddedConf) > 0 {
				domains, err = parseFirewallDomains(string(EmbeddedConf))
			} else {
				return nil, fmt.Errorf("firewall.conf not found")
			}
		} else {
			domains, err = readFirewallDomains(path)
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(domains)
	var items []registry.ListItem
	for _, d := range domains {
		items = append(items, registry.ListItem{Label: d, Value: d})
	}
	return items, nil
}

// ResolveConfFile determines the host-side path to a firewall.conf file
// suitable for bind-mounting into a container. It respects DevMode,
// custom paths (from --firewall), DefaultConfPath, and embedded fallbacks.
//
// The returned path may be a temp file (from embedded extraction) — callers
// that need cleanup must track it themselves.
func ResolveConfFile(customPath string) (string, error) {
	// In DevMode, use the developer-friendly conf directly from the
	// embedded content (ignore any custom path).
	if DevMode && len(EmbeddedDevConf) > 0 {
		tmp, err := extractEmbedded(EmbeddedDevConf, "mittens-firewall-dev-*.conf")
		if err != nil {
			return "", fmt.Errorf("extracting embedded firewall-dev.conf: %w", err)
		}
		return tmp, nil
	}

	confPath := resolveConfPath(customPath)

	// If the resolved path doesn't exist on disk, try extracting the
	// embedded default to a temp file. This covers the "make install"
	// case where the binary lives in /usr/local/bin without the
	// container/ directory alongside it.
	if confPath == "" || !fileExists(confPath) {
		if len(EmbeddedConf) > 0 {
			tmp, err := extractEmbedded(EmbeddedConf, "mittens-firewall-*.conf")
			if err != nil {
				return "", fmt.Errorf("extracting embedded firewall.conf: %w", err)
			}
			return tmp, nil
		} else if confPath == "" {
			return "", fmt.Errorf("firewall.conf not found (set DefaultConfPath or use --firewall /path/to/file)")
		} else {
			return "", fmt.Errorf("firewall config not found: %s", confPath)
		}
	}

	return confPath, nil
}

// setup mounts the firewall configuration file into the container and sets
// the MITTENS_FIREWALL environment variable so the entrypoint knows to
// activate the network firewall (proxy + iptables).
//
// If the user provided a custom file via --firewall /path/to/file, that
// path is in ctx.Extension.RawArg and takes precedence over the default.
func setup(ctx *registry.SetupContext) error {
	confPath, err := ResolveConfFile(ctx.Extension.RawArg)
	if err != nil {
		return err
	}
	return mountFirewall(ctx, confPath)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseFirewallDomains(string(data))
}

// parseFirewallDomains extracts domain names from firewall.conf content.
func parseFirewallDomains(content string) ([]string, error) {
	var domains []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
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

// fileExists reports whether path exists on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// mountFirewall adds docker args to mount a firewall config file and enable
// the firewall in the container entrypoint.
func mountFirewall(ctx *registry.SetupContext, confPath string) error {
	*ctx.DockerArgs = append(*ctx.DockerArgs,
		"-v", confPath+":/mnt/mittens-staging/firewall.conf:ro",
		"-e", "MITTENS_FIREWALL=true",
	)
	return nil
}

// extractEmbedded writes the given content to a temp file with the given
// name pattern and returns its path. The file is made world-readable so
// that the container root process can read it even when DAC_OVERRIDE is
// dropped (--cap-drop ALL).
func extractEmbedded(content []byte, pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := os.Chmod(f.Name(), 0644); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
