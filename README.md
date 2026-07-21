# projx-engine

A **structured knowledge store for your codebase** that injects the *right* context at
the *right* moment — automatically, at a fraction of the token cost of a fat `CLAUDE.md`.

Instead of pasting context or hand-maintaining a giant markdown file, ProjX keeps your
project's knowledge as **typed records** (conventions, off-limits gates, decisions, docs,
and a code-map of every symbol with `file:line` anchors) and a lifecycle-aware hook feeds
Claude Code only the slice relevant to each message. On a real 8-repo project (~3,200
symbols, a ~197k-token knowledge base that can't even fit in a context window), the
per-turn context stays flat at **~1k tokens (~99% saved)**, and "where is X" becomes a
one-line anchor jump instead of a 25-file grep.

---

## Quick start — is it drag-and-drop?

Almost. One-time install, then one command per project. You also need the `claude` CLI
(Claude Code) — you already have it.

**Install (one line, prebuilt — no build from source):**
```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SirNiklas9/projx-engine/main/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/SirNiklas9/projx-engine/main/install.ps1 | iex
```
Each installer downloads the latest release binary, puts it on your `PATH`, and runs the
global bootstrap (lifecycle hook + global floor + the projx skill). Idempotent and
self-healing — re-run any time to upgrade and repair.

**Or install as a Claude Code plugin:**
```
/plugin marketplace add SirNiklas9/projx-engine
/plugin install projx@projx
```
The plugin bundles the projx skill + `/projx:*` commands; on first use the skill fetches
the binary and runs `init --global` for you.

**Per project (the "drag and drop"):**
```sh
cd /path/to/your/project
projx-engine init          # installs the connector, seeds the floor, indexes the code
```
That's it. Open Claude Code in that folder and it just works: each turn gets the lean
floor + the sliced-to-your-task context, off-limits paths are blocked, and `/projx:*`
slash commands are available.

**Multi-repo project?** Declare the repos once:
```sh
projx-engine init                                   # in a workspace folder
projx-engine map sync ../Evolution ../Frontend ../Api   # index them into one store
```
From then on the SessionStart refresh keeps them indexed, and **focus auto-tracks the
repo you're editing** (edit `Evolution/…` → its context leads; jump to `Frontend/…` → it
shifts). Override with `@focus <repo>` / `@unfocus`.

To turn it on for a friend's existing project: they run the one-line installer above, then
`projx-engine init` in the repo. No config files to write by hand.

> **Building from source** (contributors): `make install` git-stamps the version and installs
> to `~/.local/bin` (Windows: `.\build.ps1`). The one-line installers above are the supported
> path for everyone else.

---

## How knowledge gets in (four levels, cheapest first)

The value is that most of it is automatic. You rarely hand-author anything.

| Level | Source | Cost | What it catches |
|---|---|---|---|
| **−1** | **You, by hand** — `projx-engine store commit …` or `/projx:remember` | free, manual | anything, precisely |
| **0** | **Doc-comments** — the code-map pulls each symbol's leading `//` comment | free, automatic | concepts you already documented |
| **1** | **Body terms** — the code-map also indexes what each function *calls* + its string literals | free, automatic, deterministic | a concept buried *inside* a function (a `webhook` handler named `processInbound`) |
| **2** | **Model summaries** — a cheap batch pass tags each symbol semantically | tiny model cost, cached | the truly implicit — a concept written *nowhere* in the code |

- **Level −1** is the fallback and the fast path: `@remember webhooks live in router.go:1527`
  and the agent turns your aside into a clean, permanent doc record with an anchor.
- **Level 0/1** are on by default — `map sync` extracts them. Well-named, well-commented
  code needs almost no manual seeding.
- **Level 2** (semantic) auto-engages only when keyword matching is ambiguous, so you pay
  for a model only when it actually helps. Native uses your own `claude` CLI (no extra
  key); the deployed cell uses `PROJX_AI_KEY`.

---

## Bake your rules — a seed file (Level −1, made shareable)

Rather than run `store commit` by hand, declare your project's knowledge in an editable
**`projx.seed.toml`** and bake it in:

```toml
[[convention]]
key  = "deploy-is-prod"
body = "Pushing to main deploys production. Confirm first."

[[gate]]
pattern = "secret/**"

[[adr]]
key  = "stateless-jwt"
body = "Stateless JWT over server sessions — horizontal scale, no session store."

[[doc]]
key    = "billing/stripe/webhook"
anchor = "Evolution/internal/router/router.go:1527"
body   = "Stripe webhook verified + dispatched inside router.Setup."

[[route]]
class = "cheap-fast"
cmd   = "claude --model claude-haiku-4-5-20251001"
```

```sh
projx-engine seed apply       # upsert every record; PRUNE ones you deleted from the file
projx-engine seed export      # dump the store's human records back to a seed file
```

The **file is the source of truth**: edit it and re-apply to re-bake (idempotent, prunes
removals). `init` applies it automatically — so a friend who clones your repo gets your
whole rule-set from one `init`. The auto code-map is *not* exported (it's regenerated by
`map sync`).

## Providers are agnostic — you write the integrations

ProjX never hardcodes a vendor. It makes two kinds of provider calls, and both are **data
you control**, not compiled-in assumptions:

- **Agent launch** (the work) — the model tier map (`[[route]]` records). Edit the command,
  routing follows.
