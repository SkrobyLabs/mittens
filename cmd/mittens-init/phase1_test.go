package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestLookupSupplementaryGroupsInFile(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	data := `root:x:0:
claude:x:1000:
docker:x:998:claude
video:x:44:other, claude
primary-duplicate:x:1000:claude
malformed
badgid:x:not-a-number:claude
`
	if err := os.WriteFile(groupPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := lookupSupplementaryGroupsInFile(groupPath, "claude", 1000)
	if err != nil {
		t.Fatal(err)
	}

	want := []int{998, 44}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestLookupSupplementaryGroupsInFileNoMemberships(t *testing.T) {
	dir := t.TempDir()
	groupPath := filepath.Join(dir, "group")
	if err := os.WriteFile(groupPath, []byte("docker:x:998:someoneelse\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := lookupSupplementaryGroupsInFile(groupPath, "claude", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("groups = %v, want empty", got)
	}
}

func TestExtractMCPJSONServerNames_ProjectScopedClaudeConfig(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "project")
	f := filepath.Join(tmp, ".claude.json")
	content := `{
	"mcpServers": {
		"user-server": {"url": "https://user.example.com/mcp"}
	},
	"projects": {
		"` + project + `": {
			"mcpServers": {
				"project-server": {"url": "https://project.example.com/mcp"}
			}
		}
	}
}`
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	names := extractMCPServerNames(mcpConfig{Path: f, Format: "json", Key: "mcpServers", ProjectPath: project})
	sort.Strings(names)
	want := []string{"project-server", "user-server"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}

	host := extractMCPServerURL(mcpConfig{Path: f, Format: "json", Key: "mcpServers", ProjectPath: project}, "project-server")
	if host != "project.example.com" {
		t.Fatalf("host = %q, want project.example.com", host)
	}
}

func TestExtractMCPTOMLServerNamesAndURL(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config.toml")
	content := `
[mcp_servers.github]
url = "https://api.githubcopilot.com/mcp"

[mcp_servers."linear-team"]
command = "npx"
args = ["-y", "linear-mcp"]
`
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := mcpConfig{Path: f, Format: "toml", Key: "mcp_servers"}
	names := extractMCPServerNames(cfg)
	sort.Strings(names)
	want := []string{"github", "linear-team"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}

	host := extractMCPServerURL(cfg, "github")
	if host != "api.githubcopilot.com" {
		t.Fatalf("host = %q, want api.githubcopilot.com", host)
	}
}
