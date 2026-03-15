package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Extension represents a parsed extension definition (from YAML manifest).
type Extension struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Flags        []ExtensionFlag   `yaml:"flags"`
	Firewall     []string          `yaml:"firewall"`
	Mounts       []MountConfig     `yaml:"mounts"`
	Env          map[string]string `yaml:"env"`
	Build        *BuildConfig      `yaml:"build"`
	Capabilities []string          `yaml:"capabilities"`
	DefaultOn    bool              `yaml:"default_on"`

	// Runtime state (not from YAML)
	Enabled bool     `yaml:"-"`
	Args    []string `yaml:"-"` // csv values or enum choice
	RawArg  string   `yaml:"-"` // first arg as string (for templates)
	AllMode bool     `yaml:"-"` // --ext-all was used
	Source  string   `yaml:"-" json:"source,omitempty"` // "built-in" or "user"
}

// ExtensionFlag describes a CLI flag contributed by an extension.
type ExtensionFlag struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Arg         string   `yaml:"arg"`         // none, csv, enum, path
	EnumValues  []string `yaml:"enum_values"` // for enum arg type
	Multi       bool     `yaml:"multi"`       // allow comma-separated enum values
}

// MountConfig describes a bind mount an extension needs.
type MountConfig struct {
	Src  string            `yaml:"src"`
	Dst  string            `yaml:"dst"`
	Mode string            `yaml:"mode"` // ro, rw
	When string            `yaml:"when"` // dir_exists, file_exists
	Env  map[string]string `yaml:"env"`  // env vars to set when mount exists
}

// BuildConfig describes custom Docker build steps for an extension.
type BuildConfig struct {
	Script   string            `yaml:"script"`    // script filename (relative to extension dir)
	ImageTag string            `yaml:"image_tag"` // go template: "dotnet{{.Arg}}"
	Args     map[string]string `yaml:"args"`      // build args, values are go templates
}

// ResolvedMount is a mount with paths expanded and conditions already checked.
type ResolvedMount struct {
	Src  string
	Dst  string
	Mode string
	Env  map[string]string
}

// SetupContext is passed to setup resolvers.
type SetupContext struct {
	Home          string
	ContainerName string    // docker container name for this invocation
	Extension     *Extension
	DockerArgs    *[]string // append docker run flags
	FirewallExtra *[]string // append extra domains
	TempDirs      *[]string // track temp dirs for cleanup
	StagingDir    string    // pre-created temp dir for this extension
}

// SetupResult is returned by external (subprocess) plugins.
type SetupResult struct {
	Mounts        []ResolvedMount   `json:"mounts"`
	Env           map[string]string `json:"env"`
	FirewallExtra []string          `json:"firewall_extra"`
	DockerArgs    []string          `json:"docker_args"`
}

// ParseFlag tries to match the given args against one of the extension's flags.
// It returns the number of arguments consumed and whether a match was found.
// For "none" or absent arg type, it is a boolean toggle (consumed=1).
// For "csv", it consumes the next arg, splits by comma, and appends to Args (consumed=2).
// For "enum", it consumes the next arg and validates against EnumValues (consumed=2).
// For "path", it consumes the next arg as-is (consumed=2).
// If the flag name ends with "-all" (like --aws-all), AllMode is set to true (consumed=1).
func (e *Extension) ParseFlag(args []string) (consumed int, matched bool) {
	if len(args) == 0 {
		return 0, false
	}

	for _, flag := range e.Flags {
		if args[0] != flag.Name {
			continue
		}

		switch flag.Arg {
		case "none", "":
			// Flags starting with --no- disable the extension.
			if strings.HasPrefix(flag.Name, "--no-") {
				e.Enabled = false
				return 1, true
			}
			e.Enabled = true
			// Check if the flag name ends with "-all"
			if strings.HasSuffix(flag.Name, "-all") {
				e.AllMode = true
			}
			return 1, true

		case "csv":
			if len(args) < 2 {
				return 0, false
			}
			e.Enabled = true
			e.RawArg = args[1]
			e.Args = append(e.Args, strings.Split(args[1], ",")...)
			return 2, true

		case "enum":
			e.Enabled = true
			// Enum arg is optional: if next arg is a valid value, consume it.
			// When multi=true, comma-separated lists of valid values are accepted.
			// Otherwise just enable the extension with no specific value.
			if len(args) >= 2 {
				val := args[1]
				var parts []string
				if flag.Multi {
					parts = strings.Split(val, ",")
				} else {
					parts = []string{val}
				}
				allValid := true
				for _, p := range parts {
					found := false
					for _, ev := range flag.EnumValues {
						if ev == p {
							found = true
							break
						}
					}
					if !found {
						allValid = false
						break
					}
				}
				if allValid {
					e.RawArg = val
					e.Args = parts
					return 2, true
				}
			}
			return 1, true

		case "path":
			if len(args) < 2 {
				return 0, false
			}
			e.Enabled = true
			e.RawArg = args[1]
			e.Args = []string{args[1]}
			return 2, true
		}
	}
	return 0, false
}

