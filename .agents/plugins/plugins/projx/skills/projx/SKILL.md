---
name: projx
description: >-
  Install, update, repair, and use ProjX, the harness-agnostic governed
  knowledge layer. Use for ProjX setup, project or workspace activation,
  dashboard status, stored knowledge, gates, impact, routing, reconciliation,
  verification, and durable learning.
---

# ProjX for Codex

ProjX runs above the model and harness. Its global lifecycle adapter injects the
applicable global, workspace, and project context into every task, while MCP
provides deterministic knowledge and governance tools.

## AI-owned installation

The user never installs a binary, edits PATH, creates a database, or configures
hooks or MCP by hand. When ProjX is missing, stale, or the user asks to enable,
install, update, or repair it:

1. Run the bundled bootstrap script for the host OS. Resolve paths relative to
   this `SKILL.md`: `../../scripts/bootstrap.ps1` on Windows or
   `../../scripts/bootstrap.sh` on Linux/macOS.
2. Pass the current project root. The script downloads the matching public
   release to a temporary directory and invokes `init --global`, followed by
   project initialization. Do not build from source and do not add anything to
   PATH.
3. Report the managed engine version and ask the user to restart Codex once if
   hooks or MCP changed. Do not replace or terminate an active managed engine.

The bootstrap is idempotent. The engine copies the runtime to an immutable,
content-addressed directory beneath `~/.codex/projx/bin/`, merges Codex and
Claude adapters, seeds stores, runs manifest migrations in order, and registers
the exact headless binary for background work. On Windows the script downloads
both the console CLI and GUI-subsystem proxy so no background console windows
appear.

If network or filesystem approval is required, request it as one scoped install
approval and continue automatically after it is granted. If the matching
release asset does not exist, report that exact constraint; never silently use
a local compiler or an unverified third-party binary.

## Project and workspace activation

The bootstrap script enables the current project by default. For a multi-repo
workspace, run the managed engine with:

```text
<managed-engine> --root <workspace> init --workspace --codex
```

Use the exact managed path reported by bootstrap. PATH is neither required nor
authoritative.

## Governed working protocol

1. Query `store_query` before broad repository discovery.
2. Use `impact` before changing a widely used symbol.
3. Treat a denied `gate_check` as authoritative; never work around it.
4. Follow Recall -> Gate -> Act -> Verify -> Learn. Learning stages candidates;
   it does not automatically promote observations to policy.
5. Save durable verified discoveries with `store_commit`, not markdown memory.
6. Use `route` when ProjX should select the work tier.
7. Use dispatcher/workflow orchestration for decomposable work. Parallelize
   workers with disjoint declared write sets; retest dependencies sequentially.

## Dashboard and reconciliation

For status, call `status_snapshot` with the current root and render its active
scope, records, gates, ADR freshness, agents, modes, verification, hooks, MCP,
store, and binary health. The Codex SessionStart adapter also emits an **Open
ProjX dashboard** link for each new task.

Reconciliation is AI-governed: run or respond to the engine's reconciliation
sweep, compare stale or conflicting candidates against current evidence, and
promote, supersede, or reject only through the applicable gates.

## Removal

Run `<managed-engine> uninstall --global`. Preserve knowledge and secrets.
Use `--purge-store` only when the user explicitly requests deletion of global
knowledge.
