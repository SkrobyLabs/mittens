package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

// BuildContext holds the parameters needed to build a Docker image.
type BuildContext struct {
	ContextDir     string               // project root (docker build context)
	Dockerfile     string               // path to Dockerfile (relative to ContextDir or absolute)
	ImageName      string               // e.g. "mittens"
	ImageTag       string               // e.g. "latest" or "aws-kubectl"
	UserID         int                  // host UID to bake into the image
	GroupID        int                  // host GID to bake into the image
	Extensions     []*registry.Extension // enabled extensions with build configs
	ExtraBuildArgs map[string]string    // additional --build-arg key=value pairs (e.g. provider args)
	Verbose        bool                 // pass --progress=plain to docker build
}

// ensureBaseImages reads the Dockerfile, extracts all FROM images, and pulls
// any that are not available locally. This works around Docker Desktop / WSL2
// environments where BuildKit cannot execute the credential helper
// (docker-credential-desktop.exe) but `docker pull` from the host CLI works.
func ensureBaseImages(dockerfile string) {
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		return
	}

	// Collect named build stages so we can skip "FROM stagename" references.
	stages := map[string]bool{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "FROM ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		// Record "AS <name>" aliases for later FROM references.
		for i, p := range parts {
			if strings.EqualFold(p, "AS") && i+1 < len(parts) {
				stages[strings.ToLower(parts[i+1])] = true
			}
		}

		image := parts[1]

		// Skip build-arg references (e.g. FROM ${BASE_IMAGE})
		if strings.Contains(image, "$") {
			continue
		}
		// Skip references to earlier build stages (e.g. FROM builder)
		if stages[strings.ToLower(image)] {
			continue
		}
		// Already available locally — nothing to do
		if exec.Command("docker", "image", "inspect", image).Run() == nil {
			continue
		}

		fmt.Printf("[mittens] Pulling base image: %s\n", image)
		cmd := exec.Command("docker", "pull", image)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run() // best-effort; build will report its own error if still missing
	}
}

// platformPreBuildHook is called before `docker build` to perform platform-specific
// setup. On Windows, it pre-pulls base images to work around BuildKit credential
// helper issues. Default is a no-op.
var platformPreBuildHook = func(dockerfile string) {}

// BuildImage runs `docker build` with the given context and returns any error.
func BuildImage(ctx BuildContext) error {
	if ctx.Dockerfile != "" {
		platformPreBuildHook(ctx.Dockerfile)
	}

	args := []string{"build"}
	if ctx.Verbose {
		// --progress=plain requires BuildKit. Check if buildx is available
		// to avoid failing on the legacy builder.
		if err := exec.Command("docker", "buildx", "version").Run(); err == nil {
			args = append(args, "--progress=plain")
		}
	}

	// Dockerfile path
	if ctx.Dockerfile != "" {
		args = append(args, "-f", ctx.Dockerfile)
	}

	// User/group build args
	args = append(args, "--build-arg", fmt.Sprintf("USER_ID=%d", ctx.UserID))
	args = append(args, "--build-arg", fmt.Sprintf("GROUP_ID=%d", ctx.GroupID))

	// Collect extension names that have build scripts for INSTALL_EXTENSIONS
	var installNames []string
	for _, ext := range ctx.Extensions {
		if ext.Build != nil && ext.Build.Script != "" {
			installNames = append(installNames, ext.Name)
		}
	}
	if len(installNames) > 0 {
		args = append(args, "--build-arg", "INSTALL_EXTENSIONS="+strings.Join(installNames, ","))
	}

	// Extension-specific build args
	for _, ext := range ctx.Extensions {
		for k, v := range ext.BuildArgs() {
			args = append(args, "--build-arg", k+"="+v)
		}
	}

	// Extra build args (e.g. provider-specific).
	for k, v := range ctx.ExtraBuildArgs {
		args = append(args, "--build-arg", k+"="+v)
	}

	// Tag and context
	args = append(args, "-t", ctx.ImageName+":"+ctx.ImageTag)
	args = append(args, ctx.ContextDir)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}
	return nil
}

// RunContainer executes `docker run` with the provided arguments and returns
// the container's exit code. The binary parameter specifies the AI CLI binary
// name to invoke (e.g. "claude").
func RunContainer(args []string, imageName, imageTag string, shell bool, binary string, cliArgs []string, stdin *os.File) (int, error) {
	dockerArgs := []string{"run"}
	dockerArgs = append(dockerArgs, args...)
	dockerArgs = append(dockerArgs, imageName+":"+imageTag)

	if shell {
		dockerArgs = append(dockerArgs, "/bin/bash")
	} else {
		dockerArgs = append(dockerArgs, binary)
		dockerArgs = append(dockerArgs, cliArgs...)
	}

	if stdin == nil {
		stdin = os.Stdin
	}

	cmd := exec.Command("docker", dockerArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return 0, nil
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// ExtractCredentials copies the credential file out of a stopped container.
// containerCredPath is the full path to the credential file inside the container.
func ExtractCredentials(containerName, containerCredPath, destPath string) error {
	cmd := exec.Command("docker", "cp",
		containerName+":"+containerCredPath,
		destPath,
	)
	_ = cmd.Run()
	return nil
}

// InspectContainerRunning checks if a container exists and whether it is running.
// Returns (exists, running). If the container does not exist, both are false.
func InspectContainerRunning(containerName string) (exists bool, running bool) {
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", containerName).Output()
	if err != nil {
		return false, false
	}
	return true, strings.TrimSpace(string(out)) == "true"
}

// RemoveContainer force-removes a container by name.
func RemoveContainer(containerName string) error {
	cmd := exec.Command("docker", "rm", "-f", containerName)
	_ = cmd.Run()
	return nil
}

// ComputeImageTag collects image tag parts from enabled extensions,
// sorts them, and joins with "-". Returns "latest" if empty.
func ComputeImageTag(extensions []*registry.Extension) string {
	var parts []string
	for _, ext := range extensions {
		if !ext.Enabled {
			continue
		}
		part := ext.ImageTagPart()
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "latest"
	}
	sort.Strings(parts)
	return strings.Join(parts, "-")
}

// platformCurrentUserIDs returns the current user's UID and GID.
// On Windows (where os.Getuid() returns -1), the _windows.go init() overrides
// this to return 1000, 1000.
var platformCurrentUserIDs = func() (int, int) {
	return os.Getuid(), os.Getgid()
}

// CurrentUserIDs returns the current user's UID and GID.
func CurrentUserIDs() (uid int, gid int) {
	return platformCurrentUserIDs()
}
