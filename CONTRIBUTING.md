# Contributing

Contributions are welcome. Please follow the process below.

## Start with an issue

Before writing any code, [open an issue](https://github.com/SkrobyLabs/mittens/issues) first.

- **Bug reports** — describe what happened, what you expected, and how to reproduce it. Include your OS, Docker version, and provider (`claude`, `codex`, `gemini`).
- **Feature requests** — explain the problem you're trying to solve, not just the solution you have in mind. Context helps.

This lets us discuss the approach before you invest time on a PR that might not land.

## Pull requests

- Keep PRs focused on a single change. Small, well-scoped PRs get reviewed faster.
- Reference the issue your PR addresses.
- Include a clear description of what changed and why.
- Make sure `make test` passes.

## What will get rejected

- Large PRs opened without prior discussion.
- Bulk AI-generated PRs with no clear intent or context. If a bot wrote it and you can't explain every line, don't submit it.
- Changes that don't relate to an open issue or prior conversation.

## Code style

- Follow existing patterns in the codebase.
- Run `make fmt` and `make vet` before submitting.
- Don't add features, abstractions, or refactors beyond what the issue calls for.

## Container tests from inside Mittens

The Docker integration suite needs access to a Docker daemon. When running it
inside Mittens with access to the host daemon, keep temporary bind-mount
fixtures under the persistent workspace so sibling containers can see them:

```bash
mkdir -p .cache/integration-tmp
TMPDIR="$PWD/.cache/integration-tmp" \
GIT_CEILING_DIRECTORIES="$PWD/.cache/integration-tmp" \
  make test-integration-short
```

The Git discovery boundary prevents temporary test projects from inheriting
the surrounding repository. Short mode skips additional extension-image
builds; it still builds and tests the base image.

## Release verification

`make release VERSION=vX.Y.Z` builds the supported host binaries and Linux
entrypoints under `dist/`, with a `SHA256SUMS` manifest for those artifacts.
Release builds require a clean working tree, including untracked files, so
the stamped commit identifies the source being built. They use the commit
timestamp, trim local source paths, and keep module dependencies read-only.
Use the same Go toolchain and source commit
when comparing rebuilds; override `DATE` explicitly only when required.

Verify a release from inside `dist/` with `sha256sum -c SHA256SUMS` on Linux or
`shasum -a 256 -c SHA256SUMS` on macOS. Publish the manifest alongside the
binaries. Checksums detect corruption; they do not authenticate the publisher.
The verification workflow builds all release targets, checks hashes, and
smoke-tests version metadata without publishing artifacts.

These controls cover Mittens binaries. Container builds still use moving apt
repositories and some upstream provider/tool installers; they are not fully
reproducible or independently authenticated by the binary checksum manifest.
