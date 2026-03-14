// Package mcp implements the MCP server domain resolver for mittens.
// It discovers MCP server names from domain mapping files and Claude
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

	"github.com/SkrobyLabs/mittens/extensions/registry"
)

func init() {
	registry.Register("mcp", &registry.Registration{
		List:  listServers,
		Setup: setup,
	})
}

// listServers returns a sorted, deduplicated list of all known MCP server
// names gathered from:
//  1. The built-in mcp-domains.conf (/etc/mittens/mcp-domains.conf)
//  2. User override at ~/.claude/mcp-domains.conf
//  3. mcpServers keys from ~/.claude.json
//  4. Server names from {workspace}/.mcp.json (if it exists)
func listServers() ([]string, error) {
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
		// 2. User override mcp-domains.conf.
		userConf := filepath.Join(home, ".claude", "mcp-domains.conf")
		if names, err := readMCPDomainNames(userConf); err == nil {
			add(names)
		}

		// 3. mcpServers keys from ~/.claude.json.
		claudeJSON := filepath.Join(home, ".claude.json")
		if names, err := readClaudeJSONMCPServers(claudeJSON); err == nil {
			add(names)
		}
	}

	// 4. Workspace .mcp.json (cwd-based).
	cwd, _ := os.Getwd()
	if cwd != "" {
		mcpJSON := filepath.Join(cwd, ".mcp.json")
		if names, err := readMCPJSONServers(mcpJSON); err == nil {
			add(names)
		}
	}

	sort.Strings(servers)
	return servers, nil
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

// readClaudeJSONMCPServers reads ~/.claude.json and returns the keys of
// the top-level mcpServers object.
func readClaudeJSONMCPServers(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	serversRaw, ok := raw["mcpServers"]
	if !ok {
		return nil, nil
	}

	var serversMap map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &serversMap); err != nil {
		return nil, err
	}

	var names []string
	for name := range serversMap {
		names = append(names, name)
	}
	return names, nil
}

// readMCPJSONServers reads a .mcp.json file (workspace-level MCP config)
// and returns the keys of the top-level mcpServers object.
func readMCPJSONServers(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	serversRaw, ok := raw["mcpServers"]
	if !ok {
		return nil, nil
	}

	var serversMap map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &serversMap); err != nil {
		return nil, err
	}

	var names []string
	for name := range serversMap {
		names = append(names, name)
	}
	return names, nil
}
