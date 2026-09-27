package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestPolicyRejectsHostFirewall(t *testing.T) {
	for _, legacyHost := range []bool{false, true} {
		for _, firewall := range []string{"", "strict", "dev", "custom", "disabled"} {
			p := defaultProjectPolicy()
			if legacyHost {
				p.Execution.NetworkHost = true
			} else {
				p.Network.Mode = "host"
			}
			p.Network.Firewall = firewall
			p.Network.CustomConfig = "/tmp/allowlist.conf"
			err := p.Validate()
			if firewall == "disabled" {
				if err != nil {
					t.Fatalf("host with disabled firewall: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "host networking cannot be combined") {
				t.Fatalf("legacyHost=%v firewall=%q: got %v, want host firewall rejection", legacyHost, firewall, err)
			}
		}
	}
	if err := defaultProjectPolicy().Validate(); err != nil {
		t.Fatalf("default bridge firewall must remain valid: %v", err)
	}
}

func TestHostFirewallRejectedOnPolicyLoad(t *testing.T) {
	t.Setenv("MITTENS_HOME", t.TempDir())
	for _, path := range []string{projectPolicyPath("/repo/app"), UserDefaultsPolicyPath()} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// An omitted firewall defaults to strict, including in user defaults.
		if err := os.WriteFile(path, []byte("version: 2\nnetwork:\n  mode: host\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := LoadProjectPolicy("/repo/app", nil); err == nil || !strings.Contains(err.Error(), "host networking") {
		t.Fatalf("project load: %v, want host firewall rejection", err)
	}
	if _, _, err := LoadUserDefaultsPolicy(nil); err == nil || !strings.Contains(err.Error(), "host networking") {
		t.Fatalf("defaults load: %v, want host firewall rejection", err)
	}
	if _, err := PolicyFromLegacyFlags([]string{"--network-host"}, nil); err == nil || !strings.Contains(err.Error(), "host networking") {
		t.Fatalf("legacy conversion: %v, want host firewall rejection", err)
	}
}

func TestValidateNetworkOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     bool
		firewall bool
		learn    bool
		wantErr  bool
	}{
		{name: "bridge firewall", firewall: true},
		{name: "bridge learn", firewall: true, learn: true},
		{name: "host unrestricted", host: true},
		{name: "host firewall", host: true, firewall: true, wantErr: true},
		{name: "host learn", host: true, learn: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{NetworkHost: tc.host, FirewallLearn: tc.learn, Extensions: []*registry.Extension{
				nil, {Name: "firewall", Enabled: tc.firewall},
			}}
			if err := a.validateNetworkOptions(); (err != nil) != tc.wantErr {
				t.Fatalf("validateNetworkOptions() = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestRunRejectsHostFirewallAfterRuntimeOverrides(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cli      []string
		armLearn bool
		defaults bool
	}{
		// The public CLI rejects --network-host, but direct ParseFlags callers
		// and legacy embeddings must also be checked before Docker is invoked.
		{name: "legacy runtime host override", cli: []string{"--network-host"}},
		{name: "explicit learn", cli: []string{"--firewall-learn"}},
		{name: "armed learn", armLearn: true},
		{name: "defaults and explicit learn", defaults: true, cli: []string{"--firewall-learn"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := t.TempDir()
			workspace := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("MITTENS_HOME", filepath.Join(home, ".mittens"))
			t.Setenv("MITTENS_WSL_CWD", workspace)
			// No real Docker or git process can run with this isolated PATH.
			t.Setenv("PATH", bin)
			marker := filepath.Join(home, "docker-invoked")
			t.Setenv("MITTENS_TEST_DOCKER_MARKER", marker)
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf invoked > \"$MITTENS_TEST_DOCKER_MARKER\"\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			p := defaultProjectPolicy()
			if tc.name != "legacy runtime host override" {
				p.Network.Mode = "host"
				p.Network.Firewall = "disabled"
			}
			a := &App{Provider: DefaultProvider(), Extensions: []*registry.Extension{
				{Name: "firewall", Enabled: true, DefaultOn: true},
			}}
			var defaults *ProjectPolicy
			if tc.defaults {
				defaults, p = p, nil
			}
			if err := a.applyLaunchConfig(p, defaults, tc.cli); err != nil {
				t.Fatal(err)
			}
			if tc.armLearn {
				if err := armLearnPass(workspace); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Run(); err == nil || !strings.Contains(err.Error(), "host networking cannot be combined") {
				t.Fatalf("Run() = %v, want host firewall rejection", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("Docker must not run before network rejection: %v", err)
			}
		})
	}
}

func TestAssembleDockerArgsRejectsHostFirewall(t *testing.T) {
	for _, learn := range []bool{false, true} {
		a := &App{NetworkHost: true, FirewallLearn: learn, Extensions: []*registry.Extension{
			{Name: "firewall", Enabled: !learn},
		}}
		args, err := a.assembleDockerArgsE(nil, nil)
		if err == nil || !strings.Contains(err.Error(), "host networking cannot be combined") || args != nil {
			t.Fatalf("learn=%v: assembleDockerArgsE() = %v, %v; want no Docker args and host firewall rejection", learn, args, err)
		}
	}
}
