package kubectl

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestSetupUnavailableContexts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kubectl fixture uses a POSIX shell")
	}
	for _, tt := range []struct {
		name    string
		args    []string
		failure string
		wantErr string
		mounted bool
	}{
		{name: "missing first selection", args: []string{"missing", "available"}, mounted: true},
		{name: "all selections missing", args: []string{"missing"}},
		{name: "available selection", args: []string{"available"}, mounted: true},
		{name: "no selections"},
		{name: "listing failure", args: []string{"available"}, failure: "list", wantErr: "get-contexts"},
		{name: "extraction failure", args: []string{"available"}, failure: "extract", wantErr: "extracting kubectl context"},
		{name: "merge failure", args: []string{"available"}, failure: "merge", wantErr: "merging kubeconfigs"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			staging := t.TempDir()
			calls := filepath.Join(bin, "calls")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$MITTENS_TEST_KUBECTL_CALLS"
case "$*" in
  'config get-contexts -o name')
    [ "$MITTENS_TEST_KUBECTL_FAILURE" != list ] || exit 1
    printf 'available\nunselected\n'
    ;;
  'config view --minify --flatten --context=available')
    [ "$MITTENS_TEST_KUBECTL_FAILURE" != extract ] || exit 1
    printf 'server: https://selected.example.com\n'
    ;;
  'config view --flatten')
    [ "$MITTENS_TEST_KUBECTL_FAILURE" != merge ] || exit 1
    [ "$KUBECONFIG" = "$MITTENS_TEST_KUBECTL_STAGING/available.yaml" ] || exit 2
    cat "$KUBECONFIG"
    ;;
  'config use-context available')
    [ "$KUBECONFIG" = "$MITTENS_TEST_KUBECTL_STAGING/config" ] || exit 2
    ;;
  *) exit 3 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MITTENS_TEST_KUBECTL_CALLS", calls)
			t.Setenv("MITTENS_TEST_KUBECTL_STAGING", staging)
			t.Setenv("MITTENS_TEST_KUBECTL_FAILURE", tt.failure)
			warnings, err := os.CreateTemp(t.TempDir(), "warnings")
			if err != nil {
				t.Fatal(err)
			}
			defer warnings.Close()
			var dockerArgs, firewall []string
			ctx := &registry.SetupContext{
				Extension: &registry.Extension{Args: tt.args}, StagingDir: staging,
				ContainerHome: "/home/test", DockerArgs: &dockerArgs, FirewallExtra: &firewall,
			}
			func() {
				stderr := os.Stderr
				os.Stderr = warnings
				defer func() { os.Stderr = stderr }()
				err = setup(ctx)
			}()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			warningText, err := os.ReadFile(warnings.Name())
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.args) > 0 && tt.args[0] == "missing" && !strings.Contains(string(warningText), `kubectl context "missing" is not available; skipping`) {
				t.Fatalf("missing context warning not emitted: %s", warningText)
			}
			if tt.mounted {
				wantArgs := []string{"-v", filepath.Join(staging, "config") + ":/home/test/.kube/config:ro"}
				if !reflect.DeepEqual(dockerArgs, wantArgs) || !reflect.DeepEqual(firewall, []string{"selected.example.com"}) {
					t.Fatalf("Docker args = %v, firewall = %v", dockerArgs, firewall)
				}
				log, err := os.ReadFile(calls)
				if err != nil || !strings.Contains(string(log), "config use-context available\n") {
					t.Fatalf("first available context was not selected: %s (%v)", log, err)
				}
			} else if len(dockerArgs) != 0 || len(firewall) != 0 {
				t.Fatalf("unexpected mount or firewall entries: %v, %v", dockerArgs, firewall)
			}
			if tt.name == "all selections missing" {
				log, err := os.ReadFile(calls)
				if err != nil || string(log) != "config get-contexts -o name\n" {
					t.Fatalf("empty selection must not extract or merge configs: %s (%v)", log, err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExtractUniqueHosts (via registry helper, replaces extractK8sServerHosts)
// ---------------------------------------------------------------------------

func TestExtractK8sServerHosts(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "kubeconfig")
	content := `apiVersion: v1
clusters:
- cluster:
    server: https://k8s.example.com:6443
    certificate-authority-data: LS0t...
  name: prod
- cluster:
    server: https://staging.k8s.internal:6443
  name: staging
- cluster:
    server: https://k8s.example.com:6443
  name: prod-dupe
contexts:
- context:
    cluster: prod
  name: prod-ctx
`
	os.WriteFile(f, []byte(content), 0644)

	hosts := registry.ExtractUniqueHosts(f, `server:\s*(https?://[^\s]+)`)

	// Should deduplicate k8s.example.com.
	want := map[string]bool{
		"k8s.example.com":      true,
		"staging.k8s.internal": true,
	}
	if len(hosts) != len(want) {
		t.Fatalf("got %v, want keys %v", hosts, want)
	}
	for _, h := range hosts {
		if !want[h] {
			t.Errorf("unexpected host %q", h)
		}
	}
}
