//go:build integration

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Comprehensive container assertions.
//
// These complement integration_test.go (which focuses on the entrypoint's
// config/credential/firewall behavior) by asserting the *image contents*: that
// the base image carries the tools we expect, that pinned versions are honored,
// and that the size-trimming gates (Docker CE and the X11 stack) keep their
// payloads out of images that do not ask for them.
//
// The base image is built once by TestMain in integration_test.go and shared
// via the package-level testImage. Tests that need an image built with extra
// build args build their own tagged variant; those are skipped in -short mode
// because the extra apt/source installs are slow.
// ---------------------------------------------------------------------------

// dockerBuildVariant builds a mittens image to a caller-supplied tag with the
// given extra build args, so version/extension matrices do not clobber the
// shared base image tag.
func dockerBuildVariant(t *testing.T, tag string, extraBuildArgs ...string) string {
	t.Helper()

	projectRoot := findProjectRoot(t)
	dockerfile := filepath.Join(projectRoot, "container", "Dockerfile")
	uid, gid := CurrentUserIDs()

	args := []string{
		"build", "-q",
		"-f", dockerfile,
		"--build-arg", fmt.Sprintf("USER_ID=%d", uid),
		"--build-arg", fmt.Sprintf("GROUP_ID=%d", gid),
		"-t", tag,
	}
	args = append(args, extraBuildArgs...)
	args = append(args, projectRoot)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stderr // surface build progress
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker build (%s) failed: %v", tag, err)
	}
	return tag
}

// ---------------------------------------------------------------------------
// Base image contents
// ---------------------------------------------------------------------------

// TestContainer_CoreTooling verifies the developer tools we install in the base
// image are all present and on PATH for the unprivileged user.
func TestContainer_CoreTooling(t *testing.T) {
	// Tools expected on the user's PATH.
	tools := []string{
		"git", "curl", "jq", "less", "vim",
		"rg", "fdfind", "yq", "gh",
		"node", "npm", "python3", "bash", "unzip", "ssh",
		"claude",
	}

	script := `
for t in ` + strings.Join(tools, " ") + `; do
    if command -v "$t" >/dev/null 2>&1; then echo "OK $t"; else echo "MISSING $t"; fi
done
# iptables lives in /usr/sbin, which is not always on a non-root PATH.
if command -v iptables >/dev/null 2>&1 || [ -x /usr/sbin/iptables ]; then echo "OK iptables"; else echo "MISSING iptables"; fi
`

	out := dockerRun(t, testImage, nil, "bash", "-c", script)

	for _, tool := range append(tools, "iptables") {
		if !strings.Contains(out, "OK "+tool) {
			t.Errorf("expected tool %q present in base image\noutput:\n%s", tool, out)
		}
	}
}

// TestContainer_YqPinnedVersion verifies yq is the pinned version rather than a
// floating "latest" download.
func TestContainer_YqPinnedVersion(t *testing.T) {
	const want = "4.53.3"
	out := dockerRun(t, testImage, nil, "bash", "-c", "yq --version")
	if !strings.Contains(out, want) {
		t.Errorf("expected yq %s, got: %s", want, out)
	}
}

// ---------------------------------------------------------------------------
// Trimming gates: payloads that must NOT be in the base image
// ---------------------------------------------------------------------------

// TestContainer_DockerAbsentFromBase asserts Docker CE is no longer baked into
// the base image — it is now installed only by the docker extension.
func TestContainer_DockerAbsentFromBase(t *testing.T) {
	out := dockerRun(t, testImage, nil, "bash", "-c",
		`if command -v docker >/dev/null 2>&1 || command -v dockerd >/dev/null 2>&1; then echo HAS_DOCKER; else echo NO_DOCKER; fi`)
	if !strings.Contains(out, "NO_DOCKER") {
		t.Errorf("Docker CE should not be present in the base image (it belongs to the docker extension): %s", out)
	}
}

// TestContainer_X11AbsentFromBase asserts the X11 clipboard stack is not present
// in the default (claude) image — it is installed only when a provider opts in
// via INSTALL_X11.
func TestContainer_X11AbsentFromBase(t *testing.T) {
	out := dockerRun(t, testImage, nil, "bash", "-c",
		`if command -v Xvfb >/dev/null 2>&1 || [ -e /usr/local/bin/xclip-real ]; then echo HAS_X11; else echo NO_X11; fi`)
	if !strings.Contains(out, "NO_X11") {
		t.Errorf("X11 stack (xvfb/xclip-real) should not be in the base claude image: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Opt-in payloads: build with the gate enabled and confirm it installs
// ---------------------------------------------------------------------------

// TestContainer_DockerExtensionInstalls verifies the docker extension installs
// a working Docker CLI + daemon when enabled.
func TestContainer_DockerExtensionInstalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow docker-extension build in -short mode")
	}
	img := dockerBuildVariant(t, "mittens:it-docker",
		"--build-arg", "INSTALL_EXTENSIONS=docker")

	out := dockerRun(t, img, nil, "bash", "-c",
		`docker --version && command -v dockerd >/dev/null 2>&1 && echo DOCKERD_OK`)
	if !strings.Contains(out, "Docker version") {
		t.Errorf("docker CLI should be installed by the docker extension: %s", out)
	}
	if !strings.Contains(out, "DOCKERD_OK") {
		t.Errorf("dockerd should be installed by the docker extension: %s", out)
	}
}

// TestContainer_GoExtensionPinnedVersion verifies the go extension installs the
// exact pinned patch version (1.26.4) rather than resolving a bare minor.
func TestContainer_GoExtensionPinnedVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow go-extension build in -short mode")
	}
	img := dockerBuildVariant(t, "mittens:it-go",
		"--build-arg", "INSTALL_EXTENSIONS=go",
		"--build-arg", "GO_VERSION=1.26.4")

	out := dockerRun(t, img, nil, "bash", "-c", "/usr/local/go/bin/go version")
	if !strings.Contains(out, "go1.26.4") {
		t.Errorf("expected pinned go1.26.4, got: %s", out)
	}
}

// TestContainer_X11BuildInstallsStack verifies that opting into INSTALL_X11
// installs Xvfb and the saved real xclip binary the clipboard bridge needs.
func TestContainer_X11BuildInstallsStack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow x11 build in -short mode")
	}
	img := dockerBuildVariant(t, "mittens:it-x11",
		"--build-arg", "INSTALL_X11=true")

	out := dockerRun(t, img, nil, "bash", "-c",
		`command -v Xvfb >/dev/null 2>&1 && [ -x /usr/local/bin/xclip-real ] && echo X11_OK`)
	if !strings.Contains(out, "X11_OK") {
		t.Errorf("INSTALL_X11=true should install Xvfb and xclip-real: %s", out)
	}
}

// TestContainer_PythonExtensionVersion verifies the python extension builds the
// requested minor (resolving to its latest patch). This compiles CPython from
// source and takes several minutes, so it is gated behind an explicit env var
// in addition to -short.
func TestContainer_PythonExtensionVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping python source build in -short mode")
	}
	if os.Getenv("MITTENS_TEST_PYTHON") == "" {
		t.Skip("set MITTENS_TEST_PYTHON=1 to run the slow CPython source build")
	}
	img := dockerBuildVariant(t, "mittens:it-python",
		"--build-arg", "INSTALL_EXTENSIONS=python",
		"--build-arg", "PYTHON_VERSION=3.14")

	out := dockerRun(t, img, nil, "bash", "-c", "/usr/local/python3.14/bin/python3 --version")
	if !strings.Contains(out, "Python 3.14") {
		t.Errorf("expected Python 3.14.x, got: %s", out)
	}
}
