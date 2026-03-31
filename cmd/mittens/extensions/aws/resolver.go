// Package aws implements the AWS credential resolver for mittens.
// It registers itself with the registry at init time.
package aws

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

func init() {
	registry.Register("aws", &registry.Registration{
		List:  listProfiles,
		Setup: setup,
	})
}

// ---------------------------------------------------------------------------
// List resolver
// ---------------------------------------------------------------------------

// listProfiles reads ~/.aws/credentials and ~/.aws/config and returns a
// deduplicated, sorted list of profile names.
func listProfiles() ([]registry.ListItem, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	awsDir := filepath.Join(home, ".aws")

	seen := make(map[string]bool)
	var profiles []string

	// credentials file: sections are [profile_name]
	for _, name := range listINISections(filepath.Join(awsDir, "credentials")) {
		if !seen[name] {
			seen[name] = true
			profiles = append(profiles, name)
		}
	}

	// config file: sections are [default] or [profile name]
	for _, name := range listINISectionsConfig(filepath.Join(awsDir, "config")) {
		if !seen[name] {
			seen[name] = true
			profiles = append(profiles, name)
		}
	}

	sort.Strings(profiles)
	var items []registry.ListItem
	for _, p := range profiles {
		items = append(items, registry.ListItem{Label: p, Value: p})
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// Setup resolver
// ---------------------------------------------------------------------------

// setup mounts AWS credentials into the container, optionally filtered to
// only the requested profiles. It also discovers AWS regions and SSO URLs
// to add region-specific service endpoints to the firewall whitelist.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension
	home := ctx.Home
	awsDir := filepath.Join(home, ".aws")

	// Discover and add region-specific firewall domains.
	domains := discoverFirewallDomains(home)
	if len(domains) > 0 {
		*ctx.FirewallExtra = append(*ctx.FirewallExtra, domains...)
	}

	credStagingPath := "/mnt/mittens-creds-aws"

	// --aws-all: mount entire directory
	if ext.AllMode {
		if info, err := os.Stat(awsDir); err == nil && info.IsDir() {
			*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", awsDir+":"+credStagingPath+":ro")
			*ctx.CredStagingDirs = append(*ctx.CredStagingDirs, credStagingPath+":.aws")
			registry.LogInfo("Mounting AWS credentials (all profiles)")
		} else {
			registry.LogWarn("AWS credentials requested but %s does not exist", awsDir)
		}
		return nil
	}

	// No profiles selected: nothing to do
	if len(ext.Args) == 0 {
		return nil
	}

	// Create staging directory
	staging := ctx.StagingDir
	if staging == "" {
		tmpDir, err := os.MkdirTemp("", "mittens-aws.*")
		if err != nil {
			return fmt.Errorf("creating AWS temp dir: %w", err)
		}
		staging = tmpDir
		*ctx.TempDirs = append(*ctx.TempDirs, tmpDir)
	}

	profiles := ext.Args

	// Check for source_profile references and add missing ones
	configFile := filepath.Join(awsDir, "config")
	if _, err := os.Stat(configFile); err == nil {
		extra := checkSourceProfiles(configFile, profiles)
		if len(extra) > 0 {
			profiles = append(profiles, extra...)
		}
	}

	// Validate requested profiles exist
	credsFile := filepath.Join(awsDir, "credentials")
	credsSections := listINISections(credsFile)
	configSections := listINISectionsConfig(configFile)
	known := make(map[string]bool)
	for _, s := range credsSections {
		known[s] = true
	}
	for _, s := range configSections {
		known[s] = true
	}
	for _, p := range ext.Args {
		if !known[p] {
			registry.LogWarn("AWS profile '%s' not found in credentials or config", p)
		}
	}

	// Filter credentials file
	if _, err := os.Stat(credsFile); err == nil {
		filtered, err := filterINI(credsFile, profiles, nil)
		if err == nil && len(filtered) > 0 {
			dest := filepath.Join(staging, "credentials")
			if err := os.WriteFile(dest, filtered, 0600); err != nil {
				return fmt.Errorf("writing filtered credentials: %w", err)
			}
		}
	}

	// Filter config file (uses [profile name] format)
	if _, err := os.Stat(configFile); err == nil {
		filtered, err := filterINI(configFile, profiles, func(h string) string {
			if h != "default" {
				h = strings.TrimPrefix(h, "profile ")
			}
			return h
		})
		if err == nil && len(filtered) > 0 {
			dest := filepath.Join(staging, "config")
			if err := os.WriteFile(dest, filtered, 0600); err != nil {
				return fmt.Errorf("writing filtered config: %w", err)
			}
		}
	}

	// Check if any profile uses SSO (has sso_ keys) and copy SSO cache
	if _, err := os.Stat(configFile); err == nil {
		if profilesUseSSO(configFile, profiles) {
			ssoDir := filepath.Join(awsDir, "sso", "cache")
			if info, err := os.Stat(ssoDir); err == nil && info.IsDir() {
				destSSO := filepath.Join(staging, "sso", "cache")
				if err := os.MkdirAll(filepath.Dir(destSSO), 0755); err == nil {
					if err := fileutil.CopyDir(ssoDir, destSSO); err != nil {
						registry.LogWarn("Failed to copy SSO cache: %v", err)
					}
				}
			}
		}
	}

	// Copy CLI cache if it exists (for cached credentials)
	cliCache := filepath.Join(awsDir, "cli", "cache")
	if info, err := os.Stat(cliCache); err == nil && info.IsDir() {
		destCache := filepath.Join(staging, "cli", "cache")
		if err := os.MkdirAll(filepath.Dir(destCache), 0755); err == nil {
			if err := fileutil.CopyDir(cliCache, destCache); err != nil {
				registry.LogWarn("Failed to copy CLI cache: %v", err)
			}
		}
	}

	*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", staging+":"+credStagingPath+":ro")
	*ctx.CredStagingDirs = append(*ctx.CredStagingDirs, credStagingPath+":.aws")
	registry.LogInfo("AWS profiles: %s", strings.Join(ext.Args, ", "))
	return nil
}

// ---------------------------------------------------------------------------
// INI helpers
// ---------------------------------------------------------------------------

var (
	sectionRe       = regexp.MustCompile(`^\[(.+?)\]\s*$`)
	sourceProfileRe = regexp.MustCompile(`^\s*source_profile\s*=\s*(.+?)\s*$`)
	ssoKeyRe        = regexp.MustCompile(`^\s*sso_`)
	regionRe        = regexp.MustCompile(`^\s*region\s*=\s*(\S+)\s*$`)
	ssoStartURLRe   = regexp.MustCompile(`^\s*sso_start_url\s*=\s*(\S+)\s*$`)
)

// listINISections parses an INI file (credentials format) and returns section
// names. Section headers are used as-is (e.g. [default] -> "default").
func listINISections(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := sectionRe.FindStringSubmatch(scanner.Text()); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

// listINISectionsConfig parses an INI file (config format) and returns
// profile names. Strips the "profile " prefix (except for [default]).
func listINISectionsConfig(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := sectionRe.FindStringSubmatch(scanner.Text()); m != nil {
			header := m[1]
			if header != "default" {
				header = strings.TrimPrefix(header, "profile ")
			}
			names = append(names, header)
		}
	}
	return names
}

// filterINI reads an INI file and returns only the sections whose names match
// the wanted list. Lines before the first section (header comments) are
// preserved. If headerNorm is non-nil it is applied to each section header
// before matching (used by config-format files to strip "profile " prefixes).
func filterINI(path string, sections []string, headerNorm func(string) string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	wanted := make(map[string]bool, len(sections))
	for _, s := range sections {
		wanted[s] = true
	}

	var out strings.Builder
	printing := false
	seenSection := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			seenSection = true
			header := m[1]
			if headerNorm != nil {
				header = headerNorm(header)
			}
			printing = wanted[header]
		}
		// Lines before the first section are header lines; always include them
		if !seenSection || printing {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// checkSourceProfiles parses the config INI and for each selected profile
// checks if it has a source_profile = X line. If X is not in the wanted set,
// it is added to the returned list and a warning is printed.
func checkSourceProfiles(configPath string, profiles []string) []string {
	f, err := os.Open(configPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	wanted := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		wanted[p] = true
	}

	sourceRe := sourceProfileRe
	currentSection := ""
	var extra []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			header := m[1]
			if header != "default" {
				header = strings.TrimPrefix(header, "profile ")
			}
			currentSection = header
			continue
		}
		if wanted[currentSection] {
			if m := sourceRe.FindStringSubmatch(line); m != nil {
				sourceProfile := m[1]
				if !wanted[sourceProfile] {
					registry.LogWarn("Profile '%s' has source_profile '%s' which is not in the requested set", currentSection, sourceProfile)
					registry.LogWarn("  Including '%s' automatically. Add --aws %s explicitly to silence this.", sourceProfile, sourceProfile)
					wanted[sourceProfile] = true
					extra = append(extra, sourceProfile)
				}
			}
		}
	}
	return extra
}

