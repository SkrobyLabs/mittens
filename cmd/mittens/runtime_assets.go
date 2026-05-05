package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const runtimeRootEnv = "MITTENS_RUNTIME_ROOT"

// embeddedRuntimeRoot materializes bundled runtime files into a versioned cache
// so installed binaries do not depend on adjacent source-tree assets.
func embeddedRuntimeRoot() (string, error) {
	root := filepath.Join(homeDir(), ".mittens", "runtime", runtimeCacheKey())
	if err := materializeRuntimeAssets(root); err != nil {
		return "", err
	}
	return root, nil
}

func runtimeCacheKey() string {
	v := version
	if strings.TrimSpace(v) == "" {
		v = "dev"
	}
	c := commit
	if strings.TrimSpace(c) == "" {
		c = "unknown"
	}
	return sanitizeRuntimeKey(v + "-" + c)
}

func sanitizeRuntimeKey(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "dev-unknown"
	}
	return out
}

func materializeRuntimeAssets(root string) error {
	return fs.WalkDir(runtimeAssets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}

		target := filepath.Join(root, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		data, err := runtimeAssets.ReadFile(path)
		if err != nil {
			return err
		}
		mode := runtimeAssetMode(path)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if sameFileBytes(target, data) {
			return os.Chmod(target, mode)
		}
		tmp := target + ".tmp"
		if err := os.WriteFile(tmp, data, mode); err != nil {
			return err
		}
		if err := os.Chmod(tmp, mode); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("installing runtime asset %s: %w", path, err)
		}
		return nil
	})
}

func runtimeAssetMode(path string) fs.FileMode {
	base := filepath.Base(path)
	if base == "mittens-init" || strings.HasSuffix(base, ".sh") {
		return 0755
	}
	return 0644
}

func sameFileBytes(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	return err == nil && bytes.Equal(got, want)
}
