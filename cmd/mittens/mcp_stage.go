package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

// mcpStagePlan carries the result of MCP staging: how to mount the transformed
// provider config and which proxy servers the broker should run.
type mcpStagePlan struct {
	prefsOverride string // Claude: transformed .claude.json to mount at StagingUserPrefsPath
	configBindSrc string // Codex/Gemini: transformed file (host path)
	configBindDst string // Codex/Gemini: container dst inside StagingConfigDir
	proxySpecs    []MCPProxySpec
}

// mcpServerAction describes how a single server's config entry is transformed.
type mcpServerAction struct {
	rewriteProxy bool // replace command/args with the shim, strip env
	expand       bool // expand ${VAR} in env/headers/args/command/url
}

// planMCPStaging computes proxy pin verification and the staged provider-config
// transform. It records refusals and injected env names on the App and returns
// nil (no staging) when there is nothing to transform.
func (a *App) planMCPStaging(home string) (*mcpStagePlan, error) {
	if !a.MCPAll && len(a.MCPServers) == 0 {
		return nil, nil
	}
	if a.Provider == nil || a.Provider.MCPConfigFile == "" {
		return nil, nil
	}

	hostServers := readMCPServers(a.Provider, home, a.Workspace)
	actions := map[string]mcpServerAction{}
	var proxySpecs []MCPProxySpec
	a.mcpProxyRefusals = map[string]string{}

	// Managed direct/mount servers get env expansion.
	managed := a.managedMCPModes(hostServers)
	for name, mode := range managed {
		if mode == mcpModeProxy {
			continue
		}
		actions[name] = mcpServerAction{expand: true}
	}

	// Proxy servers: verify the pin and decide approve vs refuse.
	for _, s := range a.proxyMCPServers() {
		server, ok := hostServers[s.Name]
		if !ok {
			a.mcpProxyRefusals[s.Name] = "refused: not configured"
			logWarn("MCP proxy %q is not configured; skipping", s.Name)
			continue
		}
		if server.Scope == mcpconfig.ScopeWorkspace {
			a.mcpProxyRefusals[s.Name] = "refused: workspace-only"
			logWarn("MCP proxy %q is workspace-scope only; proxy unsupported in v1", s.Name)
			continue
		}
		if s.CommandPin == "" {
			a.mcpProxyRefusals[s.Name] = "refused: unpinned"
			logWarn("MCP proxy %q has no command_pin; refusing (re-approve via wizard or `mittens policy set mcp.%s.mode proxy`)", s.Name, s.Name)
			continue
		}
		current := mcpCommandPin(server)
		if current != s.CommandPin {
			a.mcpProxyRefusals[s.Name] = "refused: command changed"
			logWarn("MCP proxy %q command changed since approval; refusing.\n  approved pin: %s\n  current pin:  %s\n  current command: %s",
				s.Name, s.CommandPin, current, mcpCommandLine(server))
			continue
		}
		actions[s.Name] = mcpServerAction{rewriteProxy: true}
		proxySpecs = append(proxySpecs, MCPProxySpec{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
			Dir:     server.Dir,
		})
	}

	if !anyStagingAction(actions) {
		// No transform needed; still return any proxy specs (none survive here
		// since rewriteProxy implies an action).
		return &mcpStagePlan{proxySpecs: proxySpecs}, nil
	}

	// Provider guard: a provider that mounts its whole config dir rw would
	// bypass staging entirely; refuse rather than ship untransformed config.
	if a.Provider.RuntimePlan().HistoryMountsWholeConfig {
		return nil, fmt.Errorf("MCP staging unsupported for providers that mount the whole config directory")
	}

	configPath := filepath.Join(home, a.Provider.MCPConfigFile)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &mcpStagePlan{proxySpecs: proxySpecs}, nil
		}
		return nil, fmt.Errorf("reading MCP config %s: %w", configPath, err)
	}

	transformed, injected, err := a.transformMCPConfig(data, actions)
	if err != nil {
		return nil, err
	}
	a.mcpInjectedEnv = injected

	tmp, err := a.writeMCPStageFile(a.Provider.MCPConfigFile, transformed)
	if err != nil {
		return nil, err
	}

	plan := &mcpStagePlan{proxySpecs: proxySpecs}
	if a.Provider.UserPrefsFile != "" && a.Provider.UserPrefsFile == a.Provider.MCPConfigFile {
		plan.prefsOverride = tmp
	} else {
		rel := strings.TrimPrefix(a.Provider.MCPConfigFile, a.Provider.ConfigDir+"/")
		plan.configBindSrc = tmp
		plan.configBindDst = filepath.Join(a.Provider.StagingConfigDir(), rel)
	}
	return plan, nil
}

// warnUnmountableDirectMCPServers warns when a direct-mode stdio server's
// command is a host-absolute path that is not mounted into the container, and
// suggests switching it to proxy or mount mode.
func (a *App) warnUnmountableDirectMCPServers(home string) {
	if len(a.MCPServers) == 0 {
		return
	}
	servers := readMCPServers(a.Provider, home, a.Workspace)
	for _, s := range a.MCPServers {
		if s.Mode != mcpModeDirect {
			continue
		}
		srv, ok := servers[s.Name]
		if !ok || !srv.IsStdio() || !filepath.IsAbs(srv.Command) {
			continue
		}
		if a.mcpPathAlreadyMounted(srv.Command) {
			continue
		}
		logWarn("MCP server %q uses host path %s in direct mode; it will not resolve in the container. Consider: mittens policy set mcp.%s.mode proxy",
			s.Name, srv.Command, s.Name)
	}
}