- **One-shot completions** (triage + decompose splitting) — a declared **integration**. The
  engine carries zero vendor flags; how it reaches a model is a record.

Claude Code ships as the *default* integration — a replaceable datum, not code. Point ProjX
at anything else by declaring an integration and marking it active:

```toml
# An OpenAI-compatible endpoint (OpenRouter, a local server, anything speaking the API).
[[integration]]
name        = "local-llm"
transport   = "http-openai"
base_url    = "http://localhost:11434/v1"
api_key_env = "LOCAL_LLM_KEY"     # the env var NAME holding the key — never the key itself
model       = "qwen2.5-coder"
active      = true

# Or a different agent CLI — {prompt}/{model} substitute as whole args (spaces are safe).
[[integration]]
name       = "my-cli"
transport  = "cli"
template   = "my-agent --print {prompt} -m {model}"
```

`seed apply` flips the active provider with no code change. Two transports cover the field:
`http-openai` (any OpenAI-compatible server — fully neutral) and `cli` (drive whatever
one-shot agent binary you already run). Nothing in ProjX's logic is vendor-specific.

## What happens each turn (the lifecycle)

`settings.json` points every Claude Code hook at one Go command — `projx-engine hook`
(no bash, no `jq`; cross-platform):

- **SessionStart** → refresh the code-map, inject the **lean floor** (the protocol + the
  binding law: conventions + off-limits gates).
- **UserPromptSubmit** → inject the **delta**: the law re-asserted + only the new/changed
  records relevant to *this* message (ranked, capped, balanced across repos, focused on
  your active repo). Already-seen records aren't re-sent.
- **PreToolUse (Bash/Read/Edit/Write/MultiEdit/NotebookEdit)** → **gate**: block a
  tool call whose target path or shell command reaches off-limits data
  (`secret/**`, `.env*`, keys) — enforced, not advised.
- **PreCompact** → mark the floor lost so the next turn refills after compaction.
- **Stop** → **suggest-only**: if you said `@remember` and nothing was committed, nudge
  once (never nags).

Many Claude Code sessions can share one store while each keeps its own delta cursor and
focus — shared knowledge, independent per-session state.

---

## Routing (cheapest-first)

`projx-engine run <task>` picks the model tier by rule, spending the cheapest that fits:

- **haiku** — the majority: renames, lookups, small edits, most worker tasks.
- **sonnet** — when haiku isn't enough: features, tests, reviews.
- **opus** — rarely: architecture, cross-repo redesign, hard debugging.

Escalate-on-uncertainty (unsure → up a tier, never down). Standing overrides:
`projx-engine route pin opus` / `route floor sonnet`; per-message `@cheap` / explicit tier
always win. The tier model IDs live in the store, editable, not hardcoded.

---

## Deployed as a WASM cell (advanced)

The same brain compiles to a Pulp WASM cell and serves its API over HTTP
(`/api/context/*`, `/api/gate/check`, `/api/route`, …). Point a connector at it with
`PROJX_CELL_URL=http://host:port` and the standard `projx-engine hook` drives the
*deployed* cell instead of the local store — brain in WASM, hands (fs / model / process)
as Pulp capabilities. See `cell/` and `host/`.

---

## Honest limitations

- **Keyword matching sees words, not meaning.** A concept nowhere in a symbol's name,
  signature, comment, *or body* won't match at Level 0/1 — that's what Level −1 (seed a
  doc once) and Level 2 (semantic) are for.
- **God-functions** (1000-line do-everything functions) match many queries and rank for
  none; their anchor is the function start, not the buried concept. Better-factored code
  indexes better.
- **Slice relevance is size-fair, not omniscient.** Per-repo balancing + focus steer it,
  but they can't invent a match the words don't contain.

---

## Command reference

```
Setup
  projx-engine init [stacks…] [--force]   ProjX-enable a project (connector + seed + map)
  projx-engine init --global              install the global lifecycle hook + floor + skill
  projx-engine init --workspace           mark the current folder a multi-repo workspace
  projx-engine uninstall [--global]       remove the connector (or the global hook + skill)
  projx-engine status                     show install health: hook, skill, store, PATH, records
  projx-engine version [--check]          print the version (--check compares the latest release)

Knowledge
  projx-engine map sync [repo dirs…]      index symbols (multi-repo workspace if dirs given)
  projx-engine seed apply|export [file]   bake / dump the declarative projx.seed.toml
  projx-engine store commit --kind K --key K/path --body "…"   add knowledge (Level −1)
  projx-engine store list [--kind …] | query <text> | get <id>
  projx-engine gate add <glob> | gate check <path>
  projx-engine secret …                   manage secrets by codename (never the material)

Work
  projx-engine route <task> | route pin|floor <tier> | route clear pin|floor
  projx-engine run [--dry-run] <task>     route one task → deterministic op or agent tier
  projx-engine dispatch [--run] <message> decompose a multi-task message; route each; fan out
  projx-engine verify [--no-build|--behavior-only]   boundaries + drift + real build/test gate
  projx-engine override <rule> --reason … request a soft-rule override (human-delegated)
  projx-engine context [--session <id>] [--task "…"]   print the injected context
  projx-engine hook                       Claude Code lifecycle handler (used by settings.json)
```

---

## License

[MIT](LICENSE) © 2026 SirNiklas9.
