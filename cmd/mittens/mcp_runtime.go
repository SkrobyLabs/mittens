package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

type mcpHelperMount struct {
	Path   string
	Access string
	Server string
}

// readMCPServers returns all MCP servers configured for the provider, merging
// user-scope provider config with the workspace .mcp.json (workspace wins).
func readMCPServers(provider *Provider, home, workspace string) map[string]mcpconfig.Server {
	var servers map[string]mcpconfig.Server
	if provider != nil && provider.MCPConfigFile != "" {
		servers = mcpconfig.ReadProvider(
			filepath.Join(home, provider.MCPConfigFile),
			provider.MCPConfigFormat,
			provider.MCPServersKey,
			workspace,
		)
	}
	return mcpconfig.Merge(servers, mcpconfig.ReadWorkspace(workspace))
}

func (a *App) planMCPHelperMounts(home string) []mcpHelperMount {
	names := a.mountModeMCPServerNames()
	if len(names) == 0 {
		return nil
	}
	serversByName := readMCPServers(a.Provider, home, a.Workspace)

	var mounts []mcpHelperMount
	seenMounts := map[string]struct{}{}
	for _, name := range names {
		server, ok := serversByName[name]
		if !ok {
			logWarn("MCP server %q is not configured for %s; skipping helper mounts", name, a.Provider.DisplayName)
			continue
		}
		if !server.IsStdio() {
			continue
		}
		for _, localPath := range mcpMountCandidates(server) {
			mountPath, ok := mcpHelperMountPath(localPath, home)
			if !ok {
				continue
			}
			if a.mcpPathAlreadyMounted(localPath) {
				continue
			}
			if mcpMountRefused(mountPath, home) {
				logWarn("MCP server %q references path %s; not auto-mounting", name, mountPath)
				continue
			}
			if _, seen := seenMounts[mountPath]; seen {
				continue
			}
			seenMounts[mountPath] = struct{}{}
			mounts = append(mounts, mcpHelperMount{Path: mountPath, Access: "ro", Server: name})
		}
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Path < mounts[j].Path })
	return mounts
}

func (a *App) mcpExtension() *registry.Extension {
	for _, ext := range a.Extensions {
		if ext != nil && ext.Name == "mcp" {
			return ext
		}
	}
	return nil
}

