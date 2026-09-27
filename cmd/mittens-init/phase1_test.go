package main

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPhase1FirewallFailureStopsBeforePrivilegeDrop(t *testing.T) {
	for _, tc := range []struct {
		name          string
		missingConfig bool
		proxyFailure  bool
		failRule      string
		wantError     string
	}{
		{name: "missing config", missingConfig: true, wantError: "firewall.conf not found"},
		{name: "proxy startup", proxyFailure: true, wantError: "starting proxy"},
		{name: "first IPv4 rule", failRule: "iptables -F OUTPUT", wantError: "iptables setup"},
		{name: "partial IPv4 installation", failRule: "iptables -A OUTPUT -p udp --dport 53 -j ACCEPT", wantError: "iptables setup"},
		{name: "IPv4 SSH rule", failRule: "iptables -A OUTPUT -p tcp --dport 22 -j ACCEPT", wantError: "iptables setup"},
		{name: "first IPv6 rule", failRule: "ip6tables -F OUTPUT", wantError: "iptables setup"},
		{name: "partial IPv6 installation", failRule: "ip6tables -A OUTPUT -p udp --dport 53 -j ACCEPT", wantError: "iptables setup"},
		{name: "IPv6 SSH rule", failRule: "ip6tables -A OUTPUT -p tcp --dport 22 -j ACCEPT", wantError: "iptables setup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config{
				Firewall:     true,
				FirewallConf: filepath.Join(t.TempDir(), "firewall.conf"),
				AIDir:        filepath.Join(t.TempDir(), "agent"),
				AIUsername:   "root",
			}
			if !tc.missingConfig {
				if err := os.WriteFile(cfg.FirewallConf, []byte("example.com\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "NODE_OPTIONS"} {
				t.Setenv(name, "unchanged")
			}
			injected := errors.New("injected setup failure")
			proxyStarted, dropped := false, false
			var commands []string
			setup := func(cfg *config) error {
				return setupFirewallWithNetwork(cfg, func(domains []string, _ *config) error {
					proxyStarted = true
					if !reflect.DeepEqual(domains, []string{"example.com"}) {
						t.Fatalf("proxy domains = %v", domains)
					}
					if tc.proxyFailure {
						return injected
					}
					return nil
				}, func(cfg *config) error {
					return setupIPTablesWithRunner(cfg, true, func(args ...string) error {
						command := strings.Join(args, " ")
						commands = append(commands, command)
						if command == tc.failRule {
							return injected
						}
						return nil
					})
				})
			}
			err := runPhase1WithSetup(cfg, setup, func(*config) error {
				dropped = true
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "refusing to start agent") || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("runPhase1WithSetup() = %v, want fatal %q", err, tc.wantError)
			}
			if !tc.missingConfig && !errors.Is(err, injected) {
				t.Fatalf("setup error not preserved: %v", err)
			}
			if dropped {
				t.Fatal("privileges dropped after firewall failure")
			}
			if _, err := os.Stat(cfg.AIDir); !os.IsNotExist(err) {
				t.Fatalf("user setup ran after firewall failure: %v", err)
			}
			if tc.missingConfig && proxyStarted {
				t.Fatal("proxy started without firewall configuration")
			}
			if tc.failRule != "" {
				if len(commands) == 0 || commands[len(commands)-1] != tc.failRule {
					t.Fatalf("rule installation did not stop on failed rule: %v", commands)
				}
			} else if len(commands) != 0 {
				t.Fatalf("rules installed before proxy readiness: %v", commands)
			}
			for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "NODE_OPTIONS"} {
				if os.Getenv(name) != "unchanged" {
					t.Errorf("%s changed before firewall installation succeeded", name)
				}
			}
		})
	}
}

func TestPhase1FirewallSuccessPublishesProxyEnvironmentBeforePrivilegeDrop(t *testing.T) {
	cfg := &config{Firewall: true, AIUsername: "root", AIDir: t.TempDir()}
	cfg.FirewallConf = filepath.Join(t.TempDir(), "firewall.conf")
	if err := os.WriteFile(cfg.FirewallConf, []byte("example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "NODE_OPTIONS"} {
		t.Setenv(name, "")
	}
	t.Setenv("NODE_OPTIONS", "--trace-warnings")
	ready, installed, dropped := false, false, false
	setup := func(cfg *config) error {
		return setupFirewallWithNetwork(cfg, func([]string, *config) error {
			ready = true
			return nil
		}, func(*config) error {
			if !ready {
				t.Fatal("rules installed before proxy readiness")
			}
			installed = true
			return nil
		})
	}
	err := runPhase1WithSetup(cfg, setup, func(*config) error {
		dropped = true
		if !installed {
			t.Fatal("privileges dropped before enforcement installed")
		}
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			if got := os.Getenv(name); got != "http://127.0.0.1:3128" {
				t.Errorf("%s = %q, want configured proxy", name, got)
			}
		}
		if got := os.Getenv("NODE_OPTIONS"); got != "--trace-warnings --use-env-proxy" {
			t.Errorf("NODE_OPTIONS = %q", got)
		}
		return nil
	})
	if err != nil || !dropped {
		t.Fatalf("successful firewall prevented startup: dropped=%v error=%v", dropped, err)
	}
}

