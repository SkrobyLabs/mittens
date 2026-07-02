package main

import (
	"os"
	"strings"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

// mcpProxyBinary is the container-side shim persona the staged config points a
// proxied server's command at.
const mcpProxyBinary = "mittens-mcp-proxy"

// transformMCPTOML applies MCP transforms to a Codex-style config.toml via a
// targeted line edit: it rewrites command/args and strips the env subtable for
// proxied servers, and expands ${VAR} tokens for direct/mount servers. Every
// other line is left byte-identical.
func transformMCPTOML(data []byte, serversKey string, actions map[string]mcpServerAction) ([]byte, map[string][]string, error) {
	injected := map[string][]string{}
	prefix := serversKey + "."
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))

	currentName := ""
	var action mcpServerAction
	managed := false
	inEnvSub := false
	skipArgsCont := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if section := tomlStageSection(trimmed); section != "" {
			skipArgsCont = false
			currentName = ""
			managed = false
			inEnvSub = false
			if strings.HasPrefix(section, prefix) {
				name, sub := mcpconfig.SplitServerSection(strings.TrimPrefix(section, prefix))
				currentName = name
				action, managed = actions[name]
				inEnvSub = sub == "env"
				// Drop the [mcp_servers.<name>.env] header for proxied servers.
				if managed && action.rewriteProxy && inEnvSub {
					continue
				}
			}
			out = append(out, line)
			continue
		}

		if skipArgsCont {
			if strings.Contains(line, "]") {
				skipArgsCont = false
			}
			continue
		}

		if managed && action.rewriteProxy {
			if inEnvSub {
				continue // strip env lines for proxied server
			}
			key := strings.TrimSpace(firstTOMLKey(line))
			switch key {
			case "command":
				out = append(out, "command = \""+mcpProxyBinary+"\"")
			case "args":
				out = append(out, "args = [\""+currentName+"\"]")
				if !strings.Contains(line, "]") {
					skipArgsCont = true
				}
			case "env":
				// Inline env table (env = { ... }); drop it.
			default:
				out = append(out, line)
			}
			continue
		}

		if managed && action.expand && strings.Contains(line, "${") {
			expanded, inj, unresolved := expandMCPValue(line, os.LookupEnv)
			for _, v := range unresolved {
				logWarn("MCP server %q references unset variable ${%s}; leaving it unexpanded", currentName, v)
			}
			injected[currentName] = append(injected[currentName], inj...)
			out = append(out, expanded)
			continue
		}

		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n")), injected, nil
}

func tomlStageSection(line string) string {
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return ""
	}
	if strings.HasPrefix(line, "[[") || strings.HasSuffix(line, "]]") {
		return ""
	}
	return strings.TrimSpace(line[1 : len(line)-1])
}

func firstTOMLKey(line string) string {
	if idx := strings.IndexByte(line, '='); idx >= 0 {
		return line[:idx]
	}
	return ""
}
