// Package mcp implements the MCP server domain resolver for mittens.
// It discovers MCP server names from domain mapping files and provider
// configuration, then passes the selected servers to the container
// entrypoint via an environment variable for firewall whitelisting.
package mcp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func init() {
	registry.Register("mcp", &registry.Registration{
		List:  listServers,
		Setup: setup,
	})
}

// listServers returns a sorted, deduplicated list of all known MCP server
// names gathered from documented provider config locations:
//  1. The built-in mcp-domains.conf (/etc/mittens/mcp-domains.conf)
//  2. Provider domain overrides, e.g. ~/.claude/mcp-domains.conf
//  3. Provider MCP config files, e.g. ~/.claude.json, ~/.gemini/settings.json, ~/.codex/config.toml
//  4. Server names from {workspace}/.mcp.json, if it exists
func listServers() ([]registry.ListItem, error) {
	seen := make(map[string]bool)
	var servers []string

	add := func(names []string) {
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				servers = append(servers, n)
			}
		}
	}

	// 1. Built-in mcp-domains.conf.
	builtinConf := "/etc/mittens/mcp-domains.conf"
	if names, err := readMCPDomainNames(builtinConf); err == nil {
		add(names)
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		// 2. Provider-specific user override mcp-domains.conf.
		for _, dir := range []string{".claude", ".gemini", ".codex"} {
			userConf := filepath.Join(home, dir, "mcp-domains.conf")
			if names, err := readMCPDomainNames(userConf); err == nil {
				add(names)
			}
		}

		// 3. Provider MCP config files.
		cwd, _ := os.Getwd()
		for _, cfg := range hostMCPConfigs(home, cwd) {
			if names, err := readMCPConfigServerNames(cfg); err == nil {
				add(names)
			}
		}
	}

	// 4. Workspace .mcp.json (cwd-based).
	cwd, _ := os.Getwd()
	if cwd != "" {
		mcpJSON := mcpConfig{Path: filepath.Join(cwd, ".mcp.json"), Format: "json", Key: "mcpServers"}
		if names, err := readMCPConfigServerNames(mcpJSON); err == nil {
			add(names)
		}
	}

	sort.Strings(servers)
	var items []registry.ListItem
	for _, s := range servers {
		items = append(items, registry.ListItem{Label: s, Value: s})
	}
	return items, nil
}

// setup sets the MITTENS_MCP environment variable so the container
// entrypoint can resolve domains and configure firewall rules at runtime.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension

	// AllMode: pass __all__ sentinel so entrypoint whitelists everything.
	if ext.AllMode {
		*ctx.DockerArgs = append(*ctx.DockerArgs, "-e", "MITTENS_MCP=__all__")
		return nil
	}

	// Build the comma-separated list of selected server names.
	if len(ext.Args) == 0 && ext.RawArg == "" {
		return nil
	}

	mcpVal := ext.RawArg
	if mcpVal == "" {
		mcpVal = strings.Join(ext.Args, ",")
	}

	*ctx.DockerArgs = append(*ctx.DockerArgs, "-e", "MITTENS_MCP="+mcpVal)
	return nil
}

// ---------------------------------------------------------------------------
// File parsers
// ---------------------------------------------------------------------------

type mcpConfig struct {
	Path        string
	Format      string
	Key         string
	ProjectPath string
}

func hostMCPConfigs(home, projectPath string) []mcpConfig {
	return []mcpConfig{
		{Path: filepath.Join(home, ".claude.json"), Format: "json", Key: "mcpServers", ProjectPath: projectPath},
		{Path: filepath.Join(home, ".gemini", "settings.json"), Format: "json", Key: "mcpServers"},
		{Path: filepath.Join(home, ".codex", "config.toml"), Format: "toml", Key: "mcp_servers"},
	}
}

// readMCPDomainNames reads a mcp-domains.conf file and returns the server
// names (left-hand side of each name=domain1,domain2 line).
func readMCPDomainNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) >= 1 {
			name := strings.TrimSpace(parts[0])
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names, scanner.Err()
}

// readMCPServerKeys reads a JSON file and returns the keys of its MCP server object.
func readMCPServerKeys(path string) ([]string, error) {
	return readMCPConfigServerNames(mcpConfig{Path: path, Format: "json", Key: "mcpServers"})
}

func readMCPConfigServerNames(cfg mcpConfig) ([]string, error) {
	switch cfg.Format {
	case "json":
		return readMCPJSONServerKeys(cfg.Path, cfg.Key, cfg.ProjectPath)
	case "toml":
		return readMCPTOMLServerKeys(cfg.Path, cfg.Key)
	default:
		return nil, nil
	}
}

func readMCPJSONServerKeys(path, key, projectPath string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var names []string
	if serversRaw, ok := raw[key]; ok {
		serverNames, err := serverNamesFromRawJSON(serversRaw)
		if err != nil {
			return nil, err
		}
		names = append(names, serverNames...)
	}

	if projectPath != "" {
		projectsRaw, ok := raw["projects"]
		if ok {
			var projects map[string]json.RawMessage
			if err := json.Unmarshal(projectsRaw, &projects); err == nil {
				if projectRaw, ok := projects[projectPath]; ok {
					var project map[string]json.RawMessage
					if err := json.Unmarshal(projectRaw, &project); err == nil {
						if serversRaw, ok := project[key]; ok {
							serverNames, err := serverNamesFromRawJSON(serversRaw)
							if err != nil {
								return nil, err
							}
							names = append(names, serverNames...)
						}
					}
				}
			}
		}
	}

	return names, nil
}

func serverNamesFromRawJSON(raw json.RawMessage) ([]string, error) {
	var serversMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &serversMap); err != nil {
		return nil, err
	}
	var names []string
	for name := range serversMap {
		names = append(names, name)
	}
	return names, nil
}

func readMCPTOMLServerKeys(path, table string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	prefix := table + "."
	for _, line := range strings.Split(string(data), "\n") {
		section := tomlSectionName(line)
		if section == "" || !strings.HasPrefix(section, prefix) {
			continue
		}
		name := strings.TrimPrefix(section, prefix)
		if idx := strings.IndexByte(name, '.'); idx >= 0 {
			name = name[:idx]
		}
		name = unquoteTOMLKey(name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func tomlSectionName(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return ""
	}
	if strings.HasPrefix(line, "[[") || strings.HasSuffix(line, "]]") {
		return ""
	}
	return strings.TrimSpace(line[1 : len(line)-1])
}

func unquoteTOMLKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) >= 2 {
		if (key[0] == '"' && key[len(key)-1] == '"') || (key[0] == '\'' && key[len(key)-1] == '\'') {
			return key[1 : len(key)-1]
		}
	}
	return key
}
