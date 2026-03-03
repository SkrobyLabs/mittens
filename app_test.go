package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Skroby/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// ParseFlags — core boolean flags
// ---------------------------------------------------------------------------

func TestParseFlags_CoreBooleans(t *testing.T) {
	tests := []struct {
		flag  string
		check func(*App) bool
	}{
		{"--verbose", func(a *App) bool { return a.Verbose }},
		{"-v", func(a *App) bool { return a.Verbose }},
		{"--no-config", func(a *App) bool { return a.NoConfig }},
		{"--no-history", func(a *App) bool { return a.NoHistory }},
		{"--no-build", func(a *App) bool { return a.NoBuild }},
		{"--dind", func(a *App) bool { return a.DinD }},
		{"--yolo", func(a *App) bool { return a.Yolo }},
		{"--network-host", func(a *App) bool { return a.NetworkHost }},
		{"--worktree", func(a *App) bool { return a.Worktree }},
		{"--shell", func(a *App) bool { return a.Shell }},
		{"--no-resume", func(a *App) bool { return a.NoResume }},
		{"--no-notify", func(a *App) bool { return a.NoNotify }},
	}
	for _, tc := range tests {
		t.Run(tc.flag, func(t *testing.T) {
			a := &App{}
			if err := a.ParseFlags([]string{tc.flag}); err != nil {
				t.Fatal(err)
			}
			if !tc.check(a) {
				t.Errorf("flag %s not set", tc.flag)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — --dir with argument
// ---------------------------------------------------------------------------

func TestParseFlags_Dir(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--dir", "/extra/path"}); err != nil {
		t.Fatal(err)
	}
	if len(a.ExtraDirs) != 1 || a.ExtraDirs[0] != "/extra/path" {
		t.Errorf("ExtraDirs = %v, want [/extra/path]", a.ExtraDirs)
	}
}

func TestParseFlags_DirMissingArg(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--dir"})
	if err == nil {
		t.Error("expected error for --dir without argument")
	}
}

func TestParseFlags_DirMissingArgNextIsFlag(t *testing.T) {
	a := &App{}
	err := a.ParseFlags([]string{"--dir", "--verbose"})
	if err == nil {
		t.Error("expected error for --dir followed by another flag")
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — "--" separator
// ---------------------------------------------------------------------------

func TestParseFlags_Separator(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--verbose", "--", "--resume", "--model", "opus"}); err != nil {
		t.Fatal(err)
	}

	if !a.Verbose {
		t.Error("--verbose before -- should be parsed")
	}

	want := []string{"--resume", "--model", "opus"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i, arg := range a.ClaudeArgs {
		if arg != want[i] {
			t.Errorf("ClaudeArgs[%d] = %q, want %q", i, arg, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — unknown flags forwarded to ClaudeArgs
// ---------------------------------------------------------------------------

func TestParseFlags_UnknownForwarded(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--resume", "--model", "opus"}); err != nil {
		t.Fatal(err)
	}

	want := []string{"--resume", "--model", "opus"}
	if len(a.ClaudeArgs) != len(want) {
		t.Fatalf("ClaudeArgs = %v, want %v", a.ClaudeArgs, want)
	}
	for i, arg := range a.ClaudeArgs {
		if arg != want[i] {
			t.Errorf("ClaudeArgs[%d] = %q, want %q", i, arg, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — extension delegation
// ---------------------------------------------------------------------------

func TestParseFlags_ExtensionDelegation(t *testing.T) {
	a := &App{
		Extensions: []*registry.Extension{
			{
				Name: "test-ext",
				Flags: []registry.ExtensionFlag{
					{Name: "--test-ext", Arg: "csv"},
				},
			},
		},
	}

	if err := a.ParseFlags([]string{"--test-ext", "a,b,c", "--verbose"}); err != nil {
		t.Fatal(err)
	}

	ext := a.Extensions[0]
	if !ext.Enabled {
		t.Error("extension should be enabled")
	}
	wantArgs := []string{"a", "b", "c"}
	if len(ext.Args) != len(wantArgs) {
		t.Fatalf("ext.Args = %v, want %v", ext.Args, wantArgs)
	}
	for i, a := range ext.Args {
		if a != wantArgs[i] {
			t.Errorf("ext.Args[%d] = %q, want %q", i, a, wantArgs[i])
		}
	}
	if !a.Verbose {
		t.Error("--verbose should still be parsed after extension flag")
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — multiple --dir flags
// ---------------------------------------------------------------------------

func TestParseFlags_MultipleDir(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--dir", "/a", "--dir", "/b"}); err != nil {
		t.Fatal(err)
	}
	if len(a.ExtraDirs) != 2 {
		t.Fatalf("ExtraDirs = %v, want 2 entries", a.ExtraDirs)
	}
	if a.ExtraDirs[0] != "/a" || a.ExtraDirs[1] != "/b" {
		t.Errorf("ExtraDirs = %v, want [/a, /b]", a.ExtraDirs)
	}
}

// ---------------------------------------------------------------------------
// ParseFlags — empty args
// ---------------------------------------------------------------------------

func TestParseFlags_Empty(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	if a.Verbose || a.Shell || len(a.ClaudeArgs) > 0 {
		t.Error("empty args should leave defaults")
	}
}

// ---------------------------------------------------------------------------
// assembleDockerArgs helpers
// ---------------------------------------------------------------------------

// argSliceContains reports whether any element in args contains substr.
func argSliceContains(args []string, substr string) bool {
	for _, a := range args {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
}

// argExists reports whether args contains the given exact value.
func argExists(args []string, val string) bool {
	for _, a := range args {
		if a == val {
			return true
		}
	}
	return false
}

// argPairExists reports whether args contains flag immediately followed by val.
// e.g. argPairExists(args, "-v", "/path") matches [... "-v" "/path" ...].
func argPairExists(args []string, flag, val string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

// argPairContains reports whether args contains flag immediately followed by
// a value containing substr.
func argPairContains(args []string, flag, substr string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && strings.Contains(args[i+1], substr) {
			return true
		}
	}
	return false
}

// setupTestHome creates a minimal $HOME layout for assembleDockerArgs tests.
// Returns the temp home dir. The caller should use t.Setenv("HOME", home).
func setupTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	return home
}

// ---------------------------------------------------------------------------
// assembleDockerArgs — Tier 3 orchestration tests
// ---------------------------------------------------------------------------

func TestAssembleDockerArgs_Baseline(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	t.Setenv("TERM", "xterm-256color")

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-test",
		WorkspaceMountSrc: "/tmp/workspace",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Core flags always present.
	if !argSliceContains(args, "-it") {
		t.Error("missing -it")
	}
	if !argPairExists(args, "--name", "mittens-test") {
		t.Error("missing --name")
	}

	// Workspace mount.
	if !argPairExists(args, "-v", "/tmp/workspace:/workspace") {
		t.Error("missing workspace mount")
	}

	// Environment variables.
	if !argPairExists(args, "-e", "ANTHROPIC_API_KEY=sk-test-key") {
		t.Error("missing ANTHROPIC_API_KEY env")
	}
	if !argPairContains(args, "-e", "TERM=") {
		t.Error("missing TERM env")
	}
	if !argPairExists(args, "-e", "MITTENS_DIND=false") {
		t.Error("missing MITTENS_DIND env")
	}

	// Security hardening (non-DinD).
	if !argPairExists(args, "--cap-drop", "ALL") {
		t.Error("missing --cap-drop ALL")
	}
	if !argPairExists(args, "--cap-add", "SETUID") {
		t.Error("missing --cap-add SETUID")
	}
	if !argPairExists(args, "--cap-add", "SETGID") {
		t.Error("missing --cap-add SETGID")
	}
	if !argPairExists(args, "--security-opt", "no-new-privileges") {
		t.Error("missing --security-opt no-new-privileges")
	}

	// Should NOT have credential mount or session mounts.
	if argPairContains(args, "-v", ".credentials.json") {
		t.Error("credential mount should not be present without credentials")
	}
	if argPairContains(args, "-v", "/projects/") {
		t.Error("session persistence mounts should not be present with NoHistory=true")
	}
}

func TestAssembleDockerArgs_WithCredentials(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create a real temp credential file.
	credFile := filepath.Join(t.TempDir(), "creds.json")
	os.WriteFile(credFile, []byte(`{"token":"test"}`), 0600)

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-cred",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{tmpFile: credFile},
	}

	args := a.assembleDockerArgs(nil, nil)

	wantMount := credFile + ":/mnt/claude-config/.credentials.json:ro"
	if !argPairExists(args, "-v", wantMount) {
		t.Errorf("missing credential mount, want -v %s\nargs: %v", wantMount, args)
	}
}

func TestAssembleDockerArgs_SessionPersistence(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	workspace := "/Users/test/project"
	hostProjectDir := ProjectDir(workspace)

	// Create required dirs.
	os.MkdirAll(filepath.Join(home, ".claude", "projects", hostProjectDir), 0o755)

	a := &App{
		NoHistory:          false,
		ContainerName:      "mittens-session",
		WorkspaceMountSrc:  "/tmp/ws",
		HostProjectDir:     hostProjectDir,
		Workspace:          workspace,
		EffectiveWorkspace: workspace,
		Credentials:        &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Project dir mount.
	projDir := filepath.Join(home, ".claude", "projects", hostProjectDir)
	containerProjDir := filepath.Join("/home/claude/.claude/projects", hostProjectDir)
	if !argPairExists(args, "-v", projDir+":"+containerProjDir) {
		t.Error("missing project dir mount")
	}

	// Plans and tasks mounts (Worktree=false).
	if !argPairContains(args, "-v", "/.claude/plans:") {
		t.Error("missing plans mount")
	}
	if !argPairContains(args, "-v", "/.claude/tasks:") {
		t.Error("missing tasks mount")
	}

	// MITTENS_HOST_WORKSPACE should be set when EffectiveWorkspace != "/workspace".
	if !argPairContains(args, "-e", "MITTENS_HOST_WORKSPACE="+workspace) {
		t.Error("missing MITTENS_HOST_WORKSPACE env")
	}
}

func TestAssembleDockerArgs_DinD(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		DinD:              true,
		ContainerName:     "mittens-dind",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Should have --privileged.
	if !argSliceContains(args, "--privileged") {
		t.Error("missing --privileged for DinD")
	}

	// Docker volume mount.
	if !argPairExists(args, "-v", "mittens-dind-docker:/var/lib/docker") {
		t.Error("missing docker volume mount for DinD")
	}

	// MITTENS_DIND should be true.
	if !argPairExists(args, "-e", "MITTENS_DIND=true") {
		t.Error("missing MITTENS_DIND=true")
	}

	// Security hardening should be ABSENT (mutually exclusive with privileged).
	if argPairExists(args, "--cap-drop", "ALL") {
		t.Error("--cap-drop ALL should not be present with DinD")
	}
	if argPairExists(args, "--security-opt", "no-new-privileges") {
		t.Error("--security-opt should not be present with DinD")
	}
}

func TestAssembleDockerArgs_NetworkHost(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		NetworkHost:       true,
		ContainerName:     "mittens-net",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "--network", "host") {
		t.Error("missing --network host")
	}
}

func TestAssembleDockerArgs_ExtensionMountsEnvCaps(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create a dir for the conditional mount.
	mountSrc := filepath.Join(home, ".kube")
	os.MkdirAll(mountSrc, 0o755)

	ext := &registry.Extension{
		Name:    "test-ext",
		Enabled: true,
		Mounts: []registry.MountConfig{
			{
				Src:  "~/.kube",
				Dst:  "/home/claude/.kube",
				Mode: "ro",
				When: "dir_exists",
				Env:  map[string]string{"KUBECONFIG": "/home/claude/.kube/config"},
			},
		},
		Env:          map[string]string{"MY_EXT_VAR": "hello"},
		Capabilities: []string{"NET_RAW"},
		Firewall:     []string{"ext.example.com", "api.ext.com"},
	}

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-ext",
		WorkspaceMountSrc: "/tmp/ws",
		Extensions:        []*registry.Extension{ext},
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Extension mount should be present (condition satisfied).
	if !argPairContains(args, "-v", ".kube:/home/claude/.kube:ro") {
		t.Error("missing extension mount")
	}

	// Mount env var.
	if !argPairExists(args, "-e", "KUBECONFIG=/home/claude/.kube/config") {
		t.Error("missing mount env var KUBECONFIG")
	}

	// Extension env var.
	if !argPairExists(args, "-e", "MY_EXT_VAR=hello") {
		t.Error("missing extension env var MY_EXT_VAR")
	}

	// Capability.
	if !argPairExists(args, "--cap-add", "NET_RAW") {
		t.Error("missing --cap-add NET_RAW")
	}

	// Firewall domains.
	if !argPairContains(args, "-e", "MITTENS_FIREWALL_EXTRA=ext.example.com,api.ext.com") {
		t.Error("missing MITTENS_FIREWALL_EXTRA with extension domains")
	}
}

func TestAssembleDockerArgs_ResolverContributions(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Extension with its own firewall domains.
	ext := &registry.Extension{
		Name:     "resolver-ext",
		Enabled:  true,
		Firewall: []string{"ext-domain.com"},
	}

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-resolver",
		WorkspaceMountSrc: "/tmp/ws",
		Extensions:        []*registry.Extension{ext},
		Credentials:       &CredentialManager{},
	}

	resolverArgs := []string{"-v", "/resolver/path:/container/path:ro", "-e", "RESOLVER_VAR=yes"}
	resolverFirewall := []string{"resolver-domain.com"}

	args := a.assembleDockerArgs(resolverArgs, resolverFirewall)

	// Resolver docker args appended verbatim.
	if !argPairExists(args, "-v", "/resolver/path:/container/path:ro") {
		t.Error("missing resolver mount")
	}
	if !argPairExists(args, "-e", "RESOLVER_VAR=yes") {
		t.Error("missing resolver env var")
	}

	// Firewall domains aggregated: extension + resolver.
	if !argPairContains(args, "-e", "MITTENS_FIREWALL_EXTRA=") {
		t.Fatal("missing MITTENS_FIREWALL_EXTRA")
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "MITTENS_FIREWALL_EXTRA=") {
			val := strings.TrimPrefix(args[i+1], "MITTENS_FIREWALL_EXTRA=")
			if !strings.Contains(val, "ext-domain.com") {
				t.Error("MITTENS_FIREWALL_EXTRA missing extension domain")
			}
			if !strings.Contains(val, "resolver-domain.com") {
				t.Error("MITTENS_FIREWALL_EXTRA missing resolver domain")
			}
			break
		}
	}
}

func TestAssembleDockerArgs_OptionalFiles(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create .claude.json and .gitconfig.
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{}`), 0644)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\nname=test"), 0644)

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-opt",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Both files should be mounted read-only.
	claudeJSON := filepath.Join(home, ".claude.json") + ":/mnt/claude-config/.claude.json:ro"
	if !argPairExists(args, "-v", claudeJSON) {
		t.Error("missing .claude.json mount")
	}
	gitconfig := filepath.Join(home, ".gitconfig") + ":/mnt/claude-config/.gitconfig:ro"
	if !argPairExists(args, "-v", gitconfig) {
		t.Error("missing .gitconfig mount")
	}

	// Now test without the files.
	home2 := setupTestHome(t)
	t.Setenv("HOME", home2)

	a2 := &App{
		NoHistory:         true,
		ContainerName:     "mittens-opt2",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args2 := a2.assembleDockerArgs(nil, nil)

	if argPairContains(args2, "-v", ".claude.json") {
		t.Error(".claude.json mount should not be present when file doesn't exist")
	}
	if argPairContains(args2, "-v", ".gitconfig") {
		t.Error(".gitconfig mount should not be present when file doesn't exist")
	}
}

func TestAssembleDockerArgs_CredBroker(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-broker",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
		brokerPort:        12345,
	}

	args := a.assembleDockerArgs(nil, nil)

	// Broker port env var.
	if !argPairExists(args, "-e", "MITTENS_BROKER_PORT=12345") {
		t.Error("missing MITTENS_BROKER_PORT env var")
	}

	// host.docker.internal mapping.
	if !argExists(args, "--add-host=host.docker.internal:host-gateway") {
		t.Error("missing --add-host for host.docker.internal")
	}
}

func TestAssembleDockerArgs_NoBroker(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-nobroker",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
		// brokerPort is 0 — no broker
	}

	args := a.assembleDockerArgs(nil, nil)

	// Should NOT have broker env var.
	if argPairContains(args, "-e", "MITTENS_BROKER_PORT") {
		t.Error("MITTENS_BROKER_PORT should not be present without broker")
	}
}

func TestAssembleDockerArgs_ExtraDirs(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Create real temp dirs as extra dirs.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-extra",
		WorkspaceMountSrc: "/tmp/ws",
		ExtraDirs:         []string{dir1, dir2},
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	// Each dir gets a -v mount.
	if !argPairExists(args, "-v", dir1+":"+dir1) {
		t.Errorf("missing mount for extra dir %s", dir1)
	}
	if !argPairExists(args, "-v", dir2+":"+dir2) {
		t.Errorf("missing mount for extra dir %s", dir2)
	}

	// MITTENS_EXTRA_DIRS env var with colon-separated paths.
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], "MITTENS_EXTRA_DIRS=") {
			val := strings.TrimPrefix(args[i+1], "MITTENS_EXTRA_DIRS=")
			parts := strings.Split(val, ":")
			if len(parts) != 2 {
				t.Errorf("MITTENS_EXTRA_DIRS should have 2 paths, got %d: %q", len(parts), val)
			}
			if parts[0] != dir1 || parts[1] != dir2 {
				t.Errorf("MITTENS_EXTRA_DIRS = %q, want %s:%s", val, dir1, dir2)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("missing MITTENS_EXTRA_DIRS env var")
	}
}

func TestParseFlags_Name(t *testing.T) {
	a := &App{}
	if err := a.ParseFlags([]string{"--name", "my-instance"}); err != nil {
		t.Fatal(err)
	}
	if a.InstanceName != "my-instance" {
		t.Errorf("InstanceName = %q, want %q", a.InstanceName, "my-instance")
	}
}

func TestAssembleDockerArgs_CustomName(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		InstanceName:      "planner-1",
		ContainerName:     "mittens-planner-1",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "--name", "mittens-planner-1") {
		t.Error("missing --name mittens-planner-1")
	}
	if !argPairExists(args, "-e", "MITTENS_INSTANCE_NAME=planner-1") {
		t.Error("missing MITTENS_INSTANCE_NAME env var")
	}
}

func TestAssembleDockerArgs_NoCustomName(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-12345",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if argPairContains(args, "-e", "MITTENS_INSTANCE_NAME") {
		t.Error("MITTENS_INSTANCE_NAME should not be set without --name")
	}
}

func TestAssembleDockerArgs_ContainerNameEnv(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-42",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "-e", "MITTENS_CONTAINER_NAME=mittens-42") {
		t.Error("missing MITTENS_CONTAINER_NAME env var")
	}
}

func TestAssembleDockerArgs_NoNotify(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		NoNotify:          true,
		ContainerName:     "mittens-nn",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if !argPairExists(args, "-e", "MITTENS_NO_NOTIFY=true") {
		t.Error("missing MITTENS_NO_NOTIFY env var")
	}
}

func TestAssembleDockerArgs_NotifyEnabled(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	a := &App{
		NoHistory:         true,
		ContainerName:     "mittens-yes",
		WorkspaceMountSrc: "/tmp/ws",
		Credentials:       &CredentialManager{},
	}

	args := a.assembleDockerArgs(nil, nil)

	if argPairContains(args, "-e", "MITTENS_NO_NOTIFY") {
		t.Error("MITTENS_NO_NOTIFY should not be set when notifications enabled")
	}
}

func TestIsValidContainerName(t *testing.T) {
	valid := []string{"foo", "my-instance", "app.v2", "test_1", "A123"}
	for _, name := range valid {
		if !isValidContainerName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	invalid := []string{"", "-bad", ".dot", "_under", "has space", "no/slash", "no:colon"}
	for _, name := range invalid {
		if isValidContainerName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestInspectContainerRunning_NonExistent(t *testing.T) {
	// A container that doesn't exist should return (false, false).
	exists, running := InspectContainerRunning("mittens-nonexistent-test-container-xyz")
	if exists {
		t.Error("expected exists=false for non-existent container")
	}
	if running {
		t.Error("expected running=false for non-existent container")
	}
}
