package trivy

import (
	"os"
	"path/filepath"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func init() {
	registry.Register("trivy", &registry.Registration{
		Setup: setup,
	})
}

// setup ensures the Trivy cache directory exists on the host so the
// "when: dir_exists" mount condition in extension.yaml succeeds.
func setup(ctx *registry.SetupContext) error {
	return os.MkdirAll(filepath.Join(ctx.Home, ".cache", "trivy"), 0755)
}
