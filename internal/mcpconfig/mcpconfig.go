// Package mcpconfig is the single shared parser for MCP server definitions
// across all providers (Claude JSON, Codex TOML, Gemini JSON) and the workspace
// .mcp.json. It captures command, args, url, env, headers, and working
// directory, resolving relative path tokens against the config directory.
//
// It is deliberately dependency-free (mirroring internal/fileutil) so both the
// host binary and the container-side mittens-init can import it.
package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Scope records where a server definition came from. Workspace-scope servers
// originate from the repo-controlled .mcp.json; everything else (including
// Claude's projects.<path> entries, which live in user config) is user scope.
type Scope string

const (
	ScopeUser      Scope = "user"
	ScopeWorkspace Scope = "workspace"
)

// Server is a single parsed MCP server definition.
type Server struct {
	Name    string
	Command string
	Args    []string
	URL     string
	Env     map[string]string
	Headers map[string]string
	Dir     string
	Source  string
	Scope   Scope
}

// IsStdio reports whether the server is a stdio (command-based) server rather
// than a URL-based remote server.
func (s Server) IsStdio() bool {
	return s.Command != "" && s.URL == ""
}

// source describes one config file to parse.
type source struct {
	path        string
	format      string // "json" or "toml"
	key         string // servers key, e.g. "mcpServers" or "mcp_servers"
	projectPath string // workspace path for JSON "projects" lookup; "" to skip
	baseDir     string // base for resolving relative path tokens
	scope       Scope
}

// ReadProvider reads a provider MCP config file (user scope) and returns servers
// keyed by name. workspace, when non-empty, enables the JSON "projects.<path>"
// lookup used by Claude's ~/.claude.json.
func ReadProvider(configPath, format, serversKey, workspace string) map[string]Server {
	if configPath == "" {
		return nil
	}
	return read(source{
		path:        configPath,
		format:      format,
		key:         serversKey,
		projectPath: workspace,
		baseDir:     filepath.Dir(configPath),
		scope:       ScopeUser,
	})
}

// ReadWorkspace reads the repo-controlled <workspace>/.mcp.json (workspace scope).
func ReadWorkspace(workspace string) map[string]Server {
	if workspace == "" {
		return nil
	}
	return read(source{
		path:    filepath.Join(workspace, ".mcp.json"),
		format:  "json",
		key:     "mcpServers",
		baseDir: workspace,
		scope:   ScopeWorkspace,
	})
}

