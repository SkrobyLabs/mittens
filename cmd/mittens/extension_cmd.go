package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

func runExtension(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: mittens extension {list|install|remove} [args]")
		return fmt.Errorf("missing subcommand")
	}
	switch args[0] {
	case "list":
		return extensionList()
	case "install":
		if len(args) < 2 {
			return fmt.Errorf("usage: mittens extension install <path-or-git-url>")
		}
		return extensionInstall(args[1])
	case "remove", "uninstall":
		if len(args) < 2 {
			return fmt.Errorf("usage: mittens extension remove <name>")
		}
		return extensionRemove(args[1])
	default:
		return fmt.Errorf("unknown extension subcommand: %s (available: list, install, remove)", args[0])
	}
}

func extensionList() error {
	home := homeDir()
	bundledDir := filepath.Join(runtimeRoot(), "extensions")
	userExtDir := filepath.Join(home, ".mittens", "extensions")
	exts, err := registry.LoadAllExtensions(bundledDir, userExtDir, extensionYAMLs)
	if err != nil {
		return err
	}

	var builtIn, user []*registry.Extension
	for _, ext := range exts {
		if strings.HasPrefix(ext.Source, "user") {
			user = append(user, ext)
		} else {
			builtIn = append(builtIn, ext)
		}
	}

	if len(builtIn) > 0 {
		fmt.Println("Built-in extensions:")
		for _, ext := range builtIn {
			resolverType := "yaml-only"
			if registry.GetSetupResolver(ext.Name) != nil {
				resolverType = "resolver"
			}
			fmt.Printf("  %-14s %-50s [%s]\n", ext.Name, ext.Description, resolverType)
		}
	}

	if len(user) > 0 {
		if len(builtIn) > 0 {
			fmt.Println()
		}
		fmt.Println("User-installed extensions:")
		for _, ext := range user {
			resolverType := "yaml-only"
			if registry.GetSetupResolver(ext.Name) != nil {
				resolverType = "plugin"
			}
			source := ext.Source
			if source == "user (overrides built-in)" {
				source = "overrides built-in"
			} else {
				source = ""
			}
			line := fmt.Sprintf("  %-14s %-50s [%s]", ext.Name, ext.Description, resolverType)
			if source != "" {
				line += fmt.Sprintf("  (%s)", source)
			}
			fmt.Println(line)
		}
	}

	if len(builtIn) == 0 && len(user) == 0 {
		fmt.Println("No extensions loaded.")
	}

	return nil
}

func extensionInstall(source string) error {
	home := homeDir()
	extBaseDir := filepath.Join(home, ".mittens", "extensions")
	if err := os.MkdirAll(extBaseDir, 0755); err != nil {
		return fmt.Errorf("creating extensions directory: %w", err)
	}

	// Determine source type: git URL vs local path.
	isGit := strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "git@") || strings.HasSuffix(source, ".git")

	tmpDir, err := os.MkdirTemp("", "mittens-ext-install.*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	srcDir := tmpDir
	if isGit {
		fmt.Printf("Cloning %s...\n", source)
		cmd := exec.Command("git", "clone", "--depth=1", source, tmpDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git clone failed: %w", err)
		}
	} else {
		// Local directory: copy to temp.
		absPath, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		if !fileutil.DirExists(absPath) {
			return fmt.Errorf("source directory does not exist: %s", absPath)
		}
		if err := fileutil.CopyDir(absPath, tmpDir); err != nil {
			return fmt.Errorf("copying extension: %w", err)
		}
	}

	// Validate: must have extension.yaml or plugin (at least one).
	hasYAML := fileutil.FileExists(filepath.Join(srcDir, "extension.yaml"))
	hasPlugin := fileutil.FileExists(filepath.Join(srcDir, "plugin"))
	if !hasYAML && !hasPlugin {
		return fmt.Errorf("invalid extension: must contain extension.yaml or plugin executable (or both)")
	}

	// Determine extension name from manifest.
	// Prefer YAML (no execution needed) over running the plugin binary.
	name := ""
	if hasYAML {
		data, err := os.ReadFile(filepath.Join(srcDir, "extension.yaml"))
		if err != nil {
			return fmt.Errorf("reading extension.yaml: %w", err)
		}
		var ext registry.Extension
		if err := yaml.Unmarshal(data, &ext); err != nil {
			return fmt.Errorf("bad extension.yaml: %w", err)
		}
		name = ext.Name
	}
	if name == "" && hasPlugin {
		out, err := exec.Command(filepath.Join(srcDir, "plugin"), "manifest").Output()
		if err != nil {
			logWarn("plugin manifest failed: %v (will use directory name)", err)
		} else {
			var ext registry.Extension
			if err := json.Unmarshal(out, &ext); err != nil {
				logWarn("bad plugin manifest: %v", err)
			} else {
				name = ext.Name
			}
		}
	}
	if name == "" {
		// Fall back to directory name.
		name = filepath.Base(source)
		name = strings.TrimSuffix(name, ".git")
		name = strings.TrimSuffix(name, "/")
	}

	// Validate build script naming.
	buildScript := filepath.Join(srcDir, "build.sh")
	hasBuild := fileutil.FileExists(buildScript)

	// Install: copy to ~/.mittens/extensions/<name>/
	destDir := filepath.Join(extBaseDir, name)
	if fileutil.DirExists(destDir) {
		_ = os.RemoveAll(destDir)
	}
	if err := fileutil.CopyDir(srcDir, destDir); err != nil {
		return fmt.Errorf("installing extension: %w", err)
	}

	fmt.Printf("Installed extension %q to %s\n", name, destDir)
	if hasBuild {
		fmt.Println("Note: This extension includes a build script. The Docker image will need to be rebuilt.")
	}
	return nil
}

func extensionRemove(name string) error {
	home := homeDir()
	extDir := filepath.Join(home, ".mittens", "extensions", name)
	if !fileutil.DirExists(extDir) {
		return fmt.Errorf("extension %q is not installed at %s", name, extDir)
	}

	hasBuild := fileutil.FileExists(filepath.Join(extDir, "build.sh"))

	if err := os.RemoveAll(extDir); err != nil {
		return fmt.Errorf("removing extension: %w", err)
	}

	fmt.Printf("Removed extension %q\n", name)
	if hasBuild {
		fmt.Println("Note: This extension had a build script. You may want to rebuild the Docker image.")
	}
	return nil
}
