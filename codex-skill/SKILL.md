---
name: projx
description: >-
  Use and maintain ProjX, the scoped knowledge engine for global, workspace, and
  project knowledge. Use when setting up ProjX, reading project knowledge,
  checking gates or impact, routing work, or saving durable facts.
---

# ProjX for Codex

ProjX is the authoritative declared-knowledge store for enabled projects. Its
global hook injects the applicable global, workspace, and project context into
Codex automatically. Its MCP server exposes deterministic pull tools.

## Working protocol

1. Before broad repository discovery, query `store_query` for the relevant
   concept, symbol, convention, or decision.
2. Use `impact` before changing a widely used symbol.
3. Treat a denied gate as authoritative. Never work around it.
4. Save durable discoveries with `store_commit`; do not create a markdown memory
   file when the fact belongs in ProjX.
5. Use `route` when the user asks ProjX to choose the appropriate work tier.

## Setup

Global setup is idempotent:

```text
projx-engine init --global
```

Enable the current project:

```text
projx-engine --root . init
```

Create a multi-repository workspace from its parent directory:

```text
projx-engine --root <workspace> init --workspace
```

Global setup installs Codex lifecycle hooks and this skill. Project setup
registers the ProjX MCP server in `.codex/config.toml`, then seeds and indexes the
project. Restart Codex after changing global hooks or MCP configuration.

