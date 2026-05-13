package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRootUsesExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(runtimeRootEnv, dir)

	if got := runtimeRoot(); got != dir {
		t.Fatalf("runtimeRoot() = %q, want override %q", got, dir)
	}
}

func TestRuntimeCacheKeySanitizesVersionMetadata(t *testing.T) {
	withVersionMetadata(t, "v1.2.3/dirty", "abc:def", "2026-05-05T00:00:00Z")

	if got, want := runtimeCacheKey(), "v1.2.3-dirty-abc-def"; got != want {
		t.Fatalf("runtimeCacheKey() = %q, want %q", got, want)
	}
}

func TestMaterializeRuntimeAssetsWritesRequiredFiles(t *testing.T) {
	root := t.TempDir()
	if err := materializeRuntimeAssets(root); err != nil {
		t.Fatalf("materializeRuntimeAssets() error: %v", err)
	}

	required := []string{
		filepath.Join("container", "Dockerfile"),
		filepath.Join("container", "firewall.conf"),
		filepath.Join("container", "mittens-init"),
		filepath.Join("extensions", "dotnet", "build.sh"),
		filepath.Join("extensions", "python", "extension.yaml"),
	}
	for _, rel := range required {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("materialized asset %s missing: %v", rel, err)
		}
	}

	info, err := os.Stat(filepath.Join(root, "container", "mittens-init"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("container/mittens-init mode = %v, want executable", info.Mode())
	}
}

func TestContainerDockerfileInstallsBuildxPlugin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("container", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "docker-buildx-plugin") {
		t.Fatal("container Dockerfile must install docker-buildx-plugin so docker buildx is available inside Docker-enabled agents")
	}
}
