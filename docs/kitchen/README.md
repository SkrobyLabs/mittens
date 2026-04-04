# Kitchen Docs

Kitchen is a headless control plane for parallel AI coding work. It
orchestrates planners, schedulers, and workers running inside Mittens
containers.

## Documentation

- [Reference](./reference.md) — CLI commands, HTTP API, RuntimeAPI, evidence
  tiering, configuration
- [Architecture](./architecture.md) — subsystem overview, runtime split, git
  workflow, failure handling
- [Operations](./operations.md) — snapshot history, runtime daemon, recycle,
  conflict retry, provider health

## Quick Start

```bash
# Terminal 1: start Kitchen and supervise all configured providers
kitchen serve

# Terminal 2: submit an idea
kitchen submit "Add typed parser errors"

# Review and approve the plan
kitchen plans
kitchen plan PLAN_ID
kitchen approve PLAN_ID

# Monitor progress
kitchen status
kitchen evidence PLAN_ID

# Merge completed work
kitchen merge --squash parser-errors
```

Manual `mittens daemon` startup still works, but it is the advanced/debug
path. The recommended simple flow is plain `kitchen serve`; use
`kitchen serve --provider <name>` when you intentionally want to restrict
execution to one provider.

## Recommended Reading Order

1. [Reference](./reference.md) for the current CLI and API surface
2. [Architecture](./architecture.md) for how the subsystems fit together
3. [Operations](./operations.md) for runtime operations and operator knobs
