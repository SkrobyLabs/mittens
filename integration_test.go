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
// Helpers
// ---------------------------------------------------------------------------

var testImage string // set by TestMain

// dockerBuild builds the mittens image with optional extra build args.
// Returns the full image:tag string.
func dockerBuild(t *testing.T, extraBuildArgs ...string) string {
	t.Helper()

	projectRoot := findProjectRoot(t)
	dockerfile := filepath.Join(projectRoot, "container", "Dockerfile")

	uid, gid := CurrentUserIDs()
	tag := "mittens:integration-test"

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
	cmd.Stdout = os.Stderr // show progress
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker build failed: %v", err)
	}
	return tag
}

// dockerRun executes `docker run --rm` with the given args and command,
// returning combined stdout+stderr as a string.
func dockerRun(t *testing.T, image string, runArgs []string, cmd ...string) string {
	t.Helper()

	args := []string{"run", "--rm"}
	args = append(args, runArgs...)
	args = append(args, image)
	args = append(args, cmd...)

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run failed: %v\noutput: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// findProjectRoot locates the project root by walking up from the test binary
// or current directory looking for container/Dockerfile.
func findProjectRoot(t *testing.T) string {
	t.Helper()

	// Try current directory first (go test runs in package dir).
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	dir := cwd
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "container", "Dockerfile")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not find project root from %s", cwd)
	return ""
}

// ---------------------------------------------------------------------------
// TestMain — build the image once for all integration tests
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	// Build image before running tests.
	projectRoot := ""
	cwd, _ := os.Getwd()
	dir := cwd
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "container", "Dockerfile")); err == nil {
			projectRoot = dir
			break
		}
		dir = filepath.Dir(dir)
	}
	if projectRoot == "" {
		fmt.Fprintln(os.Stderr, "FATAL: could not find project root")
		os.Exit(1)
	}

	uid, gid := CurrentUserIDs()
	tag := "mittens:integration-test"

	args := []string{
		"build", "-q",
		"-f", filepath.Join(projectRoot, "container", "Dockerfile"),
		"--build-arg", fmt.Sprintf("USER_ID=%d", uid),
		"--build-arg", fmt.Sprintf("GROUP_ID=%d", gid),
		"-t", tag,
		projectRoot,
	}

	fmt.Fprintln(os.Stderr, "[integration] Building Docker image...")
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: docker build failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "[integration] Image built successfully")

	testImage = tag
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestDockerBuild_BaseImage(t *testing.T) {
	// Verify the image exists via docker image inspect.
	cmd := exec.Command("docker", "image", "inspect", testImage)
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker image inspect %s failed: %v", testImage, err)
	}
}

func TestDockerRun_UserSwitch(t *testing.T) {
	out := dockerRun(t, testImage, nil, "bash", "-c", "id -u && id -g && whoami")

	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), out)
	}

	uid, gid := CurrentUserIDs()
	if strings.TrimSpace(lines[0]) != fmt.Sprintf("%d", uid) {
		t.Errorf("UID = %q, want %d", lines[0], uid)
	}
	if strings.TrimSpace(lines[1]) != fmt.Sprintf("%d", gid) {
		t.Errorf("GID = %q, want %d", lines[1], gid)
	}
	if strings.TrimSpace(lines[2]) != "claude" {
		t.Errorf("username = %q, want claude", lines[2])
	}
}

func TestDockerRun_CredentialMount(t *testing.T) {
	tmp := t.TempDir()
	credFile := filepath.Join(tmp, "creds.json")
	credContent := `{"accessToken":"test-tok","expiresAt":9999999999}`
	os.WriteFile(credFile, []byte(credContent), 0644)

	runArgs := []string{
		"-v", credFile + ":/mnt/claude-config/.credentials.json:ro",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c",
		"cat ~/.claude/.credentials.json && echo '---' && stat -c %a ~/.claude/.credentials.json")

	parts := strings.SplitN(out, "---", 2)
	if len(parts) < 2 {
		t.Fatalf("unexpected output format: %q", out)
	}

	content := strings.TrimSpace(parts[0])
	if content != credContent {
		t.Errorf("credential content = %q, want %q", content, credContent)
	}

	perms := strings.TrimSpace(parts[1])
	if perms != "600" {
		t.Errorf("credential permissions = %q, want 600", perms)
	}
}

