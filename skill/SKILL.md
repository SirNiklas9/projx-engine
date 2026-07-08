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

This is a runbook for YOU, the AI. You install and drive the binary yourself,
ADAPTING to the operating system — do NOT assume a POSIX shell is available (the
target machine is often Windows). There are TWO primary prebuilt binaries —
**Linux** and **Windows** — published on the latest GitHub release of the public
repo `SirNiklas9/projx-engine` (macOS optional).

## 1. GLOBAL — bootstrap once per machine (idempotent)

First, check whether the binary is already installed and runnable. Try the
expected path/name for the OS (see the table below); if `projx-engine` (or
`projx-engine.exe`) runs, **check for an update before bootstrapping**:

```
projx-engine version --check
```

If the last line reads `update: available vX -> vY`, the installed binary is
stale — download the latest asset (Step B below) and replace it, then continue.
Do the swap **safely**: download to a temp file first, then move it over the
existing binary. On Windows a running `.exe` cannot be overwritten in place, so
the move must happen while the binary is NOT executing (write to
`projx-engine.exe.new`, then `Move-Item -Force` over `projx-engine.exe`). If the
check reports `up to date` (or the network check fails), skip straight to the
bootstrap command:

```
projx-engine init --global
```

That merges the ProjX lifecycle hook into `~/.claude/settings.json` (preserving
any hooks you already have), seeds the global-scope floor (working-protocol +
secrets-by-codename conventions + off-limits gate rules), and installs this
skill. It is idempotent, never clobbers existing hooks, and does NOT reinstall
the binary.

### If the binary is NOT installed — install it yourself, per OS

Detect the OS, then do the three steps for that OS. Download the PREBUILT binary
from the latest release — do NOT build from source.

**Step A — detect OS.** Windows vs Linux/macOS. (In this harness: check the
platform, or run `uname -s` where a shell exists; on Windows use PowerShell.)

**Step B — download the right asset** from the latest release of
`github.com/SirNiklas9/projx-engine`. Pick by OS/arch:

| OS | Asset name | Install path | Make runnable |
|---|---|---|---|
| Linux (x86_64) | `projx-engine_linux_amd64` | `~/.local/bin/projx-engine` | `chmod +x` |
| Linux (arm64) | `projx-engine_linux_arm64` | `~/.local/bin/projx-engine` | `chmod +x` |
| Windows (x86_64) | `projx-engine_windows_amd64.exe` | `%LOCALAPPDATA%\projx\projx-engine.exe` (or `~/.local/bin/projx-engine.exe` under Git Bash) | already executable |
| macOS (optional) | `projx-engine_darwin_amd64` / `_arm64` | `~/.local/bin/projx-engine` | `chmod +x` |

Use whatever download tool is present:

- **Linux/macOS:** `gh release download --repo SirNiklas9/projx-engine --pattern <asset> --output <path>`, or
  `curl -fsSL https://github.com/SirNiklas9/projx-engine/releases/latest/download/<asset> -o <path>`.
- **Windows (PowerShell):**
  ```powershell
  $dir = "$env:LOCALAPPDATA\projx"; New-Item -ItemType Directory -Force $dir | Out-Null
  Invoke-WebRequest -Uri "https://github.com/SirNiklas9/projx-engine/releases/latest/download/projx-engine_windows_amd64.exe" -OutFile "$dir\projx-engine.exe"
  ```
  (or `gh release download ... --output "$dir\projx-engine.exe"`).

**Pin a specific version (optional).** The commands above fetch the LATEST
release — that is the default and what you usually want. To install a SPECIFIC
version instead, swap `releases/latest/download/<asset>` for
`releases/download/<tag>/<asset>` — e.g.
`https://github.com/SirNiklas9/projx-engine/releases/download/v0.3.0/projx-engine_windows_amd64.exe`
(with `gh`, pass the tag: `gh release download <tag> --repo SirNiklas9/projx-engine …`).
Only pin when you deliberately want an older/fixed build; otherwise stay on latest.

If the download 404s, the latest release has no binary for this platform yet —
tell the user a release with prebuilt binaries must be published first (assets
`projx-engine_linux_amd64` and `projx-engine_windows_amd64.exe`). Do NOT fall
back to building from source.

**Step C — make it runnable + reachable.** On Unix, `chmod +x` the file and make
sure `~/.local/bin` is on PATH. On Windows, add the install dir to PATH (or just
invoke it by its absolute path — that always works). Then run the bootstrap:

```
projx-engine init --global
```

> Unix-only convenience: `curl -fsSL https://raw.githubusercontent.com/SirNiklas9/projx-engine/main/install.sh | sh`
> does Steps A–C + bootstrap in one line. It is a shell script, so it is NOT the
> cross-platform path — on Windows (no guaranteed shell) do the AI-driven steps
> above instead.

### Uninstall, or CLEAN REINSTALL (machine-level, cross-platform)

To remove ProjX, or to reinstall it cleanly, use the binary's OWN uninstall — no
shell scripts, no jq, native on every OS including Windows:

```
projx-engine uninstall --global
```

This reverses `init --global`: it strips the ProjX lifecycle hook from
`~/.claude/settings.json` (preserving every OTHER hook, writing a `.projx-bak`
backup) and removes the installed skill. It KEEPS declared knowledge and secrets.
Add `--purge-store` to also drop the per-user global store (never secrets). The
binary is left in place; per-project `.projx`/`.claude` files are left as-is.

**When the user asks to "clean old ProjX and install fresh" / "reinstall clean":**

1. Download the LATEST binary (Step B above), overwriting any existing one.
2. `projx-engine uninstall --global`   — clean the old hook + skill (add `--purge-store`
   only if the user wants the global store gone too; NEVER remove secrets).
3. `projx-engine init --global`        — fresh install of the latest.
4. `projx-engine version`              — confirm the new version.

`uninstall --global` first requires a binary that has the command (v0.4.1+); the
downloaded latest always does, so it can clean an older install's footprint.

## 2. PROJECT / WORKSPACE — initialize on demand

To make the CURRENT directory a ProjX project (only when the user asks), run from
the project root (use the OS-appropriate path/name for the binary):

```
projx-engine --root . init
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
