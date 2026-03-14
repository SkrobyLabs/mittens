package aws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// listINISections (credentials format)
// ---------------------------------------------------------------------------

func TestListINISections(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "credentials")
	content := "[default]\naws_access_key_id = AAA\n\n[prod]\naws_access_key_id = BBB\n\n[staging]\naws_access_key_id = CCC\n"
	os.WriteFile(f, []byte(content), 0644)

	sections := listINISections(f)
	want := []string{"default", "prod", "staging"}
	if len(sections) != len(want) {
		t.Fatalf("got %v, want %v", sections, want)
	}
	for i, s := range sections {
		if s != want[i] {
			t.Errorf("sections[%d] = %q, want %q", i, s, want[i])
		}
	}
}

func TestListINISections_Missing(t *testing.T) {
	sections := listINISections("/nonexistent/path")
	if sections != nil {
		t.Errorf("expected nil for missing file, got %v", sections)
	}
}

// ---------------------------------------------------------------------------
// listINISectionsConfig (config format — strips "profile " prefix)
// ---------------------------------------------------------------------------

func TestListINISectionsConfig(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config")
	content := "[default]\nregion = us-east-1\n\n[profile prod]\nregion = eu-west-1\n\n[profile staging]\nregion = ap-southeast-1\n"
	os.WriteFile(f, []byte(content), 0644)

	sections := listINISectionsConfig(f)
	want := []string{"default", "prod", "staging"}
	if len(sections) != len(want) {
		t.Fatalf("got %v, want %v", sections, want)
	}
	for i, s := range sections {
		if s != want[i] {
			t.Errorf("sections[%d] = %q, want %q", i, s, want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// filterINI (credentials format)
// ---------------------------------------------------------------------------

func TestFilterINI(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "credentials")
	content := "# header comment\n[default]\nkey = AAA\n\n[prod]\nkey = BBB\n\n[staging]\nkey = CCC\n"
	os.WriteFile(f, []byte(content), 0644)

	filtered, err := filterINI(f, []string{"prod"})
	if err != nil {
		t.Fatal(err)
	}

	result := string(filtered)
	if !strings.Contains(result, "# header comment") {
		t.Error("header comment not preserved")
	}
	if !strings.Contains(result, "[prod]") {
		t.Error("wanted section [prod] not found")
	}
	if strings.Contains(result, "[default]") {
		t.Error("unwanted section [default] should be filtered out")
	}
	if strings.Contains(result, "[staging]") {
		t.Error("unwanted section [staging] should be filtered out")
	}
}

// ---------------------------------------------------------------------------
// filterINIConfig (config format)
// ---------------------------------------------------------------------------

func TestFilterINIConfig(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config")
	content := "[default]\nregion = us-east-1\n\n[profile prod]\nregion = eu-west-1\n\n[profile staging]\nregion = ap-southeast-1\n"
	os.WriteFile(f, []byte(content), 0644)

	filtered, err := filterINIConfig(f, []string{"default", "prod"})
	if err != nil {
		t.Fatal(err)
	}

	result := string(filtered)
	if !strings.Contains(result, "[default]") {
		t.Error("wanted section [default] not found")
	}
	if !strings.Contains(result, "[profile prod]") {
		t.Error("wanted section [profile prod] not found")
	}
	if strings.Contains(result, "[profile staging]") {
		t.Error("unwanted section [profile staging] should be filtered out")
	}
}

// ---------------------------------------------------------------------------
// checkSourceProfiles
// ---------------------------------------------------------------------------

func TestCheckSourceProfiles(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config")
	content := `[default]
region = us-east-1

[profile child]
source_profile = base
role_arn = arn:aws:iam::role/test

[profile base]
region = eu-west-1
`
	os.WriteFile(f, []byte(content), 0644)

	// Request "child" but not "base". checkSourceProfiles should find the dependency.
	extra := checkSourceProfiles(f, []string{"child"})
	if len(extra) != 1 || extra[0] != "base" {
		t.Errorf("extra = %v, want [base]", extra)
	}

	// When both are already requested, no extras.
	extra = checkSourceProfiles(f, []string{"child", "base"})
	if len(extra) != 0 {
		t.Errorf("extra = %v, want []", extra)
	}
}

// ---------------------------------------------------------------------------
// profilesUseSSO
// ---------------------------------------------------------------------------

func TestProfilesUseSSO(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "config")
	content := `[default]
region = us-east-1

[profile sso-profile]
sso_start_url = https://example.awsapps.com/start
sso_account_id = 123456789
sso_role_name = ReadOnly
region = us-east-1

[profile static-profile]
region = eu-west-1
`
	os.WriteFile(f, []byte(content), 0644)

	if !profilesUseSSO(f, []string{"sso-profile"}) {
		t.Error("expected true for SSO profile")
	}

	if profilesUseSSO(f, []string{"static-profile"}) {
		t.Error("expected false for non-SSO profile")
	}

	if profilesUseSSO(f, []string{"default"}) {
		t.Error("expected false for default profile with no SSO keys")
	}
}

// ---------------------------------------------------------------------------
// setup — full resolver flow: scoped credentials staging + docker args
// ---------------------------------------------------------------------------

// newAWSFixture creates a realistic ~/.aws directory in a temp dir.
func newAWSFixture(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	awsDir := filepath.Join(home, ".aws")
	os.MkdirAll(awsDir, 0755)

	credentials := `[default]
aws_access_key_id = DEFAULT_KEY
aws_secret_access_key = DEFAULT_SECRET

[prod]
aws_access_key_id = PROD_KEY
aws_secret_access_key = PROD_SECRET

[dev]
aws_access_key_id = DEV_KEY
aws_secret_access_key = DEV_SECRET
`
	os.WriteFile(filepath.Join(awsDir, "credentials"), []byte(credentials), 0600)

	config := `[default]
region = us-east-1

[profile prod]
region = eu-west-1

[profile dev]
region = ap-southeast-1

[profile sso-user]
sso_start_url = https://example.awsapps.com/start
sso_account_id = 123456789
sso_role_name = ReadOnly
region = us-east-1

[profile role-user]
source_profile = prod
role_arn = arn:aws:iam::123456789:role/test
`
	os.WriteFile(filepath.Join(awsDir, "config"), []byte(config), 0600)

	// SSO cache.
	ssoCache := filepath.Join(awsDir, "sso", "cache")
	os.MkdirAll(ssoCache, 0755)
	os.WriteFile(filepath.Join(ssoCache, "abc123.json"), []byte(`{"accessToken":"sso-tok"}`), 0600)

	// CLI cache.
	cliCache := filepath.Join(awsDir, "cli", "cache")
	os.MkdirAll(cliCache, 0755)
	os.WriteFile(filepath.Join(cliCache, "cached.json"), []byte(`{"Credentials":{}}`), 0600)

	return home
}

func TestSetup_FilteredProfiles(t *testing.T) {
	home := newAWSFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "aws",
		Enabled: true,
		Args:    []string{"prod"},
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    staging,
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// Docker args should mount staging dir at /home/claude/.aws:ro.
	joined := strings.Join(dockerArgs, " ")
	if !strings.Contains(joined, staging+":/home/claude/.aws:ro") {
		t.Errorf("docker args missing mount, got: %v", dockerArgs)
	}

	// Staged credentials should contain only [prod], not [default] or [dev].
	credsData, err := os.ReadFile(filepath.Join(staging, "credentials"))
	if err != nil {
		t.Fatal("staged credentials file missing")
	}
	creds := string(credsData)
	if !strings.Contains(creds, "[prod]") {
		t.Error("staged credentials missing [prod]")
	}
	if strings.Contains(creds, "[dev]") {
		t.Error("staged credentials should not contain [dev]")
	}
	if strings.Contains(creds, "[default]") {
		t.Error("staged credentials should not contain [default]")
	}

	// Staged config should contain [profile prod], not others.
	configData, err := os.ReadFile(filepath.Join(staging, "config"))
	if err != nil {
		t.Fatal("staged config file missing")
	}
	cfg := string(configData)
	if !strings.Contains(cfg, "[profile prod]") {
		t.Error("staged config missing [profile prod]")
	}
	if strings.Contains(cfg, "[profile dev]") {
		t.Error("staged config should not contain [profile dev]")
	}
}

func TestSetup_AllMode(t *testing.T) {
	home := newAWSFixture(t)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "aws",
		Enabled: true,
		AllMode: true,
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    t.TempDir(),
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// Should mount the entire ~/.aws directory.
	joined := strings.Join(dockerArgs, " ")
	awsDir := filepath.Join(home, ".aws")
	if !strings.Contains(joined, awsDir+":/home/claude/.aws:ro") {
		t.Errorf("AllMode should mount entire aws dir, got: %v", dockerArgs)
	}
}

func TestSetup_SourceProfileAutoInclude(t *testing.T) {
	home := newAWSFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "aws",
		Enabled: true,
		Args:    []string{"role-user"},
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    staging,
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// role-user depends on source_profile=prod, so prod should be auto-included.
	credsData, _ := os.ReadFile(filepath.Join(staging, "credentials"))
	creds := string(credsData)
	if !strings.Contains(creds, "[prod]") {
		t.Error("source_profile 'prod' should be auto-included in staged credentials")
	}
}

func TestSetup_SSOCacheCopied(t *testing.T) {
	home := newAWSFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "aws",
		Enabled: true,
		Args:    []string{"sso-user"},
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    staging,
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// SSO cache should be copied because sso-user has sso_ keys.
	ssoCache := filepath.Join(staging, "sso", "cache", "abc123.json")
	if _, err := os.Stat(ssoCache); err != nil {
		t.Errorf("SSO cache file should be copied, got: %v", err)
	}
}

func TestSetup_CLICacheCopied(t *testing.T) {
	home := newAWSFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "aws",
		Enabled: true,
		Args:    []string{"prod"},
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    staging,
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// CLI cache should be copied wholesale.
	cached := filepath.Join(staging, "cli", "cache", "cached.json")
	if _, err := os.Stat(cached); err != nil {
		t.Errorf("CLI cache should be copied, got: %v", err)
	}
}

func TestSetup_NoProfiles(t *testing.T) {
	home := newAWSFixture(t)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "aws",
		Enabled: true,
		Args:    nil, // no profiles selected
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    t.TempDir(),
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// No docker args should be added.
	if len(dockerArgs) != 0 {
		t.Errorf("expected no docker args when no profiles, got: %v", dockerArgs)
	}
}
