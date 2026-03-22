package kubectl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

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
