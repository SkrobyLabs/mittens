package docker

import (
	"fmt"
	"os"

	"github.com/Skroby/mittens/extensions/registry"
)

func init() {
	registry.Register("docker", &registry.Registration{
		Setup: setup,
	})
}

func setup(ctx *registry.SetupContext) error {
	mode := ""
	if len(ctx.Extension.Args) > 0 {
		mode = ctx.Extension.Args[0]
	}
	switch mode {
	case "dind":
		*ctx.DockerArgs = append(*ctx.DockerArgs,
			"--privileged",
			"-v", ctx.ContainerName+"-docker:/var/lib/docker",
			"-e", "MITTENS_DIND=true",
		)
		fmt.Fprintln(os.Stderr, "[mittens] docker: dind mode — isolated Docker daemon (--privileged)")
	case "host":
		*ctx.DockerArgs = append(*ctx.DockerArgs,
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			"-e", "MITTENS_DOCKER_HOST=true",
		)
		fmt.Fprintln(os.Stderr, "[mittens] docker: host mode — sharing host Docker socket")
	}
	return nil
}
