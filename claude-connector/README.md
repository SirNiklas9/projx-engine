# ProjX → Claude Code connector

A thin, Claude-Code-specific connector that wires the **harness-agnostic ProjX
engine** into Claude Code via lifecycle hooks. It calls the engine's **CLI**
(no server to keep running) so the engine stays unchanged; a different harness
(Neovim, JetBrains) would get its own thin connector calling the same commands.

It wires three hooks:

| Hook | Calls | Effect |
|---|---|---|
| `SessionStart` | `projx-engine --root "$CLAUDE_PROJECT_DIR" context` | injects the project's store context at session start |
| `UserPromptSubmit` | same | re-injects the context each turn (full context; task-slicing is deferred) |
| `PreToolUse` (Read\|Edit\|Write) | `projx-engine --root "$CLAUDE_PROJECT_DIR" gate check <path>` | blocks a tool call whose target path is off-limits (exit 2) |

The injected context is wrapped in a declarative `<project-context>` frame so
Claude Code treats it as project reference **facts**, not as injected
instructions (avoids the prompt-injection false-positive on hook context).

## Prerequisites

1. **`projx-engine` on `PATH`** (or set `PROJX_ENGINE_BIN` to its absolute path).
   Build it: `cd projx-engine && GOWORK=off go build -o ~/.local/bin/projx-engine .`
2. **`jq`** recommended (robust JSON parsing in the gate hook). Fedora:
   `sudo dnf install jq`. Without it the hook uses a best-effort `sed` fallback.
3. A **ProjX-seeded project** — the store holds the conventions + gate rules.
   Seed the floor: `projx-engine --root . store seed` (or it auto-seeds on the
   first `agent run`). Confirm: `projx-engine --root . store list`.

## Install

From your target project's root:

```sh
# merge the connector's .claude/ into your project's .claude/
cp -r /path/to/projx-engine/claude-connector/.claude/. ./.claude/
chmod +x ./.claude/hooks/*.sh
```

If the project already has `.claude/settings.json`, merge the `hooks` keys by
hand (or put these hooks in `.claude/settings.local.json`, which is gitignored,
if you don't want to commit them). Then start Claude Code in the project.

## Notes / scope

- **Never sets `PROJX_AGENT_CONTEXT=1`.** Both scripts also *unset* an inherited
  value — that restricted engine mode refuses `context` and `gate`, so unsetting
  it keeps the connector working even when Claude was launched inside a ProjX
  caged `agent run`.
- **Loud fail-open.** If the engine errors or is missing, the gate hook allows
  the tool call rather than brick the session, but prints a visible
  `ProjX gate UNAVAILABLE — NOT enforcing (engine rc=…). '<path>' allowed unchecked.`
  warning to stderr so the lost enforcement is noticed, not silent. The gate is
  one cooperative layer; OS-FS confinement (Landlock on Linux) is the real wall.
- **`PROJX_ENGINE_BIN`** overrides the engine path if it isn't on `PATH`.
- **Token cost:** `UserPromptSubmit` injects the full context every turn (v1).
  Task-sliced context is deferred until it justifies the cost.
- **Platform:** targets Linux (bash + the engine's Landlock tier). On Windows the
  scripts need Git Bash; matching is separator-normalized so paths still work.
- **Matcher** is `Read|Edit|Write`. Add `MultiEdit|NotebookEdit` to the matcher
  in `settings.json` if you want those gated too.

## Manual test

See the build conversation / `PROJX-CONNECTOR-BUILD-PLAN.md` for the exact
step-by-step verification (standalone command checks, JSON-on-stdin hook
simulation, then a real Claude Code session).
