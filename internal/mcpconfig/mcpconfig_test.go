package mcpconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestReadProvider_ClaudeJSON_TopLevelAndProject(t *testing.T) {
	home := t.TempDir()
	ws := "/work/repo"
	path := writeFile(t, home, ".claude.json", `{
	  "mcpServers": {
	    "shortcut": {"command": "npx", "args": ["-y", "@shortcut/mcp"], "env": {"TOKEN": "abc"}},
	    "remote": {"url": "https://example.test/mcp", "headers": {"Authorization": "Bearer x"}}
	  },
	  "projects": {
	    "/work/repo": {
	      "mcpServers": {"local": {"command": "/opt/tools/server", "cwd": "/work/repo"}}
	    }
	  }
	}`)

	servers := ReadProvider(path, "json", "mcpServers", ws)
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d: %v", len(servers), Names(servers))
	}
	sc := servers["shortcut"]
	if !sc.IsStdio() || sc.Command != "npx" || len(sc.Args) != 2 {
		t.Fatalf("shortcut parsed wrong: %+v", sc)
	}
	if sc.Env["TOKEN"] != "abc" {
		t.Fatalf("expected env TOKEN=abc, got %v", sc.Env)
	}
	if sc.Scope != ScopeUser {
		t.Fatalf("expected user scope, got %q", sc.Scope)
	}
	remote := servers["remote"]
	if remote.IsStdio() || remote.URL == "" || remote.Headers["Authorization"] != "Bearer x" {
		t.Fatalf("remote parsed wrong: %+v", remote)
	}
	local := servers["local"]
	if local.Command != "/opt/tools/server" || local.Dir != "/work/repo" {
		t.Fatalf("local parsed wrong: %+v", local)
	}
}

func TestReadProvider_CodexTOML_WithEnvAndMultilineArgs(t *testing.T) {
	home := t.TempDir()
	path := writeFile(t, home, "config.toml", `
# codex config
model = "gpt"

[mcp_servers.shortcut]
command = "npx"
args = [
  "-y",
  "@shortcut/mcp",
]

[mcp_servers.shortcut.env]
SHORTCUT_TOKEN = "secret"

[mcp_servers.remote]
url = "https://example.test/sse"
`)

	servers := ReadProvider(path, "toml", "mcp_servers", "")
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d: %v", len(servers), Names(servers))
	}
	sc := servers["shortcut"]
	if sc.Command != "npx" || len(sc.Args) != 2 || sc.Args[1] != "@shortcut/mcp" {
		t.Fatalf("shortcut toml parsed wrong: %+v", sc)
	}
	if sc.Env["SHORTCUT_TOKEN"] != "secret" {
		t.Fatalf("expected env SHORTCUT_TOKEN=secret, got %v", sc.Env)
	}
	if servers["remote"].URL == "" {
		t.Fatalf("remote url missing: %+v", servers["remote"])
	}
}

func TestReadWorkspace_ScopeAndRelativePaths(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, ws, ".mcp.json", `{"mcpServers": {"fs": {"command": "./bin/server.js"}}}`)
	servers := ReadWorkspace(ws)
	fs := servers["fs"]
	if fs.Scope != ScopeWorkspace {
		t.Fatalf("expected workspace scope, got %q", fs.Scope)
	}
	if fs.Command != filepath.Join(ws, "bin/server.js") {
		t.Fatalf("relative command not resolved: %q", fs.Command)
	}
}

func TestReadWorkspace_BareRelativePathResolvesWhenExists(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "dist"), "server.js", "//\n")
	writeFile(t, ws, ".mcp.json", `{"mcpServers": {
	  "local": {"command": "node", "args": ["dist/server.js"]},
	  "pkg":   {"command": "npx", "args": ["@scope/pkg"]}
	}}`)
	servers := ReadWorkspace(ws)
	// Bare-relative path that exists is resolved to absolute.
	if got := servers["local"].Args[0]; got != filepath.Join(ws, "dist/server.js") {
		t.Errorf("bare-relative arg not resolved: %q", got)
	}
	// Package specifier (no such file) is left untouched.
	if got := servers["pkg"].Args[0]; got != "@scope/pkg" {
		t.Errorf("package specifier should be untouched, got %q", got)
	}
}

func TestReadProvider_TOMLDottedQuotedServerName(t *testing.T) {
	home := t.TempDir()
	path := writeFile(t, home, "config.toml", `
[mcp_servers."my.server"]
command = "npx"
args = ["run"]

[mcp_servers."my.server".env]
TOKEN = "x"
`)
	servers := ReadProvider(path, "toml", "mcp_servers", "")
	s, ok := servers["my.server"]
	if !ok {
		t.Fatalf("dotted quoted server name not parsed: %v", Names(servers))
	}
	if s.Command != "npx" || s.Env["TOKEN"] != "x" {
		t.Fatalf("dotted server parsed wrong: %+v", s)
	}
}

func TestSplitServerSection(t *testing.T) {
	cases := []struct{ in, name, sub string }{
		{"shortcut", "shortcut", ""},
		{"shortcut.env", "shortcut", "env"},
		{`"my.server"`, "my.server", ""},
		{`"my.server".env`, "my.server", "env"},
		{`'a.b'.env`, "a.b", "env"},
	}
	for _, c := range cases {
		name, sub := SplitServerSection(c.in)
		if name != c.name || sub != c.sub {
			t.Errorf("SplitServerSection(%q) = (%q,%q), want (%q,%q)", c.in, name, sub, c.name, c.sub)
		}
	}
}

func TestMerge_SrcWins(t *testing.T) {
	dst := map[string]Server{"a": {Name: "a", Command: "old"}}
	src := map[string]Server{"a": {Name: "a", Command: "new"}, "b": {Name: "b"}}
	got := Merge(dst, src)
	if got["a"].Command != "new" || len(got) != 2 {
		t.Fatalf("merge wrong: %+v", got)
	}
}
