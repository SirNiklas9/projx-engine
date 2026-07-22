---
name: projx
description: >-
  Set up, bootstrap, update, or use ProjX, the harness-agnostic governed
  knowledge layer. Use for ProjX setup, repair, project initialization, gates,
  routing, impact, reconciliation, and durable knowledge.
---

# ProjX AI-managed runbook

ProjX is a harness-agnostic layer used by Codex, Claude, and other adapters. The
AI owns setup and maintenance. Never ask the user to install it, edit PATH, run
an installer/build script, or replace a running executable.

## Bootstrap or update

The canonical runtime is managed beneath `~/.codex/projx/bin/`. Binaries are
immutable and content-addressed. Hooks and MCP configurations point to one exact
binary. A newer release receives a new path, allowing active sessions to finish
without Windows locked-file replacement.

1. If a managed engine exists, run its `version --check`.
2. If missing or stale, detect OS/architecture and download the matching
   prebuilt asset from the latest public `SirNiklas9/projx-engine` GitHub
   release into a temporary directory. Do not build from source.
3. Run the downloaded binary directly from that temporary path:
   `projx-engine init --global`. The command copies itself atomically into
   `~/.codex/projx/bin/<version-content>/`, then configures both Codex and Claude
   adapters to that immutable path and refreshes their skills.
4. Run the managed binary's `version`, then report that the harness must restart
   to load changed hooks or MCP processes. Existing sessions are not killed.

Windows requires the paired assets `projx-engine_windows_amd64.exe` and
`projx-engine-headless_windows_amd64.exe`; stage them together and rename the
second to `projx-engine-headless.exe` beside the CLI before running bootstrap.
The headless proxy preserves harness protocol pipes while preventing console
windows. Other release assets are
`projx-engine_linux_amd64`, `projx-engine_linux_arm64`, and, when published,
the corresponding Darwin asset. Make Unix downloads executable before invoking
them. If no matching release asset exists, report that constraint; do not
silently substitute a local build.

Re-running `init --global` is the repair path. It is idempotent, preserves other
hooks, seeds missing global knowledge, and updates configuration to the newly
managed binary. Never delete project/global stores or sealed secrets during an
update.

## Project and workspace activation

Use the exact managed binary path; PATH is neither required nor authoritative.

```text
<managed-engine> --root <project> init
<managed-engine> --root <workspace> init --workspace
```

Project initialization registers the same exact binary with MCP, seeds and
indexes the project, and installs harness adapters. Global hooks float scope to
the project being touched; ProjX remains above the harness rather than becoming
one.

## Governed working protocol

Follow the injected Recall → Gate → Act → Verify → Learn lifecycle. Query stored
knowledge before broad discovery, use impact before wide code changes, treat a
denied gate as authoritative, and commit durable verified discoveries. Learning
creates candidates until evidence and policy gates promote them.

## Removal

Run `<managed-engine> uninstall --global`. This removes ProjX adapter entries and
skills while preserving knowledge and secrets. Use `--purge-store` only when the
user explicitly requests deletion of global knowledge. Old immutable binaries
may be garbage-collected only when no configured adapter references them; never
interrupt a running process to reclaim them.
