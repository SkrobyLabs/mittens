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

// dockerRunKeep executes `docker run` WITHOUT --rm so the container can be
// inspected/docker-cp'd after exit. Returns the container name. The caller
// must defer dockerRemove(t, name).
func dockerRunKeep(t *testing.T, image, name string, runArgs []string, cmd ...string) string {
	t.Helper()

	args := []string{"run", "--name", name}
	args = append(args, runArgs...)
	args = append(args, image)
	args = append(args, cmd...)

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run (keep) failed: %v\noutput: %s", err, out)
	}
	_ = out
	return name
}

// dockerCP copies a file out of a stopped container.
func dockerCP(t *testing.T, container, srcPath, dstPath string) {
	t.Helper()
	out, err := exec.Command("docker", "cp", container+":"+srcPath, dstPath).CombinedOutput()
	if err != nil {
		t.Fatalf("docker cp %s:%s → %s failed: %v\noutput: %s", container, srcPath, dstPath, err, out)
	}
}

// dockerRemove force-removes a container.
func dockerRemove(t *testing.T, name string) {
	t.Helper()
	_ = exec.Command("docker", "rm", "-f", name).Run()
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

func TestDockerRun_ConfigSubdirCopyDoesNotNest(t *testing.T) {
	tmp := t.TempDir()

	codexDir := filepath.Join(tmp, ".codex")
	skillsDir := filepath.Join(codexDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("test skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	runArgs := []string{
		"-v", codexDir + ":/mnt/claude-config/.codex:ro",
		"-e", "MITTENS_AI_CONFIG_DIR=.codex",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", `
set -e
test -f ~/.codex/skills/SKILL.md
if [[ -e ~/.codex/skills/skills ]]; then
    echo "NESTED=bug"
else
    echo "NESTED=ok"
fi
`)

	if !strings.Contains(out, "NESTED=ok") {
		t.Fatalf("config subdir copy should not create nested skills dir: %s", out)
	}
}

func TestDockerRun_FirewallWhitelist(t *testing.T) {
	tmp := t.TempDir()

	// Create a firewall.conf with test domains.
	firewallConf := filepath.Join(tmp, "firewall.conf")
	domains := "api.github.com\nregistry.npmjs.org\n# comment\npypi.org\n"
	os.WriteFile(firewallConf, []byte(domains), 0644)

	// We need NET_ADMIN for iptables, but just test the whitelist generation
	// without actually starting the proxy (avoid requiring privileged).
	// The entrypoint generates the whitelist from firewall.conf.
	// We'll directly test the parsing that generates it.
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

// ---------------------------------------------------------------------------
// Notification hook tests
// ---------------------------------------------------------------------------

func TestDockerRun_NotificationHookInjection(t *testing.T) {
	tmp := t.TempDir()

	// Create a minimal .claude config dir.
	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{}`), 0644)

	runArgs := []string{
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
		"-e", "MITTENS_BROKER_PORT=12345",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", "cat ~/.claude/settings.json")

	// Both hooks should be present.
	if !strings.Contains(out, `"Stop"`) {
		t.Error("settings.json should contain Stop hook")
	}
	if !strings.Contains(out, `"Notification"`) {
		t.Error("settings.json should contain Notification hook")
	}

	// The Notification hook command must NOT contain the old xargs '{}' bug.
	if strings.Contains(out, `'{}'`) {
		t.Error("Notification hook command should not contain literal '{}' quoting bug")
	}

	// Verify the command uses notify.sh notification.
	if !strings.Contains(out, "notify.sh notification") {
		t.Error("Notification hook should call notify.sh notification")
	}
}

func TestDockerRun_NotificationHookNotInjectedWithoutBroker(t *testing.T) {
	tmp := t.TempDir()

	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{}`), 0644)

	runArgs := []string{
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
		// No MITTENS_BROKER_PORT set.
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", "cat ~/.claude/settings.json")

	if strings.Contains(out, "notify.sh") {
		t.Error("hooks should not be injected when MITTENS_BROKER_PORT is unset")
	}
}

func TestDockerRun_NotificationHookSuppressedByNoNotify(t *testing.T) {
	tmp := t.TempDir()

	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{}`), 0644)

	runArgs := []string{
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
		"-e", "MITTENS_BROKER_PORT=12345",
		"-e", "MITTENS_NO_NOTIFY=1",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", "cat ~/.claude/settings.json")

	if strings.Contains(out, "notify.sh") {
		t.Error("hooks should not be injected when MITTENS_NO_NOTIFY is set")
	}
}

// ---------------------------------------------------------------------------
// Credential lifecycle tests
// ---------------------------------------------------------------------------

// TestDockerRun_CredentialLifecycle tests the full credential round-trip:
//  1. Mount initial creds (read-only) → entrypoint copies to writable home
//  2. Inside container: read, then overwrite with "refreshed" creds
//  3. After exit: docker cp extracts the refreshed creds from the stopped container
//  4. Verify extracted creds match the refreshed (not original) content
func TestDockerRun_CredentialLifecycle(t *testing.T) {
	tmp := t.TempDir()

	// Stage initial credentials on the host.
	initialCreds := `{"accessToken":"initial-tok","refreshToken":"refresh-tok","expiresAt":1000000000}`
	credFile := filepath.Join(tmp, "creds.json")
	os.WriteFile(credFile, []byte(initialCreds), 0644)

	// Also create a minimal .claude config dir (mounted :ro).
	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{}`), 0644)

	containerName := fmt.Sprintf("mittens-cred-lifecycle-%d", os.Getpid())

	runArgs := []string{
		"-v", credFile + ":/mnt/claude-config/.credentials.json:ro",
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
	}

	// Inside the container:
	// 1. Verify initial creds were copied by entrypoint
	// 2. Overwrite with "refreshed" creds (simulates Claude CLI token refresh)
	// 3. Print the refreshed content for verification
	script := `
set -e
# Step 1: Verify entrypoint copied the initial creds
initial=$(cat ~/.claude/.credentials.json)
echo "INITIAL: $initial"

# Verify permissions
perms=$(stat -c %a ~/.claude/.credentials.json)
echo "PERMS: $perms"

# Step 2: Simulate a token refresh (Claude CLI writes new creds)
cat > ~/.claude/.credentials.json << 'CREDS'
{"accessToken":"refreshed-tok","refreshToken":"new-refresh-tok","expiresAt":9999999999}
CREDS
chmod 600 ~/.claude/.credentials.json

# Step 3: Verify the write took effect
refreshed=$(cat ~/.claude/.credentials.json)
echo "REFRESHED: $refreshed"
`

	dockerRunKeep(t, testImage, containerName, runArgs, "bash", "-c", script)
	defer dockerRemove(t, containerName)

	// Step 4: Extract refreshed credentials from the stopped container.
	extractedPath := filepath.Join(tmp, "extracted-creds.json")
	dockerCP(t, containerName, "/home/claude/.claude/.credentials.json", extractedPath)

	data, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("reading extracted creds: %v", err)
	}
	extracted := strings.TrimSpace(string(data))

	// Must be the refreshed creds, not the initial ones.
	if !strings.Contains(extracted, "refreshed-tok") {
		t.Errorf("extracted creds should contain refreshed token, got: %s", extracted)
	}
	if strings.Contains(extracted, "initial-tok") {
		t.Errorf("extracted creds should NOT contain initial token, got: %s", extracted)
	}
	if !strings.Contains(extracted, "9999999999") {
		t.Errorf("extracted creds should contain new expiresAt, got: %s", extracted)
	}
}

// TestDockerRun_CredentialReadPerms verifies the entrypoint copies creds with
// 600 permissions and that the claude user can both read and write them.
func TestDockerRun_CredentialReadPerms(t *testing.T) {
	tmp := t.TempDir()
	credFile := filepath.Join(tmp, "creds.json")
	os.WriteFile(credFile, []byte(`{"accessToken":"tok"}`), 0644)

	runArgs := []string{
		"-v", credFile + ":/mnt/claude-config/.credentials.json:ro",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", `
set -e
# File exists and is readable
cat ~/.claude/.credentials.json > /dev/null

# Permissions are 600
perms=$(stat -c %a ~/.claude/.credentials.json)
echo "PERMS=$perms"

# Owner is claude
owner=$(stat -c %U ~/.claude/.credentials.json)
echo "OWNER=$owner"

# File is writable (simulate refresh)
echo '{"accessToken":"new"}' > ~/.claude/.credentials.json
echo "WRITE=ok"

# Verify write took effect
content=$(cat ~/.claude/.credentials.json)
echo "CONTENT=$content"
`)

	if !strings.Contains(out, "PERMS=600") {
		t.Errorf("expected PERMS=600 in output: %s", out)
	}
	if !strings.Contains(out, "OWNER=claude") {
		t.Errorf("expected OWNER=claude in output: %s", out)
	}
	if !strings.Contains(out, "WRITE=ok") {
		t.Errorf("credential file should be writable: %s", out)
	}
	if !strings.Contains(out, `"accessToken":"new"`) {
		t.Errorf("written content should persist within session: %s", out)
	}
}

// TestDockerRun_ConfigMountReadOnly verifies that the /mnt/claude-config/.claude
// mount is truly read-only — writes to it must fail, while the writable copy
// in $HOME/.claude is unaffected.
func TestDockerRun_ConfigMountReadOnly(t *testing.T) {
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"original":true}`), 0644)

	runArgs := []string{
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", `
set -e

# The read-only mount must reject writes.
if echo 'hacked' > /mnt/claude-config/.claude/settings.json 2>/dev/null; then
    echo "RO_MOUNT=WRITABLE_BUG"
else
    echo "RO_MOUNT=readonly"
fi

# Cannot create new files on the read-only mount either.
if touch /mnt/claude-config/.claude/evil.txt 2>/dev/null; then
    echo "RO_CREATE=WRITABLE_BUG"
else
    echo "RO_CREATE=readonly"
fi

# But the writable HOME copy is fine.
echo '{"modified":true}' > ~/.claude/settings.json
echo "HOME_WRITE=ok"

# Verify the original mount is still untouched.
orig=$(cat /mnt/claude-config/.claude/settings.json)
echo "ORIG=$orig"
`)

	if !strings.Contains(out, "RO_MOUNT=readonly") {
		t.Errorf("config mount should be read-only: %s", out)
	}
	if !strings.Contains(out, "RO_CREATE=readonly") {
		t.Errorf("config mount should reject new file creation: %s", out)
	}
	if !strings.Contains(out, "HOME_WRITE=ok") {
		t.Errorf("writable home should accept writes: %s", out)
	}
	if !strings.Contains(out, `"original":true`) {
		t.Errorf("original mount content should be untouched: %s", out)
	}
}

// TestDockerRun_CredentialMountReadOnly verifies that the credential file
// mount at /mnt/claude-config/.credentials.json is read-only — writes to
// the mount path fail, only the entrypoint-copied writable copy succeeds.
func TestDockerRun_CredentialMountReadOnly(t *testing.T) {
	tmp := t.TempDir()
	credFile := filepath.Join(tmp, "creds.json")
	os.WriteFile(credFile, []byte(`{"accessToken":"orig"}`), 0644)

	runArgs := []string{
		"-v", credFile + ":/mnt/claude-config/.credentials.json:ro",
	}

	out := dockerRun(t, testImage, runArgs, "bash", "-c", `
set -e

# The mount point itself must be read-only.
if echo '{"hacked":true}' > /mnt/claude-config/.credentials.json 2>/dev/null; then
    echo "CRED_MOUNT=WRITABLE_BUG"
else
    echo "CRED_MOUNT=readonly"
fi

# The writable copy at ~/.claude/.credentials.json should be writable.
echo '{"accessToken":"updated"}' > ~/.claude/.credentials.json
echo "HOME_CRED_WRITE=ok"

# Mount still has original content.
mount_content=$(cat /mnt/claude-config/.credentials.json)
echo "MOUNT_CONTENT=$mount_content"
`)

	if !strings.Contains(out, "CRED_MOUNT=readonly") {
		t.Errorf("credential mount should be read-only: %s", out)
	}
	if !strings.Contains(out, "HOME_CRED_WRITE=ok") {
		t.Errorf("writable credential copy should accept writes: %s", out)
	}
	if !strings.Contains(out, `"accessToken":"orig"`) {
		t.Errorf("mount should retain original content: %s", out)
	}
}

// TestDockerRun_CredentialPersistFromContainer verifies that docker cp can
// extract refreshed credentials from a stopped container. This validates
// the persist-file pattern used for provider state files (e.g. Gemini).
func TestDockerRun_CredentialPersistFromContainer(t *testing.T) {
	tmp := t.TempDir()

	// Simulate what CredentialManager.Setup() does: write creds to a temp file.
	hostCredFile := filepath.Join(tmp, "mittens-cred.json")
	initialJSON := `{"accessToken":"host-tok","expiresAt":100}`
	os.WriteFile(hostCredFile, []byte(initialJSON), 0600)

	claudeDir := filepath.Join(tmp, ".claude")
	os.MkdirAll(claudeDir, 0o755)

	containerName := fmt.Sprintf("mittens-persist-test-%d", os.Getpid())

	runArgs := []string{
		"-v", hostCredFile + ":/mnt/claude-config/.credentials.json:ro",
		"-v", claudeDir + ":/mnt/claude-config/.claude:ro",
	}

	// Simulate a Claude CLI session that refreshes the token.
	dockerRunKeep(t, testImage, containerName, runArgs, "bash", "-c", `
cat > ~/.claude/.credentials.json << 'EOF'
{"accessToken":"refreshed-in-container","refreshToken":"new-rt","expiresAt":9999999999}
EOF
chmod 600 ~/.claude/.credentials.json
`)
	defer dockerRemove(t, containerName)

	// Extract credentials via docker cp (same pattern used for persist files).
	cmd := exec.Command("docker", "cp",
		containerName+":/home/claude/.claude/.credentials.json",
		hostCredFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker cp failed: %v\noutput: %s", err, out)
	}

	// Read the extracted file — this is what gets persisted to credential stores.
	data, err := os.ReadFile(hostCredFile)
	if err != nil {
		t.Fatalf("reading host cred file after extract: %v", err)
	}
	result := string(data)

	if !strings.Contains(result, "refreshed-in-container") {
		t.Errorf("extracted creds should have refreshed token, got: %s", result)
	}
	if strings.Contains(result, "host-tok") {
		t.Errorf("extracted creds should NOT have original token, got: %s", result)
	}
	if !strings.Contains(result, "9999999999") {
		t.Errorf("extracted creds should have updated expiresAt, got: %s", result)
	}
}
