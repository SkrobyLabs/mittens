package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

// runDoctor reports environment health (Docker, runtime assets, broker
// transport) and migrates legacy per-project config into the structured
// policy.yaml format. It exits non-zero when any check fails.
func runDoctor(args []string) error {
	migrateAll := false
	for _, a := range args {
		switch a {
		case "--migrate-all":
			migrateAll = true
		case "--help", "-h":
			fmt.Println("Usage: mittens doctor [--migrate-all]")
			fmt.Println()
			fmt.Println("Checks Docker, runtime assets, and broker prerequisites, and migrates")
			fmt.Println("legacy project config to policy.yaml.")
			fmt.Println()
			fmt.Println("  --migrate-all   Convert every legacy project config under ~/.mittens/projects")
			return nil
		default:
			return fmt.Errorf("unknown flag %q for \"mittens doctor\" (supported: --migrate-all)", a)
		}
	}

	fmt.Println(colorCyan + "[mittens]" + colorReset + " Doctor")

	d := &doctorReport{}
	d.checkEnvironment()
	d.checkDocker()
	d.checkRuntime()
	d.checkPaths()

	exts, err := loadExtensions()
	if err != nil {
		d.fail("Extensions: failed to load: %v", err)
		exts = nil
	}

	if migrateAll {
		d.migrateAllProjects(exts)
	} else {
		d.checkProjectPolicy(exts)
	}

	fmt.Println()
	if d.problems == 0 {
		fmt.Println(colorGreen + "All checks passed." + colorReset)
		return nil
	}
	return fmt.Errorf("%d issue(s) found", d.problems)
}

// doctorReport tracks how many checks failed while printing each result.
type doctorReport struct {
	problems int
}

func (d *doctorReport) ok(format string, a ...interface{}) {
	fmt.Printf("  "+colorGreen+"✓"+colorReset+" "+format+"\n", a...)
}

func (d *doctorReport) warn(format string, a ...interface{}) {
	fmt.Printf("  "+colorYellow+"!"+colorReset+" "+format+"\n", a...)
}

func (d *doctorReport) fail(format string, a ...interface{}) {
	fmt.Printf("  "+colorRed("✗")+" "+format+"\n", a...)
	d.problems++
}

func (d *doctorReport) checkEnvironment() {
	d.ok("Platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	if isWSL() {
		d.ok("WSL environment detected")
	}
}

func (d *doctorReport) checkDocker() {
	if _, err := exec.LookPath("docker"); err != nil {
		d.fail("Docker CLI not found in PATH")
		return
	}
	out, err := captureCommand("docker", "info", "--format", "{{.ServerVersion}}")
	if err != nil || strings.TrimSpace(out) == "" {
		d.fail("Docker daemon not reachable — is Docker running?")
		return
	}
	d.ok("Docker daemon reachable (server %s)", strings.TrimSpace(out))
}

func (d *doctorReport) checkRuntime() {
	if root := os.Getenv(runtimeRootEnv); root != "" {
		if _, err := os.Stat(root); err != nil {
			d.fail("%s=%s but path is not accessible: %v", runtimeRootEnv, root, err)
			return
		}
		d.ok("Runtime root override: %s", root)
		return
	}
	root, err := embeddedRuntimeRoot()
	if err != nil {
		d.fail("Could not materialize embedded runtime assets: %v", err)
		return
	}
	d.ok("Runtime assets: %s", root)
}

func (d *doctorReport) checkPaths() {
	d.ok("Config home: %s", ConfigHome())
	if runtime.GOOS == "windows" || isWSL() {
		d.ok("Broker transport: TCP (host.docker.internal)")
	} else {
		d.ok("Broker transport: unix socket")
	}
	if UserDefaultsExist() {
		d.ok("User defaults: %s", UserDefaultsPath())
	} else {
		d.warn("No user defaults set (optional; run 'mittens init --defaults')")
	}
}

func (d *doctorReport) checkProjectPolicy(exts []*registry.Extension) {
	workspace := detectWorkspace()
	policy, source, err := LoadProjectPolicy(workspace, exts)
	switch {
	case err != nil:
		d.fail("Project policy: %v", err)
	case source == PolicySourceNone:
		d.ok("Project policy: none for this workspace (defaults apply)")
	case source == PolicySourceV2:
		d.ok("Project policy: policy.yaml (%s)", ProjectDir(workspace))
	case source == PolicySourceLegacy:
		if err := SaveProjectPolicy(workspace, policy); err != nil {
			d.fail("Project policy: legacy config found but migration failed: %v", err)
		} else {
			d.ok("Project policy: migrated legacy config → policy.yaml")
		}
	}
}

// migrateAllProjects converts every legacy `config` file under
// ~/.mittens/projects into a policy.yaml, leaving already-structured projects
// untouched. It works on project directories directly so it does not need the
// original workspace paths.
func (d *doctorReport) migrateAllProjects(exts []*registry.Extension) {
	projectsDir := filepath.Join(ConfigHome(), "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			d.ok("No projects to migrate")
			return
		}
		d.fail("Reading projects dir: %v", err)
		return
	}

	var migrated, skipped, failed int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(projectsDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "policy.yaml")); err == nil {
			skipped++
			continue
		}
		lines, err := readConfigLines(filepath.Join(dir, "config"))
		if err != nil {
			d.fail("%s: reading config: %v", e.Name(), err)
			failed++
			continue
		}
		legacyArgs := splitConfigFlags(lines)
		if len(legacyArgs) == 0 {
			continue
		}
		policy, err := PolicyFromLegacyFlags(legacyArgs, exts)
		if err != nil {
			d.fail("%s: converting legacy config: %v", e.Name(), err)
			failed++
			continue
		}
		if err := savePolicyFile(filepath.Join(dir, "policy.yaml"), policy); err != nil {
			d.fail("%s: writing policy.yaml: %v", e.Name(), err)
			failed++
			continue
		}
		migrated++
	}
	d.ok("Migrated %d legacy project config(s); %d already structured", migrated, skipped)
}
