package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransformMCPJSON_ProxyRewriteExpandAndPreserve(t *testing.T) {
	t.Setenv("SC_TOKEN", "s3cr3t")
	input := `{
	  "numFooBars": 3,
	  "oauthAccount": {"emailAddress": "u@example.test"},
	  "mcpServers": {
	    "proxied": {"command": "/opt/tool/srv", "args": ["--stdio"], "env": {"KEY": "v"}},
	    "direct":  {"command": "npx", "args": ["run"], "env": {"AUTH": "Bearer ${SC_TOKEN}"}},
	    "remote":  {"url": "https://api.example.test/mcp"}
	  }
	}`
	actions := map[string]mcpServerAction{
		"proxied": {rewriteProxy: true},
		"direct":  {expand: true},
		// remote intentionally unmanaged
	}
	out, injected, err := transformMCPJSON([]byte(input), "mcpServers", "", actions)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	// Non-mcp fields preserved.
	if string(root["numFooBars"]) != "3" {
		t.Errorf("numFooBars not preserved: %s", root["numFooBars"])
	}
	var servers map[string]map[string]interface{}
	json.Unmarshal(root["mcpServers"], &servers)

	if servers["proxied"]["command"] != "mittens-mcp-proxy" {
		t.Errorf("proxied command = %v", servers["proxied"]["command"])
	}
	if _, hasEnv := servers["proxied"]["env"]; hasEnv {
		t.Errorf("proxied env should be stripped: %v", servers["proxied"])
	}
	args, _ := servers["proxied"]["args"].([]interface{})
	if len(args) != 1 || args[0] != "proxied" {
		t.Errorf("proxied args = %v", servers["proxied"]["args"])
	}
	if env, _ := servers["direct"]["env"].(map[string]interface{}); env["AUTH"] != "Bearer s3cr3t" {
		t.Errorf("direct env not expanded: %v", servers["direct"]["env"])
	}
	if servers["remote"]["url"] != "https://api.example.test/mcp" {
		t.Errorf("remote url not preserved: %v", servers["remote"]["url"])
	}
	if len(injected["direct"]) != 1 || injected["direct"][0] != "SC_TOKEN" {
		t.Errorf("injected = %v", injected)
	}
}

func TestTransformMCPTOML_ProxyRewriteAndExpand(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghtok")
	input := `# top comment
model = "gpt"

[mcp_servers.shortcut]
command = "/opt/shortcut/bin"   # inline comment
args = [
  "--stdio",
  "run",
]

[mcp_servers.shortcut.env]
SHORTCUT_TOKEN = "secret"

[mcp_servers.gh]
command = "npx"
args = ["gh"]
env = { TOKEN = "${GH_TOKEN}" }
`
	actions := map[string]mcpServerAction{
		"shortcut": {rewriteProxy: true},
		"gh":       {expand: true},
	}
	out, _, err := transformMCPTOML([]byte(input), "mcp_servers", actions)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `command = "mittens-mcp-proxy"`) {
		t.Errorf("proxy command not rewritten:\n%s", got)
	}
	if !strings.Contains(got, `args = ["shortcut"]`) {
		t.Errorf("proxy args not collapsed:\n%s", got)
	}
	if strings.Contains(got, "SHORTCUT_TOKEN") {
		t.Errorf("proxy env subtable not stripped:\n%s", got)
	}
	if strings.Contains(got, `"run",`) {
		t.Errorf("multi-line args continuation not swallowed:\n%s", got)
	}
	if !strings.Contains(got, "# top comment") || !strings.Contains(got, `model = "gpt"`) {
		t.Errorf("unrelated lines not preserved byte-identical:\n%s", got)
	}
	if !strings.Contains(got, `TOKEN = "ghtok"`) {
		t.Errorf("gh env not expanded:\n%s", got)
	}
}