// ImageTagPart returns the image tag component for this extension,
// resolving {{.Arg}} placeholders with the raw argument value.
// Returns "" if Build is nil or ImageTag is empty.
func (e *Extension) ImageTagPart() string {
	if e.Build == nil || e.Build.ImageTag == "" {
		return ""
	}
	tag := strings.ReplaceAll(e.Build.ImageTag, "{{.Arg}}", e.RawArg)
	// Sanitize comma-separated args for Docker tag compatibility.
	return strings.ReplaceAll(tag, ",", "-")
}

// BuildArgs returns the resolved build arguments, expanding Go templates
// in values. Supports {{.Arg}} replacement and {{if .Arg}}...{{else}}...{{end}}.
// Returns nil if Build is nil or has no Args.
func (e *Extension) BuildArgs() map[string]string {
	if e.Build == nil || len(e.Build.Args) == 0 {
		return nil
	}

	data := struct{ Arg string }{Arg: e.RawArg}
	resolved := make(map[string]string, len(e.Build.Args))

	for k, v := range e.Build.Args {
		// If the value contains template directives, use text/template.
		if strings.Contains(v, "{{") {
			tmpl, err := template.New(k).Parse(v)
			if err != nil {
				// Fall back to simple replacement on parse error.
				resolved[k] = strings.ReplaceAll(v, "{{.Arg}}", e.RawArg)
				continue
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				resolved[k] = strings.ReplaceAll(v, "{{.Arg}}", e.RawArg)
				continue
			}
			resolved[k] = buf.String()
		} else {
			resolved[k] = v
		}
	}
	return resolved
}

// ExpandedMounts returns mounts with ~ expanded to the given home directory
// and conditional mounts filtered by existence checks. Mounts whose "when"
// condition is not satisfied are excluded.
func (e *Extension) ExpandedMounts(home string) []ResolvedMount {
	var result []ResolvedMount
	for _, m := range e.Mounts {
		src := expandTilde(m.Src, home)
		dst := expandTilde(m.Dst, home)

		switch m.When {
		case "dir_exists":
			info, err := os.Stat(src)
			if err != nil || !info.IsDir() {
				continue
			}
		case "file_exists":
			info, err := os.Stat(src)
			if err != nil || info.IsDir() {
				continue
			}
		}

		mode := m.Mode
		if mode == "" {
			mode = "ro"
		}

		result = append(result, ResolvedMount{
			Src:  src,
			Dst:  dst,
			Mode: mode,
			Env:  m.Env,
		})
	}
	return result
}

// FirewallDomains returns a copy of the extension's firewall domain list.
func (e *Extension) FirewallDomains() []string {
	if len(e.Firewall) == 0 {
		return nil
	}
	domains := make([]string, len(e.Firewall))
	copy(domains, e.Firewall)
	return domains
}

// expandTilde replaces a leading ~ with the provided home directory.
func expandTilde(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