func TestPhase1DisabledFirewallStillStartsAgent(t *testing.T) {
	cfg := &config{AIUsername: "root", AIDir: t.TempDir()}
	dropped := false
	err := runPhase1WithSetup(cfg, func(*config) error {
		t.Fatal("firewall setup called when disabled")
		return nil
	}, func(*config) error {
		dropped = true
		return nil
	})
	if err != nil || !dropped {
		t.Fatalf("disabled firewall prevented startup: dropped=%v error=%v", dropped, err)
	}
}

func TestSetupIPTablesDisabledIPv6AndOptionalLogging(t *testing.T) {
	var commands []string
	err := setupIPTablesWithRunner(&config{}, false, func(args ...string) error {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, " -j LOG ") {
			return errors.New("kernel LOG module unavailable")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if !strings.HasPrefix(command, "iptables ") {
			t.Fatalf("ran IPv6 command when IPv6 disabled: %s", command)
		}
	}
	if len(commands) == 0 || commands[len(commands)-1] != "iptables -A OUTPUT -p tcp --dport 22 -j ACCEPT" {
		t.Fatalf("optional logging failure interrupted enforcement: %v", commands)
	}
}

func TestFirewallNeedsIPv6(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses string
		all       string
		defaults  string
		readErr   error
		want      bool
		wantErr   bool
	}{
		{name: "unsupported kernel", readErr: os.ErrNotExist},
		{name: "unreadable kernel state", readErr: os.ErrPermission, wantErr: true},
		{name: "active interface", addresses: "00000000000000000000000000000001 01 80 10 80 lo\n", want: true},
		{name: "disabled current and future interfaces", all: "1\n", defaults: "1\n"},
		{name: "current interfaces can enable IPv6", all: "0\n", defaults: "1\n", want: true},
		{name: "future interfaces can enable IPv6", all: "1\n", defaults: "0\n", want: true},
		{name: "invalid kernel setting", all: "unexpected", wantErr: true},
		{name: "missing kernel setting", all: "missing", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := firewallNeedsIPv6(func(path string) ([]byte, error) {
				switch path {
				case "/proc/net/if_inet6":
					return []byte(tc.addresses), tc.readErr
				case "/proc/sys/net/ipv6/conf/all/disable_ipv6":
					if tc.all == "missing" {
						return nil, os.ErrNotExist
					}
					return []byte(tc.all), nil
				case "/proc/sys/net/ipv6/conf/default/disable_ipv6":
					return []byte(tc.defaults), nil
				default:
					t.Fatalf("unexpected kernel setting read: %s", path)
					return nil, nil
				}
			})
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("firewallNeedsIPv6() = %v, %v; want %v, error=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

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

func TestEnsureProjectsDirWritable(t *testing.T) {
	home := t.TempDir()
	cfg := &config{AIUsername: "root", AIDir: filepath.Join(home, ".claude")}

	// Simulate a pre-existing per-project history bind mount: its contents map
	// to host files and must survive untouched (the chown is non-recursive).
	child := filepath.Join(cfg.AIDir, "projects", "mounted-project")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(child, "history.jsonl")
	if err := os.WriteFile(marker, []byte("host data"), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureProjectsDirWritable(cfg)

	// The projects parent must exist so the agent CLI can create sibling
	// transcript directories at runtime (e.g. during compaction).
	if fi, err := os.Stat(filepath.Join(cfg.AIDir, "projects")); err != nil || !fi.IsDir() {
		t.Fatalf("projects dir missing: %v", err)
	}
	// Pre-existing bind-mount content is left intact.
	if data, err := os.ReadFile(marker); err != nil || string(data) != "host data" {
		t.Fatalf("mounted project content disturbed: data=%q err=%v", data, err)
	}
}

func TestEnsureProjectsDirWritableCreatesMissingParent(t *testing.T) {
	home := t.TempDir()
	cfg := &config{AIUsername: "root", AIDir: filepath.Join(home, ".claude")}

	ensureProjectsDirWritable(cfg)

	if fi, err := os.Stat(filepath.Join(cfg.AIDir, "projects")); err != nil || !fi.IsDir() {
		t.Fatalf("projects dir not created: %v", err)
	}
}

func TestPingDockerDaemonUsesLightweightPingEndpoint(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			t.Errorf("request path = %q, want /_ping", r.URL.Path)
		}
		_, _ = w.Write([]byte("OK"))
	})}
	go server.Serve(listener)
	defer server.Close()

	if err := pingDockerDaemon(sock, time.Second); err != nil {
		t.Fatalf("pingDockerDaemon() error = %v", err)
	}
}

func TestPingDockerDaemonHonorsTimeout(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	})}
	go server.Serve(listener)
	defer func() {
		close(block)
		server.Close()
	}()

	start := time.Now()
	if err := pingDockerDaemon(sock, 20*time.Millisecond); err == nil {
		t.Fatal("pingDockerDaemon() error = nil, want timeout")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("pingDockerDaemon() took %s, want bounded timeout", elapsed)
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
