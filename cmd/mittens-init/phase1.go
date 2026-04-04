package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// runPhase1 performs root-level setup: DinD, Docker socket, network firewall
// (Go proxy + iptables), then drops privileges and re-execs as the AI user.
func runPhase1(cfg *config) error {
	if cfg.DinD {
		startDinD()
	}

	if cfg.DockerHost {
		setupDockerSocket(cfg)
	}

	if cfg.Firewall {
		if err := setupFirewall(cfg); err != nil {
			logWarn("Firewall setup failed: %v", err)
		}
	}

	// Drop privileges and re-exec this binary as the AI user.
	return dropPrivileges(cfg)
}

// startDinD starts the Docker daemon for Docker-in-Docker mode.
func startDinD() {
	logInfo("Starting Docker daemon...")

	cmd := exec.Command("dockerd",
		"--host=unix:///var/run/docker.sock",
		"--storage-driver=overlay2",
	)
	logFile, _ := os.Create("/tmp/dockerd.log")
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		logWarn("Docker daemon failed to start: %v", err)
		return
	}

	// Wait for dockerd to be ready.
	for i := 0; i < 30; i++ {
		if exec.Command("docker", "info").Run() == nil {
			logInfo("Docker daemon ready")
			return
		}
		time.Sleep(time.Second)
	}

	logWarn("Docker daemon failed to start")
	if data, err := os.ReadFile("/tmp/dockerd.log"); err == nil {
		lines := strings.Split(string(data), "\n")
		start := len(lines) - 20
		if start < 0 {
			start = 0
		}
		for _, l := range lines[start:] {
			if l != "" {
				fmt.Fprintln(os.Stderr, l)
			}
		}
	}
}

// setupDockerSocket configures the host Docker socket permissions.
func setupDockerSocket(cfg *config) {
	logInfo("Using host Docker socket")
	sock := "/var/run/docker.sock"

	info, err := os.Stat(sock)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return
	}

	// Try group-based access first.
	_ = exec.Command("chgrp", "docker", sock).Run()
	_ = exec.Command("chmod", "g+rw", sock).Run()

	// Verify the AI user can access it.
	check := exec.Command("su", "-s", "/bin/sh", "-c", fmt.Sprintf("test -w %s", sock), cfg.AIUsername)
	if check.Run() != nil {
		// Fallback: world-readable (safe — only exposed inside the container).
		_ = os.Chmod(sock, 0666)
	}

	if exec.Command("docker", "info").Run() == nil {
		logInfo("Host Docker daemon accessible")
	} else {
		logWarn("host Docker socket not accessible")
	}
}

