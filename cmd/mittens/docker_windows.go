//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ensureBaseImages reads the Dockerfile, extracts all FROM images, and pulls
// any that are not available locally. This works around Docker Desktop / WSL2
// environments where BuildKit cannot execute the credential helper
// (docker-credential-desktop.exe) but `docker pull` from the host CLI works.
func ensureBaseImages(dockerfile string) {
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		return
	}

	// Collect named build stages so we can skip "FROM stagename" references.
	stages := map[string]bool{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "FROM ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// Record "AS <name>" aliases for later FROM references.
		for i, p := range parts {
			if strings.EqualFold(p, "AS") && i+1 < len(parts) {
				stages[strings.ToLower(parts[i+1])] = true
			}
		}

		image := parts[1]

		// Skip build-arg references (e.g. FROM ${BASE_IMAGE})
		if strings.Contains(image, "$") {
			continue
		}
		// Skip references to earlier build stages (e.g. FROM builder)
		if stages[strings.ToLower(image)] {
			continue
		}
		// Already available locally — nothing to do
		if exec.Command("docker", "image", "inspect", image).Run() == nil {
			continue
		}

		fmt.Printf("[mittens] Pulling base image: %s\n", image)
		cmd := exec.Command("docker", "pull", image)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run() // best-effort; build will report its own error if still missing
	}
}

func init() {
	platformCurrentUserIDs = func() (int, int) {
		return 1000, 1000
	}

	platformPreBuildHook = func(dockerfile string) {
		ensureBaseImages(dockerfile)
	}
}