// mountModeMCPServerNames returns the names of servers selected for mount mode.
func (a *App) mountModeMCPServerNames() []string {
	var names []string
	for _, s := range a.MCPServers {
		if s.Mode == mcpModeMount {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

// proxyMCPServers returns the policy entries selected for proxy mode.
func (a *App) proxyMCPServers() []MCPServerPolicy {
	var out []MCPServerPolicy
	for _, s := range a.MCPServers {
		if s.Mode == mcpModeProxy {
			out = append(out, s)
		}
	}
	return out
}

func (a *App) hasProxyMCPServers() bool {
	return len(a.proxyMCPServers()) > 0
}

// nonProxyMCPServerNames returns the explicitly selected direct/mount server
// names (proxy servers excluded — their traffic bypasses the firewall).
func (a *App) nonProxyMCPServerNames() []string {
	var names []string
	for _, s := range a.MCPServers {
		if s.Mode != mcpModeProxy {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

// configureMCPExtension seeds the mcp firewall extension from the MCP policy so
// the existing MITTENS_MCP resolver only whitelists non-proxy servers. When
// "all" is combined with any proxy server the __all__ sentinel is expanded
// host-side into an explicit non-proxy name list, since the container-side
// __all__ path would otherwise re-whitelist proxied servers.
func (a *App) configureMCPExtension() {
	ext := a.mcpExtension()
	if ext == nil {
		return
	}
	if a.MCPAll {
		if !a.hasProxyMCPServers() {
			ext.Enabled = true
			ext.AllMode = true
			ext.Args = nil
			ext.RawArg = ""
			return
		}
		proxy := map[string]struct{}{}
		for _, s := range a.proxyMCPServers() {
			proxy[s.Name] = struct{}{}
		}
		servers := readMCPServers(a.Provider, os.Getenv("HOME"), a.Workspace)
		var names []string
		for name := range servers {
			if _, isProxy := proxy[name]; !isProxy {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		a.setMCPExtensionNames(ext, names)
		return
	}
	a.setMCPExtensionNames(ext, a.nonProxyMCPServerNames())
}

func (a *App) setMCPExtensionNames(ext *registry.Extension, names []string) {
	ext.AllMode = false
	if len(names) == 0 {
		ext.Enabled = false
		ext.Args = nil
		ext.RawArg = ""
		return
	}
	ext.Enabled = true
	ext.Args = append([]string(nil), names...)
	ext.RawArg = strings.Join(names, ",")
}

// mcpMountCandidates returns the local filesystem paths that are plausibly the
// server's own entry point: the command itself, plus args that either carry a
// script extension or are the first positional (non-flag, not a flag's value)
// argument. Flag values (e.g. "--log-file /x.log") and later data-path args are
// deliberately excluded so mounting stays scoped to helper code.
func mcpMountCandidates(s mcpconfig.Server) []string {
	var paths []string
	if isLocalMCPPathToken(s.Command) {
		paths = append(paths, filepath.Clean(s.Command))
	}
	prevWasFlag := false
	seenPositional := false
	for _, arg := range s.Args {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(trimmed, "-") {
			prevWasFlag = true
			continue
		}
		isPositional := !prevWasFlag
		prevWasFlag = false
		candidate := hasScriptExtension(arg) || (isPositional && !seenPositional)
		if isPositional {
			seenPositional = true
		}
		if candidate && isLocalMCPPathToken(arg) {
			paths = append(paths, filepath.Clean(arg))
		}
	}
	return paths
}

func hasScriptExtension(token string) bool {
	switch strings.ToLower(filepath.Ext(strings.Trim(strings.TrimSpace(token), `"'`))) {
	case ".js", ".mjs", ".cjs", ".py", ".sh", ".rb":
		return true
	}
	return false
}

func isLocalMCPPathToken(token string) bool {
	token = strings.Trim(strings.TrimSpace(token), `"'`)
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	return filepath.IsAbs(token)
}

// mcpHelperMountPath resolves a local path to a mountable directory. Regular
// files with an exec bit or a script extension are promoted to their git repo
// root (so relative imports work); explicit directories are mounted as-is.
// Anything else is refused.
func mcpHelperMountPath(localPath, home string) (string, bool) {
	info, err := os.Stat(localPath)
	if err != nil {
		logWarn("MCP helper path does not exist: %s", localPath)
		return "", false
	}
	if info.IsDir() {
		return filepath.Clean(localPath), true
	}
	if !info.Mode().IsRegular() {
		return "", false
	}
	execable := info.Mode().Perm()&0o111 != 0
	if !execable && !hasScriptExtension(localPath) {
		logWarn("MCP helper path is not executable and has no script extension: %s", localPath)
		return "", false
	}
	if gitRoot, err := exec.Command("git", "-C", filepath.Dir(localPath), "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(gitRoot))
		if root != "" && !mcpMountRefused(root, home) {
			logInfo("MCP helper mount promoted to repo root (mounts whole repo read-only): %s", root)
			return filepath.Clean(root), true
		}
	}
	return filepath.Clean(filepath.Dir(localPath)), true
}

func (a *App) mcpPathAlreadyMounted(path string) bool {
	for _, mounted := range a.mcpMountedPrefixes() {
		if pathWithin(path, mounted) {
			return true
		}
	}
	return false
}

func (a *App) mcpMountedPrefixes() []string {
	var out []string
	if a.WorkspaceMountSrc != "" && a.WorkspaceMountSrc == a.Workspace {
		out = append(out, a.WorkspaceMountSrc)
	}
	if !a.Worktree {
		for _, dirSpec := range a.ExtraDirs {
			spec := parseExtraDirSpec(dirSpec)
			if abs, err := filepath.Abs(spec.Path); err == nil {
				out = append(out, abs)
			}
		}
	}
	return out
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

// containerFilesystemRoots are container paths that must never be shadowed by a
// host identity mount (host /etc over container /etc breaks phase-1 user setup).
var containerFilesystemRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/opt", "/var", "/root"}

// mcpMountRefused reports whether a mount at the given path must be refused:
// either it collides with a container filesystem root, or it lands under a
// sensitive credential/state directory.
func mcpMountRefused(path, home string) bool {
	clean := filepath.Clean(path)
	for _, root := range containerFilesystemRoots {
		if clean == root || pathWithin(clean, root) {
			return true
		}
	}
	return mcpPathSensitive(clean, home)
}

func mcpPathSensitive(path, home string) bool {
	if home == "" {
		return false
	}
	sensitive := []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".azure"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".docker"),
	}
	for _, prefix := range sensitive {
		if pathWithin(path, prefix) {
			return true
		}
	}
	return false
}
