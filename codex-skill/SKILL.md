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
2. When dispatcher mode is on, COORDINATOR implementation work MUST call the
   ProjX `dispatch_run` MCP tool and return the resulting run to the user. A
   dispatched worker (`PROJX_ROLE=worker` or `PROJX_AGENT_CONTEXT=1`) MUST
   implement its assigned task directly and MUST NOT dispatch it again. The
   coordinator must not edit files directly after calling the read-only `route`
   tool. `route` previews a decision; it does not execute or switch models.
3. Use `impact` before changing a widely used symbol.
4. Treat a denied gate as authoritative. Never work around it.
5. Save durable discoveries with `store_commit`; do not create a markdown memory
   file when the fact belongs in ProjX.
6. Use `route` only when the user asks to preview the appropriate work tier.

## Codex status

The Codex-only `SessionStart` adapter emits one compact native status message
showing the active scope, knowledge count, integration health, and whether the
code map is refreshing in the background. It never starts or advertises a
browser dashboard automatically. The lifecycle hook's knowledge output remains
model context, and other harnesses do not receive this presentation behavior.

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

The browser dashboard is explicit: run `projx --dashboard` only when the user
asks to open it. The compatibility form is `projx-engine status --serve`;
use its `--no-open` option to print the URL without launching a browser.
Terminal views are `status --compact`, `status --watch`, and `--json`.

## Setup

The skill owns setup and updates. On Windows it stages the paired interactive
engine and `projx-engine-headless.exe` proxy beside one another; lifecycle hooks,
MCP, and background status use the proxy so they never open console windows. It
then runs global bootstrap; the engine copies both assets to an immutable path
under the user's neutral ProjX home, typically `os.UserConfigDir()/projx/bin/`,
and points Codex and Claude adapters at that exact path. Do not ask the user to
install it, edit PATH, or replace a running binary.

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
