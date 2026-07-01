# ProjX Engine — v0.1.0 (pre-release)

The floating, dispatch-first knowledge engine. This is the first tagged pre-release; the
core spine is built and verified end-to-end. Below is **what to run** and **what changed**.

---

## What it is (one paragraph)

ProjX keeps a codebase's knowledge as **typed records** (conventions, off-limits gates,
decisions, docs, a `file:line` code-map of every symbol) in a per-repo store, composed with
a machine-global and an optional workspace store. A lifecycle-aware connector feeds an AI
agent only the **task-relevant slice** each turn (~1k tokens instead of a fat CLAUDE.md).
The **main session dispatches** work to tier-routed agents instead of editing directly, and
one running **engine floats** across repos — point it at any repo, it composes and serves
what that repo needs.

## Run it

```sh
# build the CLI once
cd projx-engine && GOWORK=off go build -o ~/.local/bin/projx-engine .

# per project (drag-and-drop): installs the connector, seeds the floor, indexes the code
cd /path/to/repo && projx-engine init

# multi-repo: index several into one store
projx-engine map sync ../Evolution ../Frontend ../Api
```

Open Claude Code in the folder — the connector loads automatically: each turn gets the lean
floor + task slice, off-limits paths are gate-blocked, and (new) the trunk dispatches edits
to tier-agents. Optional levels: drop a `.projx-workspace/` folder above several repos to
give them a shared **workspace** rule-set; the machine-global store lives at
`<UserConfigDir>/projx/store.db`.

## What changed in v0.1.0 (this is the delta — the run surface above is stable)

**Trunk-dispatch (default ON):**
- The main session is a **dispatcher**: `PreToolUse` denies `Edit/Write` in the trunk (unless
  `PROJX_ROLE=worker`), so work routes to a spawned tier-agent (`dispatch --run`). Toggle with
  `store commit --kind gate-rule --key setting/dispatcher-mode --body off`.
- Proven e2e: trunk denied → auto-dispatch → uncaged worker edits + reports → trunk verifies.

**Routing — 4 tiers incl. a new top rung:**
- `cheap-fast` (Haiku) → `default` (Sonnet) → `deep-reasoning` (Opus 4.8) → **`elevate` (Fable 5)**.
  `elevate` is deliberate-only (`@elevate`/`@fable`, `route pin/floor`); auto-escalation tops
  out at Opus. Tier commands now `--permission-mode acceptEdits` so headless workers can edit.

**Three-level store, composed on read:**
- machine/user **global** + optional **workspace** (`.projx-workspace/` walk-up) + per-repo
  **project**. `scope ≠ storage-location`; writes route to the owning level, fall back up when
  a level is absent (project-only just works). Verified: a workspace rule composes across repos.

**The engine FLOATS:**
- `serve` takes `?root=` per request with a per-root composed-store cache — one running engine
  serves any repo on demand (AI-refocusable). Not "open a project" — point it anywhere.

**Workbench integration:**
- The Workbench's Store pane and cross-machine Global sync now relay to the floating engine
  (the engine is the single store authority; cross-machine sync operates on its global store).

**Cage:** now strictly **opt-in** (`PROJX_CAGE=1`) — never auto-engaged (enforces the opt-in law).

**Languages:** tree-sitter (Go, JS/TS/TSX/Astro, Python, C#, Odin, …) — not Go-only.

## Known limitations (pre-release, honest)

- Workbench `gateFromStore` + agent-launch context still read locally (Store CRUD + Global sync
  relay; agent-launch relay to `/api/agent/spec` is next).
- Engine `agent/run` handlers use the default root (not yet floated).
- Store-cache is unbounded (closed on shutdown; bounded LRU is a follow-up).
- Full two-machine cross-sync proven at the mechanism level (engine round-trip); end-to-end
  two-box run not yet exercised.
