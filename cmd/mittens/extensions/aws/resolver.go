// Package aws implements the AWS credential resolver for mittens.
// It registers itself with the registry at init time.
package aws

import (
	"bufio"
	"fmt"
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
func listProfiles() ([]string, error) {
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
	return profiles, nil
}

// ---------------------------------------------------------------------------
// Setup resolver
// ---------------------------------------------------------------------------

// setup mounts AWS credentials into the container, optionally filtered to
// only the requested profiles.
func setup(ctx *registry.SetupContext) error {
	ext := ctx.Extension
	home := ctx.Home
	awsDir := filepath.Join(home, ".aws")

	// --aws-all: mount entire directory
	if ext.AllMode {
		if info, err := os.Stat(awsDir); err == nil && info.IsDir() {
			*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", awsDir+":/home/claude/.aws:ro")
			fmt.Fprintf(os.Stderr, "[mittens] Mounting AWS credentials (all profiles)\n")
		} else {
			fmt.Fprintf(os.Stderr, "[mittens] WARN: AWS credentials requested but %s does not exist\n", awsDir)
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
			fmt.Fprintf(os.Stderr, "[mittens] WARN: AWS profile '%s' not found in credentials or config\n", p)
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
						fmt.Fprintf(os.Stderr, "[mittens] WARN: Failed to copy SSO cache: %v\n", err)
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
				fmt.Fprintf(os.Stderr, "[mittens] WARN: Failed to copy CLI cache: %v\n", err)
			}
		}
	}

	*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", staging+":/home/claude/.aws:ro")
	fmt.Fprintf(os.Stderr, "[mittens] AWS profiles: %s\n", strings.Join(ext.Args, ", "))
	return nil
}

// ---------------------------------------------------------------------------
// INI helpers
// ---------------------------------------------------------------------------

var (
	sectionRe       = regexp.MustCompile(`^\[(.+?)\]\s*$`)
	sourceProfileRe = regexp.MustCompile(`^\s*source_profile\s*=\s*(.+?)\s*$`)
	ssoKeyRe        = regexp.MustCompile(`^\s*sso_`)
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
					fmt.Fprintf(os.Stderr, "[mittens] WARN: Profile '%s' has source_profile '%s' which is not in the requested set\n", currentSection, sourceProfile)
					fmt.Fprintf(os.Stderr, "[mittens] WARN:   Including '%s' automatically. Add --aws %s explicitly to silence this.\n", sourceProfile, sourceProfile)
					wanted[sourceProfile] = true
					extra = append(extra, sourceProfile)
				}
			}
		}
	}
	return extra
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
