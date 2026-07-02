package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

func mountApp(provider *Provider, home, workspace, mountSrc string, worktree bool, servers ...MCPServerPolicy) *App {
	return &App{
		Provider:          provider,
		Workspace:         workspace,
		WorkspaceMountSrc: mountSrc,
		Worktree:          worktree,
		MCPServers:        servers,
	}
}

func TestPlanMCPHelperMounts_CodexStdioLocalScriptMountsRepoRoot(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	repo := filepath.Join(t.TempDir(), "mcpfilter")
	if err := os.MkdirAll(filepath.Join(repo, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dist", "cli.js"), []byte("console.log('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Skipf("git init unavailable: %v", err)
	}

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `
[mcp_servers.shortcut]
command = "node"
args = [
  "` + filepath.Join(repo, "dist", "cli.js") + `",
  "--upstream",
  "https://mcp.shortcut.com/mcp"
]

[mcp_servers.remote]
url = "https://example.com/mcp"
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	app := mountApp(CodexProvider(), home, workspace, workspace, false,
		MCPServerPolicy{Name: "shortcut", Mode: mcpModeMount},
		MCPServerPolicy{Name: "remote", Mode: mcpModeMount})

	mounts := app.planMCPHelperMounts(home)
	if len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want one", mounts)
	}
	if mounts[0].Path != repo || mounts[0].Access != "ro" || mounts[0].Server != "shortcut" {
		t.Fatalf("mount = %#v, want %s ro for shortcut", mounts[0], repo)
	}
}

func TestPlanMCPHelperMounts_SelectedServerMissingForProviderWarnsAndSkips(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(`[mcp_servers.codex_only]
command = "node"
args = []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := mountApp(ClaudeProvider(), home, workspace, workspace, false,
		MCPServerPolicy{Name: "codex_only", Mode: mcpModeMount})

	if mounts := app.planMCPHelperMounts(home); len(mounts) != 0 {
		t.Fatalf("mounts = %#v, want none for MCP server missing from Claude config", mounts)
	}
}

func TestPlanMCPHelperMounts_SkipsSensitivePaths(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(sshDir, "mcp-helper")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(`[mcp_servers.secret]
command = "`+tool+`"
args = []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := mountApp(CodexProvider(), home, workspace, workspace, false,
		MCPServerPolicy{Name: "secret", Mode: mcpModeMount})

	if mounts := app.planMCPHelperMounts(home); len(mounts) != 0 {
		t.Fatalf("mounts = %#v, want none for sensitive path", mounts)
	}
}

func TestPlanMCPHelperMounts_WorkspaceMCPRelativePath(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "server.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
  "mcpServers": {
    "local": {
      "command": "python",
      "args": ["./server.py"]
    }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := mountApp(ClaudeProvider(), home, workspace, "/some/worktree", true,
		MCPServerPolicy{Name: "local", Mode: mcpModeMount})

	mounts := app.planMCPHelperMounts(home)
	if len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want one", mounts)
	}
	if mounts[0].Path != workspace {
		t.Fatalf("mount path = %q, want original workspace path %q", mounts[0].Path, workspace)
	}
}

// TestPlanMCPHelperMounts_OnlyMountModeServers verifies direct/proxy servers are
// never auto-mounted, only mode:mount servers.
func TestPlanMCPHelperMounts_OnlyMountModeServers(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "server.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
  "mcpServers": {"local": {"command": "python", "args": ["./server.py"]}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := mountApp(ClaudeProvider(), home, workspace, "/some/worktree", true,
		MCPServerPolicy{Name: "local", Mode: mcpModeDirect})
	if mounts := app.planMCPHelperMounts(home); len(mounts) != 0 {
		t.Fatalf("mounts = %#v, want none for direct-mode server", mounts)
	}
}

// mcpMountCandidatesArgs exercises the tightened discovery rules (Resolved Q11)
// directly against mcpMountCandidates.
func mcpMountCandidatesArgs(command string, args ...string) []string {
	return mcpMountCandidates(mcpconfig.Server{Command: command, Args: args})
}

// TestMCPMountCandidates_Q11 covers the five discovery cases from Resolved Q11.
func TestMCPMountCandidates_Q11(t *testing.T) {
	// (a) the command itself (host-absolute script) is a candidate.
	if got := mcpMountCandidates(mcpconfig.Server{Command: "/opt/repo/server.py"}); !contains(got, "/opt/repo/server.py") {
		t.Fatalf("(a) command script should be a candidate, got %v", got)
	}

	// (b) a .sh script arg is a candidate; (c) a --log-file data arg is not.
	got := mcpMountCandidatesArgs("node", "/abs/entry.js", "--log-file", "/tmp/x.log")
	if !contains(got, "/abs/entry.js") {
		t.Fatalf("(b/c) expected entry.js candidate, got %v", got)
	}
	if contains(got, "/tmp/x.log") {
		t.Fatalf("(c) data arg /tmp/x.log must not be a candidate, got %v", got)
	}

	// (b) .sh arg mounted even when not first positional.
	got = mcpMountCandidatesArgs("bash", "/abs/main.py", "/abs/plugin.sh")
	if !contains(got, "/abs/plugin.sh") {
		t.Fatalf("(b) .sh arg should be a candidate, got %v", got)
	}

	// (d) an /etc scope arg is refused by mcpMountRefused.
	if !mcpMountRefused("/etc", "/home/u") {
		t.Fatalf("(d) /etc must be refused")
	}
	if !mcpMountRefused("/etc/passwd", "/home/u") {
		t.Fatalf("(d) paths under /etc must be refused")
	}

	// (e) first non-flag arg (a directory) is a candidate.
	got = mcpMountCandidatesArgs("mcp-server-filesystem", "/srv/data")
	if !contains(got, "/srv/data") {
		t.Fatalf("(e) first non-flag arg should be a candidate, got %v", got)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
