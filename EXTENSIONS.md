# mittens Extension System

mittens uses a pluggable extension system. There are two kinds of extensions:

- **Built-in** -- compiled into the binary, written in Go
- **External** -- standalone executables that speak a JSON subprocess protocol (any language)

## Distribution

The compiled `mittens` binary is **not self-contained**. It needs these files next to it (resolved via the binary's directory):

```
mittens                  # compiled binary
container/
  Dockerfile                 # base Docker image definition
  entrypoint.sh              # container entrypoint (root -> claude user)
  squid.conf                 # proxy config for firewall mode
  firewall.conf              # default domain whitelist
  mcp-domains.conf           # MCP server -> domain mappings
  clipboard-shim.sh          # xclip shim for clipboard images
  clipboard-sync.sh          # macOS clipboard polling
extensions/
  aws/build.sh               # build scripts are COPY'd into Docker
  azure/build.sh             #   and executed during `docker build`
  dotnet/build.sh
  gcp/build.sh
  go/build.sh
  kubectl/build.sh
```

The extension YAML manifests and Go resolver logic are embedded into the binary at compile time (`go:embed`), so they don't need to ship as files. Only `container/` and `extensions/*/build.sh` must be present at runtime.

## Built-in Extensions

Each built-in extension lives under `extensions/<name>/` and consists of up to three parts:

### 1. YAML Manifest (`extension.yaml`)

Defines the extension's metadata, CLI flags, Docker mounts, environment variables, firewall domains, and build configuration. Embedded into the binary at compile time.

```yaml
name: ssh
description: Mount SSH keys (~/.ssh) into the container

flags:
  - name: "--ssh"
    description: "Mount SSH keys (read-only)"
    arg: "none"                 # none | csv | enum | path

mounts:
  - src: "~/.ssh"              # ~ expands to $HOME
    dst: "/home/claude/.ssh"
    mode: "ro"                  # ro | rw
    when: "dir_exists"          # dir_exists | file_exists | (empty = always)
    env:                        # env vars set when this mount is active
      SSH_AUTH_SOCK: "/ssh-agent"

firewall:                       # domains added to whitelist when enabled
  - "sts.amazonaws.com"

env:                            # env vars always set when enabled
  MITTENS_FIREWALL: "true"

capabilities:                   # Linux capabilities added to the container
  - "NET_ADMIN"

default_on: false               # true = enabled unless --no-<name> is used

build:
  script: "build.sh"            # executed inside Docker during image build
  image_tag: "dotnet{{.Arg}}"   # Go template, contributes to image tag
  args:                         # Docker build-args, values are Go templates
    DOTNET_CHANNEL: "{{if .Arg}}{{.Arg}}.0{{else}}LTS{{end}}"
```

**Flag argument types:**

| Type   | Behaviour | Example |
|--------|-----------|---------|
| `none` | Boolean toggle, no value consumed | `--ssh` |
| `csv`  | Consumes next arg, splits by comma | `--aws prod,staging` |
| `enum` | Consumes next arg if it matches `enum_values`, otherwise just enables | `--dotnet 9` or `--dotnet` |
| `path` | Consumes next arg as a file path | `--firewall /path/to/custom.conf` |

Flags prefixed with `--no-` (e.g. `--no-firewall`) disable the extension.

### 2. Go Resolver (`resolver.go`) -- optional

For extensions that need logic beyond what YAML can express (credential filtering, config file manipulation, dynamic firewall rules), a Go resolver handles list and setup operations.

```go
package aws

import "github.com/SkrobyLabs/mittens/extensions/registry"

func init() {
    registry.Register("aws", &registry.Registration{
        List:  listProfiles,   // returns available items for wizard selection
        Setup: setup,          // runs before container launch
    })
}

func listProfiles() ([]string, error) {
    // Parse ~/.aws/credentials and ~/.aws/config
    // Return profile names
}

func setup(ctx *registry.SetupContext) error {
    // ctx.Extension.Args     -- selected profiles (from --aws prod,staging)
    // ctx.Extension.AllMode  -- true if --aws-all was used
    // ctx.Home               -- user's home directory
    // ctx.StagingDir         -- temp dir for this extension (cleaned up after)
    // ctx.DockerArgs          -- append -v, -e flags here
    // ctx.FirewallExtra      -- append extra domains here
    // ctx.TempDirs           -- register additional temp dirs for cleanup
}
```

Extensions self-register via Go's `init()` mechanism. The main package uses blank imports to trigger registration:

```go
import (
    _ "github.com/SkrobyLabs/mittens/extensions/aws"
    _ "github.com/SkrobyLabs/mittens/extensions/gcp"
)
```

**Current built-in extensions with resolvers:** aws, azure, gcp, kubectl, mcp, firewall

**Current built-in extensions without resolvers (YAML-only):** ssh, gh, dotnet, go

### 3. Build Script (`build.sh`) -- optional

A shell script executed inside the Docker image during `docker build`. Used to install CLI tools (AWS CLI, Azure CLI, kubectl, .NET SDK, Go SDK, etc.).

The script path is declared in the YAML manifest under `build.script`. The Dockerfile iterates over enabled extensions and runs their build scripts:

```dockerfile
COPY extensions/ /tmp/ext-builds/
ARG INSTALL_EXTENSIONS=""
RUN if [ -n "$INSTALL_EXTENSIONS" ]; then \
    IFS=','; for ext in $INSTALL_EXTENSIONS; do \
        script="/tmp/ext-builds/${ext}/build.sh"; \
        if [ -f "$script" ]; then bash "$script" || exit 1; fi; \
    done; fi
```

## External Extensions (Subprocess Protocol)

External extensions are standalone executables that communicate via JSON over stdin/stdout. They don't require recompiling mittens.

### Installation

Place your plugin executable at:

```
~/.mittens/extensions/<name>/plugin
```

mittens discovers these at startup via `LoadExternalExtensions()`.

### Protocol

The `plugin` executable must handle three subcommands:

#### `plugin manifest`

Returns the extension definition as JSON (same schema as the YAML manifest).

```
$ ./plugin manifest
{
  "name": "redis",
  "description": "Mount Redis credentials",
  "flags": [
    {"name": "--redis", "description": "Select Redis instances", "arg": "csv"},
    {"name": "--redis-all", "description": "Mount all instances", "arg": "none"}
  ],
  "firewall": ["redis.example.com"]
}
```

#### `plugin list`

Returns available items as a JSON string array. Used by the wizard for selection UI.

```
$ ./plugin list
["prod", "staging", "local"]
```

#### `plugin setup`

Reads a JSON context object from **stdin**, writes a `SetupResult` to **stdout**.

**Input (stdin):**
```json
{
  "args": ["prod", "staging"],
  "all_mode": false,
  "home": "/Users/you",
  "staging": "/tmp/mittens-redis-abc123"
}
```

**Output (stdout):**
```json
{
  "mounts": [
    {
      "src": "/tmp/mittens-redis-abc123/config.json",
      "dst": "/home/claude/.redis/config.json",
      "mode": "ro",
      "env": {}
    }
  ],
  "env": {
    "REDIS_INSTANCES": "prod,staging"
  },
  "firewall_extra": [
    "redis.prod.example.com"
  ],
  "docker_args": []
}
```

| Field | Type | Purpose |
|-------|------|---------|
| `mounts` | `[{src, dst, mode, env}]` | Bind mounts added to `docker run` |
| `env` | `{key: value}` | Environment variables passed to the container |
| `firewall_extra` | `[string]` | Additional domains for the firewall whitelist |
| `docker_args` | `[string]` | Raw flags appended to `docker run` |

### Example

See `examples/redis-extension/plugin` for a complete Python implementation demonstrating all three protocol commands.

## Extension Lifecycle

```
1. Binary starts
2. LoadExtensions() reads embedded YAML manifests
3. init() registers Go resolvers for built-in extensions
4. LoadExternalExtensions() discovers ~/.mittens/extensions/*/plugin
   - Calls `plugin manifest` for each, registers subprocess resolvers
5. CLI flags parsed -- extensions claim their flags, set Enabled/Args/AllMode
6. For each enabled extension with a setup resolver:
   a. Create temp staging directory
   b. Call setup resolver (Go function or `plugin setup` subprocess)
   c. Resolver appends docker args, firewall domains
7. Docker image built (extensions with build scripts get installed)
8. Container launched with all mounts, env vars, capabilities
9. On exit: cleanup staging dirs, extract credentials, remove container
```
