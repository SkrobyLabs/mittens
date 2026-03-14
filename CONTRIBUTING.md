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
