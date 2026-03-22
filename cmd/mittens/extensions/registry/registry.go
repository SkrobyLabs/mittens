package registry

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ListResolver returns available values for an extension (e.g., AWS profile names).
type ListResolver func() ([]string, error)

// SetupResolver performs custom setup (filtered credential mounting, etc.).
type SetupResolver func(ctx *SetupContext) error

// Registration bundles resolvers for a named extension.
type Registration struct {
	List  ListResolver
	Setup SetupResolver
}

var resolvers = make(map[string]*Registration)

// Register adds resolvers for the given extension name.
// Called from init() in each extension package.
// If a resolver is already registered for this name (e.g. a user plugin
// overriding a built-in), the new registration replaces it with a warning.
func Register(name string, r *Registration) {
	if _, exists := resolvers[name]; exists {
		fmt.Fprintf(os.Stderr, "[mittens] Warning: overriding resolver for extension %q\n", name)
	}
	resolvers[name] = r
}

// GetListResolver returns the list resolver for the named extension, or nil.
func GetListResolver(name string) ListResolver {
	if r, ok := resolvers[name]; ok && r != nil {
		return r.List
	}
	return nil
}

// GetSetupResolver returns the setup resolver for the named extension, or nil.
func GetSetupResolver(name string) SetupResolver {
	if r, ok := resolvers[name]; ok && r != nil {
		return r.Setup
	}
	return nil
}

