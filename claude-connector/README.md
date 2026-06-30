# ProjX → Claude Code connector

A thin, Claude-Code-specific connector that wires the **harness-agnostic ProjX
engine** into Claude Code via lifecycle hooks. The only Claude-Code-specific
artifact is `settings.json` (the registration); every event points at one
**Go-native** command — `projx-engine hook` — which reads the hook JSON from stdin
and dispatches. **No bash, no `jq`, no shell variables** — so it runs identically on
Windows, macOS, and Linux. A different harness (Neovim, JetBrains) gets its own thin
`settings`-equivalent pointing at the same `projx-engine hook` (or the underlying
`context`/`gate`/`map`/`session-suggest` commands).

`settings.json` registers `projx-engine hook` on five events; the engine reads
`hook_event_name` and does the right thing:

| Event | What `projx-engine hook` does |
|---|---|
| `SessionStart` | refreshes the code map (silent), then injects the **lean floor** (protocol + law) and starts a fresh session checkpoint |
| `UserPromptSubmit` | re-asserts the law + injects only the **new/changed** store records relevant to the prompt (task-sliced **delta**) |
| `PreToolUse` (Read\|Edit\|Write) | blocks a tool call whose target path is off-limits (exit 2 + reason) |
| `PreCompact` | marks the floor lost so the next turn re-sends protocol+law+slice after compaction |
| `Stop` | **suggest-only**: if the user said `@remember` but nothing was committed, nudges once (exit 2); silent otherwise |

The engine resolves the project root from `CLAUDE_PROJECT_DIR` (or the payload `cwd`),
so the command needs no arguments.

The session checkpoint lives at `<root>/.projx/agent-seen-<session>.json` (gitignored
under `.projx`). It records which records have been injected (id → `UpdatedAt`) so each
turn sends the **least** new context: the small binding law re-asserts every turn, while
docs/ADRs/history load per-task and only once unless they change.

The injected context is wrapped in a declarative `<project-context>` frame so
Claude Code treats it as project reference **facts**, not as injected
instructions (avoids the prompt-injection false-positive on hook context).

## Prerequisites

1. **`projx-engine` on `PATH`.** Build it: `cd projx-engine && GOWORK=off go build -o ~/.local/bin/projx-engine .`
   (`settings.json` invokes `projx-engine` by name; `init` warns if it isn't found.)
2. That's it — no `bash`, no `jq`. The hook handler is pure Go and reads the hook
   JSON itself. `projx-engine init` seeds the store and indexes the code map for you.

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
# copy the connector's .claude/ (settings.json + /projx:* slash commands) into yours
cp -r /path/to/projx-engine/claude-connector/.claude/. ./.claude/
```

If the project already has `.claude/settings.json`, merge the `hooks` keys by hand
(or put them in `.claude/settings.local.json`, which is gitignored). `init` does this
merge-safely for you. Then start Claude Code in the project.

## Notes / scope

- **Caged-agent safe.** `projx-engine hook` self-unsets `PROJX_AGENT_CONTEXT` (the
  restricted mode a caged `agent run` sets), so the connector keeps working even when
  Claude was launched inside a ProjX cage.
- **Fail-open gate.** If the store can't be read, the `PreToolUse` gate allows the call
  rather than brick the session. The hook gate is one cooperative layer; OS-FS
  confinement (Landlock on Linux) is the real wall.
- **Token cost:** `UserPromptSubmit` sends a task-sliced **delta** — the binding law
  plus only the records new or changed for that prompt; unchanged records already in
  context are not re-sent, and the full reference set never dumps.
- **Matcher** is `Read|Edit|Write`. Add `MultiEdit|NotebookEdit` to the `PreToolUse`
  matcher in `settings.json` to gate those too.
- **Portability:** the handler is pure Go (no bash/`jq`), and gate matching is
  separator-normalized, so the same connector works on Windows, macOS, and Linux.

## Manual test

Feed a hook event on stdin to see each behavior without a live session, e.g.:

```sh
echo '{"hook_event_name":"PreToolUse","tool_input":{"file_path":"secret/x"}}' \
  | CLAUDE_PROJECT_DIR=. projx-engine hook ; echo "exit=$?"   # → exit 2 if secret/ is gated
```
