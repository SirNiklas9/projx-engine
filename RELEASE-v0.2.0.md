# ProjX Engine — v0.2.0 (pre-release)

Everything is now genuinely **rule-based**, **agent-agnostic**, and the **Workbench is
live-verified** — clicked through in a real browser, and paired live across Windows and
WSL. This release closes the gaps v0.1.0 left open.

## What changed since v0.1.0

**Agnostic MCP surface (new):**
- `projx-engine mcp` — a standards-compliant stdio MCP server exposing `store_query`,
  `route`, `gate_check`, `impact`, `store_commit`. Any MCP client (Claude Code, Cursor,
  Codex, Cline) can now pull ProjX's knowledge, not just Claude Code via hooks.
- `init` auto-registers it in `.mcp.json` (merge-safe, never clobbers other servers).

**Blast-radius (new):**
- `projx-engine impact <symbol>` — deterministic, self-contained, name-matched "who
  calls this, transitively." No Node dependency, works in the cage. Verified on a real
  multi-hop call chain with depth-capping.

**CodeGraph integration (optional, new):**
- If `codegraph` (github.com/colbymchenry/codegraph) is already installed, `init`
  detects it, builds its index, registers its MCP server, and declares a PREFERENCE as
  an editable store convention — never silently, never required. Native `impact`
  remains the always-available floor. Proven live (installed it for real, verified
  `.mcp.json`, the built index, and the preference record).

**Everything is now a declared rule, not hardcoded (fixed a real gap):**
- The worker directive, cage-mode default, and the model-tier classifier's keyword
  vocabulary are now seeded, editable store records — `store commit` changes behavior
  immediately, no recompile. Caught and fixed a real per-class classifier fallback bug
  in the process (a partially-seeded project could silently lose keyword matching for
  un-seeded classes).

**The Workbench is now genuinely relayed AND live-verified (not just compiled):**
- Store pane, gate rules, agent-launch context, and cross-machine Global sync all now
  relay to the floating engine (`gateFromStore()` and the agent-launch preamble were
  the last two local-only holdouts — closed this release).
- **Found + fixed a real, hidden bug that only an actual launch caught:** the
  Workbench host's default process allow-list never included `projx-engine`, so every
  relay call failed silently ("binary not in allowed set") and fell back to a
  near-empty local store — with no error surfaced in the UI. Fixed in the host's
  default config.
- **Verified with real Playwright automation, not just curl:** launched the actual
  cockpit, clicked through the pairing screen, opened the Agent pane (which
  independently discovered both registered MCP servers via a real Claude Code
  session), opened the Store pane (rendered a record committed via CLI hours earlier
  in a separate session — cross-session proof), and completed a full **read AND
  write** loop through real browser clicks, verified against the CLI afterward.
- **Verified distributed pairing across Windows and WSL, live:** built a genuine
  `linux/amd64` Workbench host inside WSL, paired it to the Windows instance over
  `127.0.0.1` (WSL2's transparent localhost forwarding — no port-proxy config
  needed), and confirmed cross-machine Global knowledge sync actually landed real
  data on the WSL side.

**Init ordering bug fix:** the CodeGraph wiring step ran before the "is this project
empty" check, so on any machine with CodeGraph installed, a truly fresh `init` would
silently skip the entire floor seed (gates, conventions, routes). Fixed by moving
CodeGraph wiring after floor seeding.

## Run it

Unchanged from v0.1.0 — see that release's notes for `install.ps1`/`install.sh` and
`init`. This release is additive: MCP, blast-radius, CodeGraph, and the Workbench
fixes layer on top of the same dispatch-first, gated, floating engine.

## Known limitations (still honest)

- Cross-platform pairing was verified same-box (Windows ↔ WSL VM), not two physically
  separate machines over a real LAN — the mechanism is plain HTTP with no OS-specific
  code, so it should generalize, but that's inference, not click-tested.
- Only Windows-as-brain / WSL-as-hand was tested, not the reverse direction.
- The MCP-server-approval step in the Agent pane was deliberately left unconfirmed
  during testing (granting code-execution access is a real permission decision, not
  one to automate).
