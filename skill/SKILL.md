---
name: projx
description: >-
  Set up, bootstrap, or install ProjX — the floating declared-knowledge engine
  (store + gate + code-map + tier-routing). Use when the user wants to bootstrap
  ProjX on this machine, install the global ProjX floor/hook, or initialize the
  current project or workspace with ProjX. Triggers on "set up ProjX", "bootstrap
  ProjX", "make this a ProjX project", "init ProjX here", "install ProjX".
---

# ProjX self-install runbook

ProjX is a single self-contained binary (git/fossil model: it runs when invoked
and exits — no daemon). It floats: its scope follows what you touch. There are
two things to set up, and they are independent.

Reference the binary by its install path — `~/.local/bin/projx-engine` — so PATH
never needs configuring and the same steps work on any machine.

## 1. GLOBAL — bootstrap once per machine (idempotent)

Ensure ProjX is bootstrapped globally. This is safe to re-run; it only adds
what's missing.

```sh
~/.local/bin/projx-engine init --global
```

This merges the ProjX lifecycle hook into `~/.claude/settings.json` (preserving
any hooks you already have), seeds the global-scope floor (working-protocol +
secrets-by-codename conventions + off-limits gate rules), and installs this
skill. It does NOT reinstall the binary and never clobbers existing hooks.

If `~/.local/bin/projx-engine` doesn't exist, INSTALL it by DOWNLOADING the
prebuilt release binary — do NOT build from source:

- Run the installer (fetches the binary for this OS/arch from the latest release,
  then runs the bootstrap above):
  ```sh
  curl -fsSL https://raw.githubusercontent.com/SirNiklas9/projx-engine/main/install.sh | sh
  ```
  Or, if the `projx-engine` repo is already on this machine, run its `install.sh`.
- The installer downloads the asset `projx-engine_<os>_<arch>` (`.exe` on Windows)
  from the latest release of the public repo `github.com/SirNiklas9/projx-engine`
  to `~/.local/bin/projx-engine` and marks it executable — via `gh release
  download` or `curl` of the release asset URL.
- If the download 404s, the latest release has no binary for this platform yet:
  tell the user a release with prebuilt binaries must be published first (asset
  `projx-engine_<os>_<arch>`). Do NOT fall back to building from source.

## 2. PROJECT / WORKSPACE — initialize on demand

To make the CURRENT directory a ProjX project (only when the user asks), run
from the project root:

```sh
~/.local/bin/projx-engine --root . init
```

This installs the project store + code map + `/projx:*` slash commands + the
ProjX MCP server. It writes a `CLAUDE.md` and a `.claude/` directory — that is
expected and managed by ProjX. It does NOT install a per-project hook: the single
global hook from step 1 does all context injection (a per-project hook would
double-inject).

For a multi-repo WORKSPACE, create a `.projx-workspace/` directory on the parent
folder that holds the repos; workspace-scoped records then compose from there.

## The model — why two steps

- **Global floor is always on.** Installed once (step 1), it applies everywhere
  the hook fires — the working protocol and secret gates travel with you.
- **Workspace / project are delineated by explicitly initializing them** (step
  2). ProjX doesn't guess; you mark a directory as a project when you mean to.
- **ProjX floats.** Scope follows what you touch: global records apply always,
  project records load when you're in that repo, workspace records when you're
  under that workspace folder. One binary, one global hook, knowledge that
  composes by scope.
