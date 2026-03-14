package registry

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
func Register(name string, r *Registration) {
	if _, exists := resolvers[name]; exists {
		panic(fmt.Sprintf("extension resolver already registered: %s", name))
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

// LoadExternalExtensions discovers external (subprocess-based) extensions
// from a directory. Each subdirectory must contain a "plugin" executable.
// Running `plugin manifest` should output JSON matching the Extension type.
func LoadExternalExtensions(dir string) ([]*Extension, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var exts []*Extension
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginPath := filepath.Join(dir, entry.Name(), "plugin")
		if _, err := os.Stat(pluginPath); err != nil {
			continue
		}
		out, err := exec.Command(pluginPath, "manifest").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mittens] Warning: external extension %q manifest failed: %v\n", entry.Name(), err)
			continue
		}
		var ext Extension
		if err := json.Unmarshal(out, &ext); err != nil {
			fmt.Fprintf(os.Stderr, "[mittens] Warning: external extension %q bad manifest: %v\n", entry.Name(), err)
			continue
		}
		// Capture for closures below; pluginPath changes each iteration.
		capturedPluginPath := pluginPath
		extName := ext.Name
		Register(extName, &Registration{
			List: func() ([]string, error) {
				out, err := exec.Command(capturedPluginPath, "list").Output()
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
				cmd := exec.Command(capturedPluginPath, "setup")
				cmd.Stdin = strings.NewReader(string(input))
				out, err := cmd.Output()
				if err != nil {
					return fmt.Errorf("plugin setup: %w", err)
				}
				var result SetupResult
				if err := json.Unmarshal(out, &result); err != nil {
					return fmt.Errorf("plugin setup output: %w", err)
				}
				// Apply result
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
		exts = append(exts, &ext)
	}
	return exts, nil
}