// LoadAllExtensions loads extensions from three sources in order:
// 1. Bundled extensions from disk (bundledDir/*/extension.yaml), falling back to embeddedFS
// 2. User extensions from userDir (extension.yaml and/or plugin executable)
// User extensions with the same name as a built-in override the built-in's YAML config.
// If the user extension has a plugin executable, its subprocess resolver replaces any Go resolver.
func LoadAllExtensions(bundledDir, userDir string, embeddedFS fs.FS) ([]*Extension, error) {
	byName := make(map[string]*Extension)

	// 1. Try loading bundled extensions from disk first.
	if diskExts, err := loadExtensionsFromDir(bundledDir); err == nil && len(diskExts) > 0 {
		for _, ext := range diskExts {
			ext.Source = "built-in"
			byName[ext.Name] = ext
		}
	} else {
		// Fall back to embedded FS.
		embeddedExts, err := LoadExtensions(embeddedFS)
		if err != nil {
			return nil, err
		}
		for _, ext := range embeddedExts {
			ext.Source = "built-in"
			byName[ext.Name] = ext
		}
	}

	// 2. Load user extensions (YAML-only and/or plugin-based).
	if entries, err := os.ReadDir(userDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			extDir := filepath.Join(userDir, entry.Name())
			yamlPath := filepath.Join(extDir, "extension.yaml")
			pluginPath := filepath.Join(extDir, "plugin")

			hasYAML := fileExists(yamlPath)
			hasPlugin := fileExists(pluginPath)
			if !hasYAML && !hasPlugin {
				continue
			}

			var ext *Extension

			// Load Extension struct: prefer YAML (no execution needed),
			// fall back to plugin manifest only when no YAML exists.
			if hasYAML {
				data, err := os.ReadFile(yamlPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[mittens] Warning: external extension %q: %v\n", entry.Name(), err)
					continue
				}
				var yamlExt Extension
				if err := yaml.Unmarshal(data, &yamlExt); err != nil {
					fmt.Fprintf(os.Stderr, "[mittens] Warning: external extension %q bad YAML: %v\n", entry.Name(), err)
					continue
				}
				ext = &yamlExt
			} else {
				// Plugin-only: must run plugin manifest to get the Extension struct.
				out, err := exec.Command(pluginPath, "manifest").Output()
				if err != nil {
					fmt.Fprintf(os.Stderr, "[mittens] Warning: external extension %q manifest failed: %v\n", entry.Name(), err)
					continue
				}
				var pluginExt Extension
				if err := json.Unmarshal(out, &pluginExt); err != nil {
					fmt.Fprintf(os.Stderr, "[mittens] Warning: external extension %q bad manifest: %v\n", entry.Name(), err)
					continue
				}
				ext = &pluginExt
			}

			// If a plugin executable exists, register subprocess resolvers.
			// This is best-effort: a failing plugin doesn't prevent the
			// extension from loading (YAML mounts/env/build still work).
			if hasPlugin {
				capturedPath := pluginPath
				extName := ext.Name
				Register(extName, &Registration{
					List: func() ([]string, error) {
						out, err := exec.Command(capturedPath, "list").Output()
						if err != nil {
							return nil, err
						}
						var items []string
						if err := json.Unmarshal(out, &items); err != nil {
							return nil, err
						}
						return items, nil
					},
					Setup: func(ctx *SetupContext) error {
						input, _ := json.Marshal(map[string]interface{}{
							"args":     ctx.Extension.Args,
							"all_mode": ctx.Extension.AllMode,
							"home":     ctx.Home,
							"staging":  ctx.StagingDir,
						})
						cmd := exec.Command(capturedPath, "setup")
						cmd.Stdin = strings.NewReader(string(input))
						out, err := cmd.Output()
						if err != nil {
							return fmt.Errorf("plugin setup: %w", err)
						}
						var result SetupResult
						if err := json.Unmarshal(out, &result); err != nil {
							return fmt.Errorf("plugin setup output: %w", err)
						}
						for _, m := range result.Mounts {
							*ctx.DockerArgs = append(*ctx.DockerArgs, "-v", fmt.Sprintf("%s:%s:%s", m.Src, m.Dst, m.Mode))
							for k, v := range m.Env {
								*ctx.DockerArgs = append(*ctx.DockerArgs, "-e", k+"="+v)
							}
						}
						for k, v := range result.Env {
							*ctx.DockerArgs = append(*ctx.DockerArgs, "-e", k+"="+v)
						}
						*ctx.FirewallExtra = append(*ctx.FirewallExtra, result.FirewallExtra...)
						*ctx.DockerArgs = append(*ctx.DockerArgs, result.DockerArgs...)
						return nil
					},
				})
			}

			if _, existed := byName[ext.Name]; existed {
				ext.Source = "user (overrides built-in)"
			} else {
				ext.Source = "user"
			}
			if ext.DefaultOn {
				ext.Enabled = true
			}
			byName[ext.Name] = ext
		}
	}

	// Collect and sort.
	var result []*Extension
	for _, ext := range byName {
		result = append(result, ext)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// loadExtensionsFromDir reads extension.yaml files from disk directories.
func loadExtensionsFromDir(dir string) ([]*Extension, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var exts []*Extension
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		yamlPath := filepath.Join(dir, entry.Name(), "extension.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			continue
		}
		var ext Extension
		if err := yaml.Unmarshal(data, &ext); err != nil {
			continue
		}
		if ext.DefaultOn {
			ext.Enabled = true
		}
		exts = append(exts, &ext)
	}
	return exts, nil
}

// ExtractUniqueHosts reads the file at path, applies the regex pattern to find
// URLs, parses them, and returns a deduplicated list of hostnames.
func ExtractUniqueHosts(path, pattern string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(string(data), -1)

	seen := make(map[string]bool)
	var hosts []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		u, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		host := u.Hostname()
		if host != "" && !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LoadExtensions reads all extension.yaml files from the provided embedded FS.
// The FS should contain paths like "extensions/<name>/extension.yaml".
func LoadExtensions(fsys fs.FS) ([]*Extension, error) {
	matches, err := fs.Glob(fsys, "extensions/*/extension.yaml")
	if err != nil {
		return nil, err
	}
	var exts []*Extension
	for _, path := range matches {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var ext Extension
		if err := yaml.Unmarshal(data, &ext); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		// Enable extensions that are on by default.
		if ext.DefaultOn {
			ext.Enabled = true
		}
		exts = append(exts, &ext)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i].Name < exts[j].Name })
	return exts, nil
}
