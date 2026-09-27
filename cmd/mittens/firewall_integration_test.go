//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/internal/initcfg"
)

// Firewall fixtures must be visible to the Docker daemon. When these tests run
// inside Mittens against an external daemon, set TMPDIR to a persistent workspace
// directory so both t.TempDir and TestMain's config mounts resolve on the host.
type firewallIntegrationFixture struct {
	prefix       string
	network      string
	configPath   string
	firewallPath string
}

func firewallDockerCommand(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("docker %s: %w", strings.Join(args, " "), ctx.Err())
	}
	return string(out), err
}

func newFirewallIntegrationFixture(t *testing.T) firewallIntegrationFixture {
	t.Helper()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fixture := firewallIntegrationFixture{
		prefix:       fmt.Sprintf("mittens-firewall-it-%x", suffix),
		configPath:   filepath.Join(dir, "config.json"),
		firewallPath: filepath.Join(dir, "firewall.conf"),
	}
	fixture.network = fixture.prefix + "-net"
	cfg := defaultContainerConfig()
	cfg.Flags.Firewall = true
	cfg.Flags.NoSSHEgress = true
	cfg.Flags.NoNotify = true
	cfg.ContainerName = fixture.prefix
	if err := cfg.Write(fixture.configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.firewallPath, []byte("allowed.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Register before creation so a failed or timed-out command still cleans up
	// anything it created. Later container cleanups run before this network cleanup.
	t.Cleanup(func() {
		out, err := firewallDockerCommand(15*time.Second, "network", "rm", fixture.network)
		if err != nil && !strings.Contains(out, "not found") {
			t.Errorf("remove firewall test network: %v\n%s", err, out)
		}
	})
	if out, err := firewallDockerCommand(30*time.Second, "network", "create", "--internal", "--driver", "bridge", fixture.network); err != nil {
		t.Fatalf("create firewall test network: %v\n%s", err, out)
	}
	return fixture
}

func cleanupFirewallContainer(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		out, err := firewallDockerCommand(15*time.Second, "rm", "-f", name)
		if err != nil && !strings.Contains(out, "No such container") {
			t.Errorf("remove firewall test container %s: %v\n%s", name, err, out)
		}
	})
}

func (f firewallIntegrationFixture) runArgs(t *testing.T, firewallMount, netAdmin bool) []string {
	t.Helper()
	name := f.prefix + "-client"
	cleanupFirewallContainer(t, name)
	args := []string{
		"run", "--rm", "--name", name, "--network", f.network,
		"--mount", "type=bind,src=" + f.configPath + ",dst=" + initcfg.ConfigPath + ",readonly",
	}
	if firewallMount {
		args = append(args, "--mount", "type=bind,src="+f.firewallPath+",dst=/mnt/mittens-staging/firewall.conf,readonly")
	}
	if netAdmin {
		args = append(args, "--cap-add", "NET_ADMIN")
	} else {
		args = append(args, "--cap-drop", "NET_ADMIN")
	}
	return args
}

func TestDockerRun_FirewallEnforcement(t *testing.T) {
	fixture := newFirewallIntegrationFixture(t)
	server := fixture.prefix + "-http"
	cleanupFirewallContainer(t, server)
	// No published ports or external connectivity: both names resolve to the
	// same local fixture, so allowlist behavior is independent of the Internet.
	if out, err := firewallDockerCommand(40*time.Second,
		"run", "-d", "--name", server, "--network", fixture.network,
		"--network-alias", "allowed.test", "--network-alias", "denied.test",
		"--entrypoint", "python3", testImage,
		"-m", "http.server", "80", "--bind", "0.0.0.0", "--directory", "/tmp",
	); err != nil {
		t.Fatalf("start HTTP fixture: %v\n%s", err, out)
	}
	var ready bool
	var readinessOutput string
	for attempt := 0; attempt < 20; attempt++ {
		out, err := firewallDockerCommand(3*time.Second, "exec", server, "python3", "-c",
			`import urllib.request; urllib.request.urlopen("http://127.0.0.1", timeout=1)`)
		if err == nil {
			ready = true
			break
		}
		readinessOutput = fmt.Sprintf("%v\n%s", err, out)
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("HTTP fixture did not become ready: %s", readinessOutput)
	}

	args := fixture.runArgs(t, true, true)
	args = append(args, testImage, "bash", "-ec", `
test "$(id -u)" -ne 0
test "$HTTP_PROXY" = http://127.0.0.1:3128
allowed=$(curl -sS --max-time 5 --noproxy "" --proxy "$HTTP_PROXY" -o /dev/null -w "%{http_code}" http://allowed.test/)
echo "Allowed HTTP status: $allowed"
test "$allowed" = 200
denied=$(curl -sS --max-time 5 --noproxy "" --proxy "$HTTP_PROXY" -o /dev/null -w "%{http_code}" http://denied.test/)
echo "Denied HTTP status: $denied"
test "$denied" = 403
rc=0
curl -sS --connect-timeout 2 --max-time 3 --noproxy "*" http://allowed.test/ -o /dev/null || rc=$?
echo "Direct egress curl exit: $rc"
test "$rc" -eq 28
echo FIREWALL_ENFORCEMENT_PASSED
`)
	out, err := firewallDockerCommand(40*time.Second, args...)
	if err != nil || !strings.Contains(out, "FIREWALL_ENFORCEMENT_PASSED") {
		t.Fatalf("firewall enforcement smoke failed: %v\n%s", err, out)
	}
}

func TestDockerRun_FirewallStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firewallMount bool
		netAdmin      bool
		wantError     string
	}{
		{name: "missing firewall configuration", netAdmin: true, wantError: "firewall.conf not found"},
		{name: "missing NET_ADMIN", firewallMount: true, wantError: "iptables setup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFirewallIntegrationFixture(t)
			args := fixture.runArgs(t, tc.firewallMount, tc.netAdmin)
			args = append(args, testImage, "bash", "-c", "echo AGENT_STARTED")
			out, err := firewallDockerCommand(40*time.Second, args...)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("expected entrypoint exit 1, got %v\n%s", err, out)
			}
			if !strings.Contains(out, "firewall setup failed; refusing to start agent") || !strings.Contains(out, tc.wantError) {
				t.Fatalf("missing firewall failure reason %q:\n%s", tc.wantError, out)
			}
			if strings.Contains(out, "AGENT_STARTED") {
				t.Fatalf("agent command ran after firewall setup failed:\n%s", out)
			}
		})
	}
}
