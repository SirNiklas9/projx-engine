# ProjX → Claude Code connector

A thin, Claude-Code-specific connector that wires the **harness-agnostic ProjX
engine** into Claude Code via lifecycle hooks. It calls the engine's **CLI**
(no server to keep running) so the engine stays unchanged; a different harness
(Neovim, JetBrains) would get its own thin connector calling the same commands.

It wires six hooks across five events:

| Hook | Calls | Effect |
|---|---|---|
| `SessionStart` | `projx-engine map sync` | re-indexes the code map (symbol signature + `file:line` anchor) so injected anchors are current; silent (writes the store only) |
| `SessionStart` | `projx-engine context --session <id>` | injects the **lean floor** (protocol + law) and starts a fresh session checkpoint |
| `UserPromptSubmit` | `projx-engine context --session <id> --task "<prompt>"` | re-asserts the law + injects only the **new/changed** store records relevant to the prompt (task-sliced **delta**) |
| `PreToolUse` (Read\|Edit\|Write) | `projx-engine gate check <path>` | blocks a tool call whose target path is off-limits (exit 2) |
| `PreCompact` | `projx-engine context --session <id> --reset` | marks the floor lost so the next turn re-sends protocol+law+slice after compaction |
| `Stop` | `projx-engine session-suggest --session <id>` | **suggest-only**: if the user said `@remember` but nothing was committed, nudges once (exit 2); silent otherwise |

The session checkpoint lives at `<root>/.projx/agent-seen-<session>.json` (gitignored
under `.projx`). It records which records have been injected (id → `UpdatedAt`) so each
turn sends the **least** new context: the small binding law re-asserts every turn, while
docs/ADRs/history load per-task and only once unless they change.

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

It also installs namespaced **`/projx:*` slash commands** (`.claude/commands/projx/`):
`/projx:remember <fact>` (save to the store), `/projx:store` (show it), `/projx:route
<task>` (see the tier decision), `/projx:gate` (list/check off-limits paths). They shell
out to the engine — low-token store entry without launching the workbench.

## Install

The one-command on-ramp (installs everything below, seeds the store, indexes the map):

```sh
projx-engine --root . init        # add stacks explicitly: init go node ; reinstall: init --force
```

Or wire it by hand from your target project's root:

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
- **Token cost:** `UserPromptSubmit` sends a task-sliced **delta** — the binding law
  plus only the records that are new or changed for that prompt. Unchanged records
  already in context are not re-sent; the full reference set never dumps. (`jq` is used
  to read the prompt from the hook JSON; without it the hook degrades to the lean floor.)
- **Platform:** targets Linux (bash + the engine's Landlock tier). On Windows the
  scripts need Git Bash; matching is separator-normalized so paths still work.
- **Matcher** is `Read|Edit|Write`. Add `MultiEdit|NotebookEdit` to the matcher
  in `settings.json` if you want those gated too.

## Manual test

See the build conversation / `PROJX-CONNECTOR-BUILD-PLAN.md` for the exact
step-by-step verification (standalone command checks, JSON-on-stdin hook
simulation, then a real Claude Code session).
