# mittens Extension System

mittens uses a pluggable extension system. There are two kinds of extensions:

- **Built-in** -- compiled into the binary, written in Go
- **External** -- standalone executables that speak a JSON subprocess protocol (any language)

## Distribution

The compiled `mittens` binary is **not self-contained**. It needs these files next to it (resolved via the binary's directory):

```
mittens                          # compiled binary
cmd/mittens/container/
  Dockerfile                     # base Docker image definition
  mittens-init                   # container entrypoint binary (built from cmd/mittens-init)
  firewall.conf                  # default domain whitelist
  firewall-dev.conf              # developer-friendly whitelist superset
  mcp-domains.conf               # MCP server -> domain mappings
  clipboard-sync.sh              # macOS host-side clipboard polling
cmd/mittens/extensions/
  aws/build.sh                   # build scripts are COPY'd into Docker
  azure/build.sh                 #   and executed during `docker build`
  dotnet/build.sh
  gcp/build.sh
  go/build.sh
  kubectl/build.sh
```

The `mittens-init` binary is the container entrypoint — it handles root-level setup (firewall proxy, iptables, DinD, privilege drop), user-level setup (config staging, JSON settings, credential sync), and provides busybox-style symlink dispatch for `xdg-open`, `xclip`, and `notify.sh`.

The extension YAML manifests and Go resolver logic are embedded into the binary at compile time (`go:embed`), so they don't need to ship as files. Only `cmd/mittens/container/` and `cmd/mittens/extensions/*/build.sh` must be present at runtime.

## Built-in Extensions

Each built-in extension lives under `cmd/mittens/extensions/<name>/` and consists of up to three parts:

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

import "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"

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
    _ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/aws"
    _ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/gcp"
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

Use the `mittens extension` command to install, list, or remove extensions:

```bash
# Install from a local directory
mittens extension install ./examples/redis-extension/

# Install from a git repository
mittens extension install https://github.com/user/mittens-ext-redis.git

# List all loaded extensions (built-in + user-installed)
mittens extension list

# Remove a user-installed extension
mittens extension remove redis
```

Extensions are installed to `~/.mittens/extensions/<name>/`. Each extension directory can contain:

- `extension.yaml` — YAML manifest (defines flags, mounts, env, firewall, build config)
- `plugin` — executable implementing the subprocess protocol (for custom resolver logic)
- `build.sh` — shell script run during `docker build` to install tools in the container

An extension needs at least one of `extension.yaml` or `plugin`. YAML-only extensions work for simple mount/env/build configurations. Add a `plugin` executable when you need custom logic (credential filtering, dynamic firewall rules, etc.).

mittens discovers user-installed extensions at startup alongside built-in ones.

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
2. init() registers Go resolvers for built-in extensions
3. LoadAllExtensions() loads extension YAMLs:
   a. Bundled: from disk (next to binary) first, go:embed fallback
   b. User: from ~/.mittens/extensions/ (YAML-only or plugin-based)
   c. User extensions with the same name shadow built-in ones
   d. Plugin-based extensions register subprocess resolvers
4. CLI flags parsed -- extensions claim their flags, set Enabled/Args/AllMode
5. For each enabled extension with a setup resolver:
   a. Create temp staging directory
   b. Call setup resolver (Go function or `plugin setup` subprocess)
   c. Resolver appends docker args, firewall domains
6. Docker image built:
   a. If external extensions have build.sh, a temp build context is created
      with both built-in and external extension directories
   b. Extensions with build scripts get installed via INSTALL_EXTENSIONS
7. Container launched with all mounts, env vars, capabilities
8. On exit: cleanup staging dirs, extract credentials, remove container
```
