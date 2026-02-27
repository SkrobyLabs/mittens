package kubectl

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// extractK8sServerHosts
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

	hosts := extractK8sServerHosts(f)

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

func TestExtractK8sServerHosts_NoServers(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "empty-kubeconfig")
	os.WriteFile(f, []byte("apiVersion: v1\nclusters: []\n"), 0644)

	hosts := extractK8sServerHosts(f)
	if len(hosts) != 0 {
		t.Errorf("expected empty, got %v", hosts)
	}
}

func TestExtractK8sServerHosts_MissingFile(t *testing.T) {
	hosts := extractK8sServerHosts("/nonexistent/kubeconfig")
	if hosts != nil {
		t.Errorf("expected nil for missing file, got %v", hosts)
	}
}

func TestExtractK8sServerHosts_IPAddress(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "kubeconfig")
	content := `apiVersion: v1
clusters:
- cluster:
    server: https://192.168.1.100:6443
  name: local
`
	os.WriteFile(f, []byte(content), 0644)

	hosts := extractK8sServerHosts(f)
	if len(hosts) != 1 || hosts[0] != "192.168.1.100" {
		t.Errorf("got %v, want [192.168.1.100]", hosts)
	}
}