// setupFirewall configures the Go forward proxy and iptables rules.
func setupFirewall(cfg *config) error {
	if _, err := os.Stat(cfg.FirewallConf); err != nil {
		return fmt.Errorf("firewall.conf not found: %s", cfg.FirewallConf)
	}

	logInfo("Applying network firewall...")

	// Parse the base whitelist.
	domains, err := parseWhitelistFile(cfg.FirewallConf)
	if err != nil {
		return fmt.Errorf("parsing firewall.conf: %w", err)
	}

	// Add MCP server domains.
	if cfg.MCP != "" {
		mcpDomains := resolveMCPDomains(cfg)
		if len(mcpDomains) > 0 {
			logInfo("MCP passthrough: added %d domain(s) to whitelist", len(mcpDomains))
			domains = append(domains, mcpDomains...)
		}
	}

	// Add extension-declared extra domains.
	domains = append(domains, cfg.FirewallExtra...)

	domains = dedup(domains)

	// Fork the proxy as a separate child process. It must run as root
	// (UID 0) so the iptables uid-owner rule allows its outbound connections.
	// A goroutine would be killed by the syscall.Exec privilege drop.
	if err := forkProxy(domains, cfg.Verbose); err != nil {
		return fmt.Errorf("starting proxy: %w", err)
	}

	// Wait for the child proxy to be listening.
	wl := newDomainWhitelist(domains)
	for i := 0; i < 40; i++ {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:3128", 100*time.Millisecond)
		if err == nil {
			conn.Close()
			logInfo("Proxy ready (%d domains whitelisted)", wl.count())
			break
		}
		if i == 39 {
			return fmt.Errorf("proxy failed to start on :3128")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Configure iptables to force all HTTP(S) through the proxy.
	if err := setupIPTables(cfg); err != nil {
		return fmt.Errorf("iptables setup: %w", err)
	}

	// Set proxy env vars — inherited by the AI user after privilege drop.
	os.Setenv("HTTP_PROXY", "http://127.0.0.1:3128")
	os.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	os.Setenv("http_proxy", "http://127.0.0.1:3128")
	os.Setenv("https_proxy", "http://127.0.0.1:3128")
	os.Setenv("NO_PROXY", "localhost,127.0.0.1,::1,host.docker.internal")
	os.Setenv("no_proxy", "localhost,127.0.0.1,::1,host.docker.internal")

	// Node 22+ native fetch ignores HTTP_PROXY by default.
	nodeOpts := os.Getenv("NODE_OPTIONS")
	if nodeOpts != "" {
		nodeOpts += " "
	}
	nodeOpts += "--use-env-proxy"
	os.Setenv("NODE_OPTIONS", nodeOpts)

	logInfo("Firewall active: outbound HTTP(S) restricted to whitelisted domains")
	return nil
}

// setupIPTables configures iptables/ip6tables to force traffic through the proxy.
// The proxy process itself runs as root, so we allow root to make direct connections.
func setupIPTables(cfg *config) error {
	for _, cmd := range []string{"iptables", "ip6tables"} {
		rules := [][]string{
			{cmd, "-F", "OUTPUT"},
			{cmd, "-P", "OUTPUT", "DROP"},
			{cmd, "-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
			{cmd, "-A", "OUTPUT", "-m", "state", "--state", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
			// DNS.
			{cmd, "-A", "OUTPUT", "-p", "udp", "--dport", "53", "-j", "ACCEPT"},
			{cmd, "-A", "OUTPUT", "-p", "tcp", "--dport", "53", "-j", "ACCEPT"},
			// Only root (proxy runs as root) may connect directly on 80/443.
			{cmd, "-A", "OUTPUT", "-m", "owner", "--uid-owner", "0", "-p", "tcp", "--dport", "443", "-j", "ACCEPT"},
			{cmd, "-A", "OUTPUT", "-m", "owner", "--uid-owner", "0", "-p", "tcp", "--dport", "80", "-j", "ACCEPT"},
			// SSH for git.
			{cmd, "-A", "OUTPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"},
		}

		for _, args := range rules {
			c := exec.Command(args[0], args[1:]...)
			if err := c.Run(); err != nil {
				// Non-fatal: ip6tables may not be available.
				if cmd == "iptables" {
					return fmt.Errorf("%s failed: %w", strings.Join(args, " "), err)
				}
			}
		}

		// Allow container to reach the host broker.
		if cfg.BrokerPort != "" {
			c := exec.Command(cmd, "-A", "OUTPUT", "-p", "tcp", "--dport", cfg.BrokerPort, "-j", "ACCEPT")
			if err := c.Run(); err != nil {
				logWarn("iptables ACCEPT rule for broker port %s failed: %v", cfg.BrokerPort, err)
			}
		}

		// Allow worker containers to reach Kitchen's WorkerBroker.
		if kitchenAddr := os.Getenv("MITTENS_KITCHEN_ADDR"); kitchenAddr != "" {
			if _, port, err := net.SplitHostPort(kitchenAddr); err == nil && port != "" {
				c := exec.Command(cmd, "-A", "OUTPUT", "-p", "tcp", "--dport", port, "-j", "ACCEPT")
				if err := c.Run(); err != nil {
					logWarn("iptables ACCEPT rule for Kitchen port %s failed: %v", port, err)
				}
			}
		}
	}
	return nil
}

// resolveMCPDomains resolves MCP server names to their API domains.
func resolveMCPDomains(cfg *config) []string {
	// Load domain mappings from built-in and user config files.
	mcpMap := make(map[string]string)
	for _, mapFile := range []string{
		"/etc/mittens/mcp-domains.conf",
		cfg.ConfigMount + "/" + cfg.AIConfigDir + "/mcp-domains.conf",
	} {
		loadMCPDomainMap(mapFile, mcpMap)
	}

	// Determine which servers to resolve.
	var serverList []string
	if cfg.MCP == "__all__" {
		// Collect all MCP server names from config files.
		claudeJSON := cfg.ConfigMount + "/" + cfg.AIPrefsFile
		serverList = append(serverList, extractMCPServerNames(claudeJSON, cfg.AIMCPServersKey)...)
		if cfg.HostWorkspace != "" {
			serverList = append(serverList, extractMCPServerNames(cfg.HostWorkspace+"/.mcp.json", "mcpServers")...)
		}
	} else {
		serverList = strings.Split(cfg.MCP, ",")
	}

	var domains []string
	for _, server := range serverList {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}

		// Check for SSE/HTTP URL in config files (user prefs, then workspace .mcp.json).
		resolved := extractMCPServerURL(cfg.ConfigMount+"/"+cfg.AIPrefsFile, cfg.AIMCPServersKey, server)
		if resolved == "" && cfg.HostWorkspace != "" {
			resolved = extractMCPServerURL(cfg.HostWorkspace+"/.mcp.json", "mcpServers", server)
		}

		// Fall back to lookup table.
		if resolved == "" {
			if mapped, ok := mcpMap[server]; ok {
				resolved = mapped
			}
		}

		if resolved != "" {
			for _, d := range strings.Split(resolved, ",") {
				d = strings.TrimSpace(d)
				if d != "" {
					domains = append(domains, d)
				}
			}
		} else {
			logWarn("no domain mapping for MCP server '%s'", server)
		}
	}
	return domains
}

// loadMCPDomainMap reads a name=domain1,domain2 mapping file into the map.
func loadMCPDomainMap(path string, m map[string]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		domains := strings.TrimSpace(parts[1])
		if name != "" && domains != "" {
			m[name] = domains
		}
	}
}

// extractMCPServerNames reads a JSON file and extracts MCP server names.
func extractMCPServerNames(path, key string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(raw, &servers) != nil {
		return nil
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	return names
}

// extractMCPServerURL extracts the URL from an MCP server config entry.
func extractMCPServerURL(path, key, server string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return ""
	}
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(raw, &servers) != nil {
		return ""
	}
	serverRaw, ok := servers[server]
	if !ok {
		return ""
	}
	var serverObj map[string]json.RawMessage
	if json.Unmarshal(serverRaw, &serverObj) != nil {
		return ""
	}
	urlRaw, ok := serverObj["url"]
	if !ok {
		return ""
	}
	var rawURL string
	if json.Unmarshal(urlRaw, &rawURL) != nil {
		return ""
	}
	return hostnameFromURL(rawURL)
}

// hostnameFromURL extracts the hostname from a URL string.
// Handles missing schemes, trailing paths, and port numbers.
func hostnameFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	// Add scheme if missing so url.Parse doesn't treat host as path.
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	if host == "" || host == "localhost" || net.ParseIP(host) != nil {
		return ""
	}
	return host
}

// dropPrivileges drops from root to the AI user and re-execs this binary.
func dropPrivileges(cfg *config) error {
	// Look up the target user.
	uid, gid, err := lookupUser(cfg.AIUsername)
	if err != nil {
		return fmt.Errorf("looking up user %s: %w", cfg.AIUsername, err)
	}

	// Set supplementary groups.
	if err := syscall.Setgroups(nil); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}

	// Set GID first (must happen before UID drop).
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid(%d): %w", gid, err)
	}

	// Set UID.
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid(%d): %w", uid, err)
	}

	// Set HOME for the new user.
	os.Setenv("HOME", cfg.AIHome)
	os.Setenv("USER", cfg.AIUsername)
	os.Setenv("LOGNAME", cfg.AIUsername)

	// Re-exec this binary (phase 2 will run as the new user).
	return syscall.Exec("/proc/self/exe", os.Args, os.Environ())
}

// lookupUser returns UID and GID for the given username by parsing /etc/passwd.
func lookupUser(username string) (uid, gid int, err error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 4 && fields[0] == username {
			var u, g int
			if _, err := fmt.Sscanf(fields[2], "%d", &u); err != nil {
				return 0, 0, fmt.Errorf("parsing UID: %w", err)
			}
			if _, err := fmt.Sscanf(fields[3], "%d", &g); err != nil {
				return 0, 0, fmt.Errorf("parsing GID: %w", err)
			}
			return u, g, nil
		}
	}
	return 0, 0, fmt.Errorf("user %s not found in /etc/passwd", username)
}