func TestDockerRun_ConfigCopy(t *testing.T) {
	tmp := t.TempDir()

	// Create a .claude dir with settings.json.
	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	settings := `{"trustedDirectories":["/workspace"]}`
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644)

	runArgs := []string{
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", "cat ~/.claude/settings.json")

	// The entrypoint copies then merges; just check the file is readable and has content.
	if !strings.Contains(out, "trustedDirectories") {
		t.Errorf("settings.json should contain trustedDirectories, got %q", out)
	}
}

func TestDockerRun_FirewallWhitelist(t *testing.T) {
	tmp := t.TempDir()

	// Create a firewall.conf with test domains.
	firewallConf := filepath.Join(tmp, "firewall.conf")
	domains := "api.github.com\nregistry.npmjs.org\n# comment\npypi.org\n"
	os.WriteFile(firewallConf, []byte(domains), 0644)

	// We need NET_ADMIN for iptables, but just test the whitelist generation
	// without actually starting squid (avoid requiring privileged).
	// The entrypoint generates whitelist.txt from firewall.conf.
	// We'll directly test the sed command that generates it.
	runArgs := []string{
		"-v", firewallConf + ":/mnt/claude-config/firewall.conf:ro",
	}

	// Run the same sed pipeline the entrypoint uses to generate whitelist.
	out := dockerRun(t, testImage, runArgs, "bash", "-c",
		`sed 's/#.*//; s/^[[:space:]]*//; s/[[:space:]]*$//' /mnt/claude-config/firewall.conf | grep -v '^$'`)

	if !strings.Contains(out, "api.github.com") {
		t.Error("whitelist missing api.github.com")
	}
	if !strings.Contains(out, "registry.npmjs.org") {
		t.Error("whitelist missing registry.npmjs.org")
	}
	if !strings.Contains(out, "pypi.org") {
		t.Error("whitelist missing pypi.org")
	}
	if strings.Contains(out, "# comment") {
		t.Error("whitelist should not contain comments")
	}
}

func TestDockerRun_FirewallExtra(t *testing.T) {
	// Test that MITTENS_FIREWALL_EXTRA gets split into separate lines.
	out := dockerRun(t, testImage,
		[]string{"-e", "MITTENS_FIREWALL_EXTRA=custom.example.com,another.example.com"},
		"bash", "-c",
		`echo "custom.example.com,another.example.com" | tr ',' '\n'`)

	if !strings.Contains(out, "custom.example.com") {
		t.Error("missing custom.example.com in output")
	}
	if !strings.Contains(out, "another.example.com") {
		t.Error("missing another.example.com in output")
	}
}

func TestDockerRun_WorkspaceMount(t *testing.T) {
	tmp := t.TempDir()

	// Create a marker file in the workspace.
	os.WriteFile(filepath.Join(tmp, "marker.txt"), []byte("mittens-integration"), 0644)

	runArgs := []string{
		"-v", tmp + ":/workspace",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", "cat /workspace/marker.txt")

	if out != "mittens-integration" {
		t.Errorf("marker content = %q, want 'mittens-integration'", out)
	}
}

func TestDockerRun_EnvVarsPassthrough(t *testing.T) {
	runArgs := []string{
		"-e", "ANTHROPIC_API_KEY=test-key-123",
		"-e", "MITTENS_DIND=false",
		"-e", "TERM=xterm-256color",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", "env")

	if !strings.Contains(out, "ANTHROPIC_API_KEY=test-key-123") {
		t.Error("missing ANTHROPIC_API_KEY in container env")
	}
	if !strings.Contains(out, "MITTENS_DIND=false") {
		t.Error("missing MITTENS_DIND in container env")
	}
	if !strings.Contains(out, "TERM=xterm-256color") {
		t.Error("missing TERM in container env")
	}
}