// Merge overlays src onto dst (src wins) and returns the merged map. dst may be
// nil.
func Merge(dst, src map[string]Server) map[string]Server {
	if dst == nil {
		dst = map[string]Server{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// Names returns the sorted, de-duplicated server names in the map.
func Names(servers map[string]Server) []string {
	if len(servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func read(src source) map[string]Server {
	switch src.format {
	case "json":
		return readJSON(src)
	case "toml":
		return readTOML(src)
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// JSON
// ---------------------------------------------------------------------------

func readJSON(src source) map[string]Server {
	data, err := os.ReadFile(src.path)
	if err != nil {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	out := map[string]Server{}
	addJSONServers(out, root[src.key], src)
	if src.projectPath != "" {
		var projects map[string]json.RawMessage
		if err := json.Unmarshal(root["projects"], &projects); err == nil {
			if raw, ok := projects[src.projectPath]; ok {
				var project map[string]json.RawMessage
				if err := json.Unmarshal(raw, &project); err == nil {
					addJSONServers(out, project[src.key], src)
				}
			}
		}
	}
	return out
}

func addJSONServers(out map[string]Server, raw json.RawMessage, src source) {
	if len(raw) == 0 {
		return
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil {
		return
	}
	for name, rawServer := range servers {
		var entry struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
			Cwd     string            `json:"cwd"`
		}
		if err := json.Unmarshal(rawServer, &entry); err != nil {
			continue
		}
		out[name] = Server{
			Name:    name,
			Source:  src.path,
			Scope:   src.scope,
			Command: resolvePathToken(entry.Command, src.baseDir),
			Args:    resolvePathTokens(entry.Args, src.baseDir),
			URL:     entry.URL,
			Env:     entry.Env,
			Headers: entry.Headers,
			Dir:     entry.Cwd,
		}
	}
}

// ---------------------------------------------------------------------------
// TOML (Codex config.toml). Hand-rolled line parser; also captures env from the
// nested [mcp_servers.<name>.env] subtable.
// ---------------------------------------------------------------------------

func readTOML(src source) map[string]Server {
	data, err := os.ReadFile(src.path)
	if err != nil {
		return nil
	}
	out := map[string]Server{}
	var current string
	inEnv := false
	var argsBuf strings.Builder
	collectingArgs := false

	flushArgs := func() {
		if current == "" || argsBuf.Len() == 0 {
			return
		}
		server := out[current]
		server.Args = resolvePathTokens(parseTOMLStringArray(argsBuf.String()), src.baseDir)
		out[current] = server
		argsBuf.Reset()
		collectingArgs = false
	}

	prefix := src.key + "."
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := stripTOMLComment(strings.TrimSpace(rawLine))
		if line == "" {
			continue
		}
		if section := tomlSectionName(line); section != "" {
			flushArgs()
			current = ""
			inEnv = false
			if strings.HasPrefix(section, prefix) {
				name, sub := SplitServerSection(strings.TrimPrefix(section, prefix))
				if name != "" {
					current = name
					inEnv = sub == "env"
					if _, ok := out[name]; !ok {
						out[name] = Server{Name: name, Source: src.path, Scope: src.scope}
					}
				}
			}
			continue
		}
		if current == "" {
			continue
		}
		if collectingArgs {
			argsBuf.WriteByte(' ')
			argsBuf.WriteString(line)
			if strings.Contains(line, "]") {
				flushArgs()
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		server := out[current]
		if inEnv {
			if server.Env == nil {
				server.Env = map[string]string{}
			}
			server.Env[unquoteTOMLKey(key)] = unquoteTOMLString(value)
			out[current] = server
			continue
		}
		switch key {
		case "command":
			server.Command = resolvePathToken(unquoteTOMLString(value), src.baseDir)
		case "url":
			server.URL = unquoteTOMLString(value)
		case "cwd":
			server.Dir = unquoteTOMLString(value)
		case "args":
			argsBuf.WriteString(value)
			if strings.Contains(value, "]") {
				server.Args = resolvePathTokens(parseTOMLStringArray(value), src.baseDir)
				argsBuf.Reset()
			} else {
				collectingArgs = true
			}
		}
		out[current] = server
	}
	flushArgs()
	return out
}

// ---------------------------------------------------------------------------
// Shared token / string helpers
// ---------------------------------------------------------------------------

func resolvePathTokens(tokens []string, baseDir string) []string {
	if len(tokens) == 0 {
		return nil
	}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, resolvePathToken(token, baseDir))
	}
	return out
}

// resolvePathToken resolves relative path tokens against baseDir. Explicit
// relative paths ("./x", "../x") always resolve. Other tokens containing a
// slash resolve only if they name an existing file/dir under baseDir — this
// catches bare-relative helper paths ("dist/server.js") while leaving package
// specifiers ("@scope/pkg", "lodash/foo") untouched so proxy/host spawns
// receive the command exactly as configured.
func resolvePathToken(token, baseDir string) string {
	token = strings.TrimSpace(token)
	if token == "" || filepath.IsAbs(token) || isLikelyURL(token) || strings.HasPrefix(token, "-") {
		return token
	}
	if strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") || token == "." || token == ".." {
		if baseDir == "" {
			return token
		}
		return filepath.Clean(filepath.Join(baseDir, token))
	}
	if baseDir != "" && strings.Contains(token, "/") {
		candidate := filepath.Clean(filepath.Join(baseDir, token))
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return token
}

func isLikelyURL(token string) bool {
	lower := strings.ToLower(token)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://")
}

// SplitServerSection splits a TOML section suffix (after the servers-key
// prefix) into the server name and an optional sub-table (e.g. "env"). It is
// quote-aware so dotted quoted names like `"my.server".env` split correctly
// into ("my.server", "env").
func SplitServerSection(rest string) (name, sub string) {
	rest = strings.TrimSpace(rest)
	if len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') {
		q := rest[0]
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			name = rest[1 : 1+end]
			remainder := rest[1+end+1:]
			if strings.HasPrefix(remainder, ".") {
				sub = remainder[1:]
			}
			return name, sub
		}
	}
	if idx := strings.IndexByte(rest, '.'); idx >= 0 {
		return unquoteTOMLKey(rest[:idx]), rest[idx+1:]
	}
	return unquoteTOMLKey(rest), ""
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

func stripTOMLComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inDouble {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return strings.TrimSpace(line[:i])
			}
		}
	}
	return line
}

func unquoteTOMLKey(s string) string {
	return unquoteTOMLString(strings.TrimSpace(s))
}

func unquoteTOMLString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

var tomlStringRE = regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'`)

func parseTOMLStringArray(value string) []string {
	var out []string
	for _, match := range tomlStringRE.FindAllStringSubmatch(value, -1) {
		if match[1] != "" {
			out = append(out, strings.ReplaceAll(match[1], `\"`, `"`))
			continue
		}
		out = append(out, match[2])
	}
	return out
}
