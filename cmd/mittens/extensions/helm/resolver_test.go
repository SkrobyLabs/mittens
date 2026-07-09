package helm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestSetupAddsReposAndHelmfileDomains(t *testing.T) {
	home := t.TempDir()
	helmConfig := filepath.Join(home, ".config", "helm")
	if err := os.MkdirAll(helmConfig, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helmConfig, "repositories.yaml"), []byte(`
repositories:
  - name: bitnami
    url: https://charts.bitnami.com/bitnami
`), 0644); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "helmfile.yaml"), []byte(`
repositories:
  - name: jetstack
    url: https://charts.jetstack.io
  - name: duplicate
    url: https://charts.bitnami.com/bitnami
`), 0644); err != nil {
		t.Fatal(err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	var firewall []string
	ctx := &registry.SetupContext{
		Home:          home,
		FirewallExtra: &firewall,
	}
	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	want := []string{"charts.bitnami.com", "charts.jetstack.io"}
	if !reflect.DeepEqual(firewall, want) {
		t.Fatalf("firewall = %#v, want %#v", firewall, want)
	}
}

func TestHelmfileCandidates(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"helmfile.yaml", "helmfile-production.yaml", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"helmfile.d", "helmfiles"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "test.yaml.gotmpl"), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := helmfileCandidates(root)
	want := []string{
		filepath.Join(root, "helmfile.yaml"),
		filepath.Join(root, "helmfile.yaml.gotmpl"),
		filepath.Join(root, "helmfile-production.yaml"),
		filepath.Join(root, "helmfile.d", "test.yaml.gotmpl"),
		filepath.Join(root, "helmfiles", "test.yaml.gotmpl"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helmfileCandidates = %#v, want %#v", got, want)
	}
}
