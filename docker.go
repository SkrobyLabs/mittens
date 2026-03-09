package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/Skroby/mittens/extensions/registry"
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

// BuildImage runs `docker build` with the given context and returns any error.
func BuildImage(ctx BuildContext) error {
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

// CurrentUserIDs returns the current user's UID and GID.
// On Windows (where os.Getuid() returns -1), it returns 1000, 1000 as defaults.
func CurrentUserIDs() (uid int, gid int) {
	if runtime.GOOS == "windows" {
		return 1000, 1000
	}
	return os.Getuid(), os.Getgid()
}
