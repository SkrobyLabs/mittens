package main

import (
	"os"
	"path/filepath"
	"strings"
)

const wslBindMountPrefix = "/mnt/wsl/docker-desktop-bind-mounts/"

// resolveWSLBindMount resolves Docker Desktop bind-mount paths back to real
// WSL paths by consulting /proc/self/mountinfo.  Paths outside the
// /mnt/wsl/docker-desktop-bind-mounts/ tree are returned unchanged.
//
// Docker Desktop on WSL2 creates bind-mounts that proxy host filesystem
// paths through internal paths like:
//
//	/mnt/wsl/docker-desktop-bind-mounts/Ubuntu/<hash>
//
// These paths can appear in os.Getwd() and git rev-parse --show-toplevel,
// making workspace identification unstable (the hash can change across
// Docker Desktop restarts).  This function reverses the mapping using
// the kernel mount table.
func resolveWSLBindMount(path string) string {
	if !strings.HasPrefix(path, wslBindMountPrefix) {
		return path
	}

	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return path
	}

	return resolveWSLBindMountFrom(path, string(data))
}

// resolveWSLBindMountFrom is the testable core of resolveWSLBindMount.
func resolveWSLBindMountFrom(path, mountinfo string) string {
	// Find the mount entry whose mount point best matches path.
	var bestMountPoint, bestRoot, bestFsType, bestSource string

	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		mountPoint := unescapeMountInfo(fields[4])
		if !strings.HasPrefix(mountPoint, wslBindMountPrefix) {
			continue
		}

		// Must be an exact match or a prefix with path separator.
		if path != mountPoint && !strings.HasPrefix(path, mountPoint+"/") {
			continue
		}

		// Prefer the most specific (longest) mount point.
		if len(mountPoint) <= len(bestMountPoint) {
			continue
		}

		// Find " - fstype source " after the optional tagged fields.
		dashIdx := -1
		for i, f := range fields {
			if f == "-" {
				dashIdx = i
				break
			}
		}
		if dashIdx < 0 || dashIdx+2 >= len(fields) {
			continue
		}

		bestMountPoint = mountPoint
		bestRoot = unescapeMountInfo(fields[3])
		bestFsType = fields[dashIdx+1]
		bestSource = unescapeMountInfo(fields[dashIdx+2])
	}

	if bestMountPoint == "" {
		return path
	}

	// Compute the relative path from mount point to our target.
	rel := strings.TrimPrefix(path, bestMountPoint)

	switch bestFsType {
	case "9p":
		// drvfs mount: source is "C:\" (drive root), root is the subpath
		// within that drive.  WSL maps C:\ → /mnt/c, D:\ → /mnt/d, etc.
		if len(bestSource) >= 2 && bestSource[1] == ':' {
			drive := strings.ToLower(string(bestSource[0]))
			return filepath.Clean(filepath.Join("/mnt/"+drive, bestRoot, rel))
		}
	case "ext4":
		// WSL native filesystem: root is the absolute path within the
		// ext4 partition, which IS the real WSL path.
		return filepath.Clean(filepath.Join(bestRoot, rel))
	}

	return path
}

// unescapeMountInfo decodes octal escape sequences (\040, \011, \012, \134)
// used in /proc/self/mountinfo fields.
func unescapeMountInfo(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '7' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			val := (s[i+1]-'0')*64 + (s[i+2]-'0')*8 + (s[i+3]-'0')
			b.WriteByte(val)
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