// managedMCPModes returns the transform mode per server name: explicit policy
// entries win; when MCPAll is set the remaining configured servers are direct.
func (a *App) managedMCPModes(hostServers map[string]mcpconfig.Server) map[string]string {
	modes := map[string]string{}
	if a.MCPAll {
		for name := range hostServers {
			modes[name] = mcpModeDirect
		}
	}
	for _, s := range a.MCPServers {
		mode := s.Mode
		if mode == "" {
			mode = mcpModeDirect
		}
		modes[s.Name] = mode
	}
	return modes
}

func anyStagingAction(actions map[string]mcpServerAction) bool {
	for _, a := range actions {
		if a.rewriteProxy || a.expand {
			return true
		}
	}
	return false
}

func (a *App) writeMCPStageFile(configFile string, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", "mittens-mcp.*")
	if err != nil {
		return "", err
	}
	a.tempDirs = append(a.tempDirs, dir)
	path := filepath.Join(dir, filepath.Base(configFile))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// transformMCPConfig dispatches to the JSON or TOML transform based on the
// provider format. Returns the transformed bytes and injected env var names
// keyed by server.
func (a *App) transformMCPConfig(data []byte, actions map[string]mcpServerAction) ([]byte, map[string][]string, error) {
	switch a.Provider.MCPConfigFormat {
	case "json":
		return transformMCPJSON(data, a.Provider.MCPServersKey, a.Workspace, actions)
	case "toml":
		return transformMCPTOML(data, a.Provider.MCPServersKey, actions)
	default:
		return data, nil, nil
	}
}

// ---------------------------------------------------------------------------
// JSON transform (Claude .claude.json, Gemini settings.json)
// ---------------------------------------------------------------------------

func transformMCPJSON(data []byte, serversKey, workspace string, actions map[string]mcpServerAction) ([]byte, map[string][]string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parsing MCP JSON: %w", err)
	}
	injected := map[string][]string{}

	if raw, ok := root[serversKey]; ok {
		out, err := transformServersObject(raw, actions, injected)
		if err != nil {
			return nil, nil, err
		}
		root[serversKey] = out
	}

	if workspace != "" {
		if projRaw, ok := root["projects"]; ok {
			var projects map[string]json.RawMessage
			if err := json.Unmarshal(projRaw, &projects); err == nil {
				if entryRaw, ok := projects[workspace]; ok {
					var entry map[string]json.RawMessage
					if err := json.Unmarshal(entryRaw, &entry); err == nil {
						if raw, ok := entry[serversKey]; ok {
							out, err := transformServersObject(raw, actions, injected)
							if err != nil {
								return nil, nil, err
							}
							entry[serversKey] = out
							if newEntry, err := json.Marshal(entry); err == nil {
								projects[workspace] = newEntry
								if newProjects, err := json.Marshal(projects); err == nil {
									root["projects"] = newProjects
								}
							}
						}
					}
				}
			}
		}
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return out, injected, nil
}

func transformServersObject(raw json.RawMessage, actions map[string]mcpServerAction, injected map[string][]string) (json.RawMessage, error) {
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil {
		return raw, nil
	}
	for name, serverRaw := range servers {
		action, ok := actions[name]
		if !ok {
			continue
		}
		out, inj, err := transformServerJSON(name, serverRaw, action)
		if err != nil {
			return nil, err
		}
		servers[name] = out
		if len(inj) > 0 {
			injected[name] = append(injected[name], inj...)
		}
	}
	return json.Marshal(servers)
}

func transformServerJSON(name string, raw json.RawMessage, action mcpServerAction) (json.RawMessage, []string, error) {
	if action.rewriteProxy {
		out, err := json.Marshal(map[string]interface{}{
			"command": mcpProxyBinary,
			"args":    []string{name},
		})
		return out, nil, err
	}
	if !action.expand {
		return raw, nil, nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, nil, nil
	}
	injected := expandJSONServerFields(name, obj)
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, err
	}
	return out, injected, nil
}

// expandJSONServerFields expands ${VAR} tokens in command, url, args, env, and
// headers in place. Returns injected env var names.
func expandJSONServerFields(name string, obj map[string]interface{}) []string {
	var injected []string
	expand := func(s string) string {
		out, inj, unresolved := expandMCPValue(s, os.LookupEnv)
		injected = append(injected, inj...)
		for _, v := range unresolved {
			logWarn("MCP server %q references unset variable ${%s}; leaving it unexpanded", name, v)
		}
		return out
	}
	for _, key := range []string{"command", "url"} {
		if v, ok := obj[key].(string); ok {
			obj[key] = expand(v)
		}
	}
	if args, ok := obj["args"].([]interface{}); ok {
		for i, a := range args {
			if s, ok := a.(string); ok {
				args[i] = expand(s)
			}
		}
	}
	for _, key := range []string{"env", "headers"} {
		if m, ok := obj[key].(map[string]interface{}); ok {
			for k, v := range m {
				if s, ok := v.(string); ok {
					m[k] = expand(s)
				}
			}
		}
	}
	return injected
}