// ---------------------------------------------------------------------------
// Firewall domain discovery
// ---------------------------------------------------------------------------

// regionServices lists the AWS service prefixes that get per-region endpoints.
var regionServices = []string{
	"sts", "s3", "ec2", "ssm", "eks", "ecr",
	"lambda", "logs", "monitoring",
}

// discoverFirewallDomains inspects ~/.aws/config and environment variables to
// build a list of AWS service endpoints that should be whitelisted. If no
// region can be discovered, it falls back to the `.amazonaws.com` wildcard.
// Global endpoints (sts.amazonaws.com, signin.aws.amazon.com) are always
// included in the YAML manifest, so they are not duplicated here.
func discoverFirewallDomains(home string) []string {
	regions := discoverRegions(home)
	ssoURLs := discoverSSOURLs(home)

	// No region discovered — fall back to wildcard so the user isn't blocked.
	if len(regions) == 0 {
		registry.LogWarn("aws: no region discovered, falling back to .amazonaws.com wildcard")
		return []string{".amazonaws.com"}
	}

	seen := make(map[string]bool)
	var domains []string
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}

	for _, region := range regions {
		for _, svc := range regionServices {
			add(svc + "." + region + ".amazonaws.com")
		}
	}

	for _, rawURL := range ssoURLs {
		u, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		host := u.Hostname()
		if host != "" {
			add(host)
		}
	}

	registry.LogInfo("aws: added %d firewall domain(s) for %d region(s)", len(domains), len(regions))
	return domains
}

