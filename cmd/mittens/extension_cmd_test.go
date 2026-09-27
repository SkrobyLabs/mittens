package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func TestExtensionInstallRecordsLocalProvenanceAndListsIt(t *testing.T) {
	home, source := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeExtensionFile(t, source, "extension.yaml", "name: local-test\ndescription: Local test\n", 0o600)
	// A supplied metadata file must not override the actual installation source.
	writeExtensionFile(t, source, registry.ProvenanceFile, `{"source":"forged","revision":"fake"}`, 0o600)
	output := captureStdoutFor(t, func() error { return extensionInstall(source) })
	if !strings.Contains(output, "Plugin manifest, list, and setup commands run with your user permissions") {
		t.Fatalf("missing host trust notice: %s", output)
	}
	provenance := readInstalledExtensionProvenance(t, home, "local-test")
	if provenance.Source != source || !provenance.Local || provenance.Revision != "" {
		t.Fatalf("local provenance = %+v", provenance)
	}
	output = captureStdoutFor(t, extensionList)
	if !strings.Contains(output, "source: "+source+" (local); revision: unknown") {
		t.Fatalf("list missing recorded provenance: %s", output)
	}
}

func TestExtensionInstallNoticePrecedesPluginManifest(t *testing.T) {
	home, source := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	logPath, marker := filepath.Join(t.TempDir(), "stdout"), filepath.Join(t.TempDir(), "executed")
	t.Setenv("MITTENS_TEST_NOTICE_PATH", logPath)
	t.Setenv("MITTENS_TEST_PLUGIN_MARKER", marker)
	writeExtensionFile(t, source, "plugin", `#!/bin/sh
if ! grep -q 'Extension trust:' "$MITTENS_TEST_NOTICE_PATH"; then exit 23; fi
printf 'notice-present' > "$MITTENS_TEST_PLUGIN_MARKER"
printf '{"name":"plugin-only","description":"Plugin test"}'
`, 0o700)
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = oldStdout; f.Close() }()
	if err := extensionInstall(source); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "notice-present" {
		t.Fatalf("plugin ran before trust notice: marker=%q error=%v", got, err)
	}
	readInstalledExtensionProvenance(t, home, "plugin-only")
}

func TestExtensionNamesCannotEscapeInstallationDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outside := filepath.Join(home, ".mittens", "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExtensionFile(t, outside, "keep", "original", 0o600)
	for _, name := range []string{"..", "../outside", "../../escape", outside, `..\outside`} {
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			encoded, err := json.Marshal(name)
			if err != nil {
				t.Fatal(err)
			}
			writeExtensionFile(t, source, "extension.yaml", "name: "+string(encoded)+"\n", 0o600)
			if err := extensionInstall(source); err == nil || !strings.Contains(err.Error(), "invalid extension name") {
				t.Fatalf("install accepted unsafe name %q: %v", name, err)
			}
			if err := extensionRemove(name); err == nil || !strings.Contains(err.Error(), "invalid extension name") {
				t.Fatalf("remove accepted unsafe name %q: %v", name, err)
			}
			if got, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(got) != "original" {
				t.Fatalf("outside content changed: %q, %v", got, err)
			}
		})
	}
}

func TestExtensionInstallRecordsGitRevisionAndLocalChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "source.git")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExtensionFile(t, repo, "extension.yaml", "name: git-test\ndescription: Git test\n", 0o600)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Extension Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Extension Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("add", "extension.yaml")
	git("-c", "commit.gpgsign=false", "commit", "-m", "extension fixture")
	revision := git("rev-parse", "HEAD")
	captureStdoutFor(t, func() error { return extensionInstall(repo) })
	provenance := readInstalledExtensionProvenance(t, home, "git-test")
	if provenance.Source != repo || provenance.Revision != revision || provenance.Local || provenance.Dirty {
		t.Fatalf("cloned provenance = %+v", provenance)
	}
	writeExtensionFile(t, repo, "extension.yaml", "name: git-test\ndescription: Local change\n", 0o600)
	// A trailing separator selects the local copy path even for a .git suffix.
	captureStdoutFor(t, func() error { return extensionInstall(repo + string(filepath.Separator)) })
	provenance = readInstalledExtensionProvenance(t, home, "git-test")
	if provenance.Source != repo || provenance.Revision != revision || !provenance.Local || !provenance.Dirty {
		t.Fatalf("dirty local provenance = %+v", provenance)
	}
}

func TestExtensionInstallDoesNotEchoCredentialedCloneURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	writeExtensionFile(t, bin, "git", "#!/bin/sh\nprintf '%s\\n' \"$5\" >&2\nexit 1\n", 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = f, f
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr; f.Close() }()
	err = extensionInstall("https://user:secret@example.invalid/ext.git?token=private#hidden")
	if err == nil {
		t.Fatal("expected simulated clone failure")
	}
	out, readErr := os.ReadFile(f.Name())
	if readErr != nil {
		t.Fatal(readErr)
	}
	combined := string(out) + err.Error()
	for _, secret := range []string{"user:", "secret", "token=", "private", "hidden"} {
		if strings.Contains(combined, secret) {
			t.Errorf("source credential %q was echoed: %s", secret, combined)
		}
	}
	if !strings.Contains(combined, "https://example.invalid/ext.git") {
		t.Fatalf("missing sanitized diagnostic: %s", combined)
	}
}

func writeExtensionFile(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readInstalledExtensionProvenance(t *testing.T, home, name string) registry.ExtensionProvenance {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".mittens", "extensions", name, registry.ProvenanceFile))
	if err != nil {
		t.Fatal(err)
	}
	var provenance registry.ExtensionProvenance
	if err := json.Unmarshal(data, &provenance); err != nil {
		t.Fatal(fmt.Errorf("invalid installed provenance: %w", err))
	}
	return provenance
}
