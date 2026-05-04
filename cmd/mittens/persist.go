package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

func persistContainerConfig(containerName, containerConfigDir, hostConfigDir string, files, dirs, globs []string, verbose bool) error {
	tmpDir, err := os.MkdirTemp("", "mittens-persist-config.*")
	if err != nil {
		return fmt.Errorf("create config snapshot dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	snapshotContents := filepath.Join(tmpDir, "config")
	if err := os.MkdirAll(snapshotContents, 0o755); err != nil {
		return fmt.Errorf("create config snapshot root: %w", err)
	}
	if err := exec.Command("docker", "cp", containerName+":"+containerConfigDir+"/.", snapshotContents).Run(); err != nil {
		return fmt.Errorf("snapshot container config: %w", err)
	}

	return persistConfigSnapshot(snapshotContents, hostConfigDir, files, dirs, globs, verbose)
}

func persistConfigSnapshot(snapshotDir, hostConfigDir string, files, dirs, globs []string, verbose bool) error {
	for _, rel := range files {
		src := filepath.Join(snapshotDir, rel)
		dst := filepath.Join(hostConfigDir, rel)
		if !fileutil.FileExists(src) {
			logVerbose(verbose, "Persist %s: not found in container", rel)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", rel, err)
		}
		if err := fileutil.CopyFile(src, dst); err != nil {
			return fmt.Errorf("persist file %s: %w", rel, err)
		}
		logInfo("Persisted %s", rel)
	}

	for _, rel := range dirs {
		src := filepath.Join(snapshotDir, rel)
		dst := filepath.Join(hostConfigDir, rel)
		if !fileutil.DirExists(src) {
			logVerbose(verbose, "Persist %s: not found in container", rel)
			continue
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("clear persisted dir %s: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", rel, err)
		}
		if err := fileutil.CopyDir(src, dst); err != nil {
			return fmt.Errorf("persist dir %s: %w", rel, err)
		}
		logInfo("Persisted %s", rel)
	}

	for _, pattern := range globs {
		matches, err := filepath.Glob(filepath.Join(snapshotDir, pattern))
		if err != nil {
			return fmt.Errorf("glob %s: %w", pattern, err)
		}
		if len(matches) == 0 {
			logVerbose(verbose, "Persist %s: not found in container", pattern)
			continue
		}
		for _, src := range matches {
			rel, err := filepath.Rel(snapshotDir, src)
			if err != nil {
				return fmt.Errorf("relative path for %s: %w", src, err)
			}
			dst := filepath.Join(hostConfigDir, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", rel, err)
			}
			if err := fileutil.CopyFile(src, dst); err != nil {
				return fmt.Errorf("persist file %s: %w", rel, err)
			}
			logInfo("Persisted %s", rel)
		}
	}

	return nil
}