// discoverRegions collects unique AWS region identifiers from the config file
// and environment variables.
func discoverRegions(home string) []string {
	seen := make(map[string]bool)
	var regions []string
	add := func(r string) {
		r = strings.TrimSpace(r)
		if r != "" && !seen[r] {
			seen[r] = true
			regions = append(regions, r)
		}
	}

	// Environment variables take priority.
	add(os.Getenv("AWS_REGION"))
	add(os.Getenv("AWS_DEFAULT_REGION"))

	// Parse ~/.aws/config for region = <value> lines.
	configPath := filepath.Join(home, ".aws", "config")
	f, err := os.Open(configPath)
	if err != nil {
		return regions
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := regionRe.FindStringSubmatch(scanner.Text()); m != nil {
			add(m[1])
		}
	}

	return regions
}

// discoverSSOURLs collects unique SSO start URLs from the config file and
// environment variables.
func discoverSSOURLs(home string) []string {
	seen := make(map[string]bool)
	var urls []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	// Environment variable.
	add(os.Getenv("AWS_SSO_START_URL"))

	// Parse ~/.aws/config for sso_start_url = <value> lines.
	configPath := filepath.Join(home, ".aws", "config")
	f, err := os.Open(configPath)
	if err != nil {
		return urls
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := ssoStartURLRe.FindStringSubmatch(scanner.Text()); m != nil {
			add(m[1])
		}
	}

	return urls
}

// profilesUseSSO checks whether any of the given profiles in the config file
// contain sso_ keys, indicating SSO-based authentication.
func profilesUseSSO(configPath string, profiles []string) bool {
	f, err := os.Open(configPath)
	if err != nil {
		return false
	}
	defer f.Close()

	wanted := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		wanted[p] = true
	}

	ssoRe := ssoKeyRe
	currentSection := ""
	inWanted := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if m := sectionRe.FindStringSubmatch(line); m != nil {
			header := m[1]
			if header != "default" {
				header = strings.TrimPrefix(header, "profile ")
			}
			currentSection = header
			inWanted = wanted[currentSection]
			continue
		}
		if inWanted && ssoRe.MatchString(line) {
			return true
		}
	}
	return false
}
