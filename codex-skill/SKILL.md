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

## Codex GUI status dashboard

The Codex-only `SessionStart` adapter emits an **Open ProjX dashboard** link for
each new task. Other harnesses do not receive this presentation behavior.

When the user asks to show, open, refresh, or inspect the ProjX dashboard in
Codex:

1. Call the `status_snapshot` MCP tool with the current project root and the
   current session id when one is available.
2. Render the returned structured snapshot as an inline Codex dashboard in the
   current task. Keep active scope prominent and show record, gate, ADR, agent,
   mode, verification, hook, MCP, store, and binary state.
3. Include a refresh action that sends a follow-up asking Codex to call
   `status_snapshot` again and update the dashboard. Include an inspect-scope
   action that asks Codex to verify the breadcrumb and owning project.
4. Tell the user to pin the task when they want the dashboard to remain in the
   Codex sidebar. Do not claim that ProjX owns permanent Codex application
   chrome; the supported GUI is the pinned interactive task.

For a persistent dashboard without chat, run `projx-engine status --serve`. It
opens a loopback-only browser dashboard that refreshes from the same snapshot.
Use `--no-open` to print the URL without launching a browser. The terminal
fallbacks are `--compact`, `--watch`, and `--json`.

## Setup

The skill owns setup and updates. On Windows it stages the paired interactive
engine and `projx-engine-headless.exe` proxy beside one another; lifecycle hooks,
MCP, and background status use the proxy so they never open console windows. It
then runs global bootstrap; the engine copies both assets to an immutable path
under `~/.codex/projx/bin/` and points Codex and Claude adapters at that exact
path. Do not ask the user to install it, edit PATH, or replace a running binary.

Global bootstrap is idempotent:

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

Global bootstrap activates the managed engine, installs lifecycle hooks, and
refreshes this skill. Project setup registers that exact engine with MCP, then
seeds and indexes the project. Restart Codex after hook or MCP configuration
changes; active sessions may finish on their previous immutable engine.