func TestTransformMCPTOML_FiltersUnselectedServers(t *testing.T) {
	input := `# retain this comment
model = "gpt"

[mcp_servers.selected]
command = "npx"

[mcp_servers.selected.env]
TOKEN = "${TOKEN:-default}"

[mcp_servers."not.selected"]
command = "should-not-remain"

[mcp_servers."not.selected".env]
SECRET = "nope"

[other.section]
value = "unchanged"
`
	out, _, err := transformMCPTOML([]byte(input), "mcp_servers", map[string]mcpServerAction{
		"selected": {expand: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `[mcp_servers.selected.env]`) || !strings.Contains(got, `TOKEN = "default"`) {
		t.Errorf("selected server was not retained and expanded:\n%s", got)
	}
	if strings.Contains(got, "not.selected") || strings.Contains(got, "should-not-remain") || strings.Contains(got, "SECRET") {
		t.Errorf("unselected server remained in staged TOML:\n%s", got)
	}
	if !strings.Contains(got, "# retain this comment\nmodel = \"gpt\"") || !strings.Contains(got, "[other.section]\nvalue = \"unchanged\"") {
		t.Errorf("non-MCP content changed:\n%s", got)
	}
}

func TestPlanMCPStaging_CodexEmptySelectionStagesDenyAll(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostConfig := filepath.Join(home, ".codex", "config.toml")
	hostData := []byte("model = \"gpt\"\n\n[mcp_servers.one]\ncommand = \"one\"\n\n[mcp_servers.two]\ncommand = \"two\"\n")
	if err := os.WriteFile(hostConfig, hostData, 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{Provider: CodexProvider(), Workspace: t.TempDir()}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.configBindSrc == "" || plan.configBindDst == "" {
		t.Fatalf("expected staged Codex config, got %#v", plan)
	}
	staged, err := os.ReadFile(plan.configBindSrc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(staged), "mcp_servers") || string(staged) != "model = \"gpt\"\n" {
		t.Errorf("staged config = %q, want non-MCP config only", staged)
	}
	unchanged, err := os.ReadFile(hostConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(hostData) {
		t.Errorf("host config changed: %q", unchanged)
	}
}

func TestPlanMCPStaging_CodexSelectedServerIsAllowlisted(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(`model = "gpt"

[mcp_servers.keep]
command = "npx"
env = { TOKEN = "${CODEX_MCP_TOKEN}" }

[mcp_servers.remove]
command = "unwanted"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_MCP_TOKEN", "expanded")
	app := &App{
		Provider:   CodexProvider(),
		Workspace:  t.TempDir(),
		MCPServers: []MCPServerPolicy{{Name: "keep", Mode: mcpModeDirect}},
	}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(plan.configBindSrc)
	if err != nil {
		t.Fatal(err)
	}
	got := string(staged)
	if !strings.Contains(got, `[mcp_servers.keep]`) || !strings.Contains(got, `TOKEN = "expanded"`) {
		t.Errorf("selected server was not staged: %s", got)
	}
	if strings.Contains(got, "remove") || strings.Contains(got, "unwanted") {
		t.Errorf("unselected server was staged: %s", got)
	}
}

func TestPlanMCPStaging_CodexAllIncludesEveryConfiguredServer(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[mcp_servers.one]\ncommand = \"one\"\n\n[mcp_servers.two]\ncommand = \"two\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{Provider: CodexProvider(), Workspace: t.TempDir(), MCPAll: true}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(plan.configBindSrc)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(staged); !strings.Contains(got, `[mcp_servers.one]`) || !strings.Contains(got, `[mcp_servers.two]`) {
		t.Errorf("MCPAll did not retain every configured server: %s", got)
	}
}

func TestAssembleDockerArgsE_CodexStagingFailureIsReturned(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex", "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{
		Provider:          CodexProvider(),
		NoHistory:         true,
		ContainerName:     "mittens-test",
		WorkspaceMountSrc: t.TempDir(),
		Credentials:       &CredentialManager{},
		MCPAll:            true,
	}
	if _, err := a.assembleDockerArgsE(nil, nil); err == nil {
		t.Fatal("expected Codex staging error")
	}
}

// TestPlanMCPStaging_PinMismatchRefused verifies a changed command refuses the
// proxy and never registers a spec.
func TestPlanMCPStaging_PinMismatchRefused(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{
	  "mcpServers": {"sc": {"command": "/opt/new/bin", "args": ["x"]}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		Provider:  ClaudeProvider(),
		Workspace: workspace,
		MCPServers: []MCPServerPolicy{{
			Name: "sc", Mode: mcpModeProxy, CommandPin: "sha256:stalepin",
		}},
	}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.proxySpecs) != 0 {
		t.Errorf("expected no proxy specs on pin mismatch, got %v", plan.proxySpecs)
	}
	if app.mcpProxyRefusals["sc"] != "refused: command changed" {
		t.Errorf("refusal = %q", app.mcpProxyRefusals["sc"])
	}
}

// TestPlanMCPStaging_UnpinnedProxyRefused verifies a proxy server with no
// command_pin is refused (never registered/rewritten unverified).
func TestPlanMCPStaging_UnpinnedProxyRefused(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{
	  "mcpServers": {"sc": {"command": "/opt/x", "args": []}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{
		Provider:   ClaudeProvider(),
		Workspace:  workspace,
		MCPServers: []MCPServerPolicy{{Name: "sc", Mode: mcpModeProxy}}, // no pin
	}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.proxySpecs) != 0 {
		t.Errorf("unpinned proxy must not register, got %v", plan.proxySpecs)
	}
	if app.mcpProxyRefusals["sc"] != "refused: unpinned" {
		t.Errorf("refusal = %q, want 'refused: unpinned'", app.mcpProxyRefusals["sc"])
	}
}

// TestPlanMCPStaging_WorkspaceProxyRefused verifies a workspace-scope (.mcp.json)
// server is refused proxy mode even if a pin is present (v1 restriction).
func TestPlanMCPStaging_WorkspaceProxyRefused(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
	  "mcpServers": {"ws": {"command": "/opt/x", "args": []}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pin := mcpCommandPin(readMCPServers(ClaudeProvider(), home, workspace)["ws"])
	app := &App{
		Provider:   ClaudeProvider(),
		Workspace:  workspace,
		MCPServers: []MCPServerPolicy{{Name: "ws", Mode: mcpModeProxy, CommandPin: pin}},
	}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.proxySpecs) != 0 {
		t.Errorf("workspace-scope proxy must be refused, got %v", plan.proxySpecs)
	}
	if app.mcpProxyRefusals["ws"] != "refused: workspace-only" {
		t.Errorf("refusal = %q, want 'refused: workspace-only'", app.mcpProxyRefusals["ws"])
	}
}

// TestPlanMCPStaging_PinMatchApproved verifies a matching pin approves the proxy
// and rewrites the staged config.
func TestPlanMCPStaging_PinMatchApproved(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{
	  "mcpServers": {"sc": {"command": "/opt/tool/bin", "args": ["--stdio"]}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Compute the correct pin for the configured command.
	pin := mcpCommandPin(readMCPServers(ClaudeProvider(), home, workspace)["sc"])
	app := &App{
		Provider:  ClaudeProvider(),
		Workspace: workspace,
		MCPServers: []MCPServerPolicy{{
			Name: "sc", Mode: mcpModeProxy, CommandPin: pin,
		}},
	}
	plan, err := app.planMCPStaging(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.proxySpecs) != 1 || plan.proxySpecs[0].Command != "/opt/tool/bin" {
		t.Fatalf("expected one proxy spec, got %v", plan.proxySpecs)
	}
	if plan.prefsOverride == "" {
		t.Fatal("expected a transformed prefs override path")
	}
	data, _ := os.ReadFile(plan.prefsOverride)
	if !strings.Contains(string(data), "mittens-mcp-proxy") {
		t.Errorf("staged config not rewritten:\n%s", data)
	}
}
