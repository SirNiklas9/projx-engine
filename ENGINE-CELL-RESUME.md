# ProjX Engine — Cell Pivot + Store Convergence (session 2026-06-26→27)

**One line:** the ENGINE is now a standalone Pulp **cell + host**, separate from the
Workbench, and it **works** — proven live, headless. Everything below is
**uncommitted working tree**; nothing is pushed.

---

## 1. What the engine is now

```
  HTTP clients (Workbench / Neovim / phone / CLI)
        │  /api/store · /api/route · /api/gate · /api/agent/spec
        ▼
  projx-engine-host  (native Pulp host — the HANDS)
    capabilities: transport.http.inbound · storage.sqlite · storage.fs
                  · spawn.process · spawn.pty · entropy.read
        │ loads
        ▼
  cell.wasm  (the BRAIN — pure logic, NO OS access, NO cage code)
    store CRUD/history/undo · CLAUDE.md gen · auto-seed floor
    · store-driven routing (auto model tier) · gate→deny · agent contract
```

- Brain = `projx-engine/cell/` (WASM). Hands = `projx-engine/host/` (native).
- The **cage stays native** — it is NOT in the cell; the cell invokes it via
  `spawn.process` (not wired yet — see gaps).
- Launch: `projx-engine-host -project <repo> -manifest cell/pulp.cell.toml -http-port 7878`

## 2. The model (unchanged, now realised in the cell)

The **store** is the one source of truth ("second deterministic root" — facts you
DECLARE). Everything else is **derived** from it:
- **CLAUDE.md** managed block (steering) — regenerated on every store change.
- **Gate deny rules** — gate-rule records → `Read(glob)/Edit(glob)`.
- **Routing** — classify task by keyword (no LLM) → class → model cmd from `KRoute` records.
- **Agent contract** = model (routing) + deny (gate) + steering (CLAUDE.md).

## 3. What was built/changed this session (all UNCOMMITTED)

### projx-store  (shared library — the single definitions)
- `store.go` — **M**: added `KRoute` kind (+ kindNames `"route"`).
- `claudemd.go` — **NEW**: `ManagedBlock`, `SpliceManagedBlock` (shared CLAUDE.md renderer).
- `seed.go` — **NEW**: `SeedFloor`, `FloorConventions`, `FloorGates` (the floor contract, one definition).
- `routing.go` — **NEW**: `FloorRoutes` (model tiers — the ONLY place the model IDs live), `Classify`, `Route`.
- `gate.go` — **NEW**: `DenyRules` (gate → agent deny, one definition).

### projx-engine  (native binary — CLI + cage + serve)
- `cmd_store.go` — **M**: `openStore` now returns a two-file `Workspace`
  (`projectStore`): project=`<repo>/.projx/store.db`, yours=`<UserConfigDir>/projx`
  (override `PROJX_YOURS_DIR`). Added `yoursDir()`. Added `route` to `parseKindForList`.
- `profiles.go` — **M**: floor's conventions/gates/model-tiers REMOVED (moved to
  projx-store); `init()` builds `floor.ModelTiers` from `store.FloorRoutes`; `Seed`
  uses `store.SeedFloor`; `autoSeed` freshness check scoped to project records.
- `serve.go` — **M**: store endpoints expanded read-only → full
  (list/put/delete/history/undo) with `syncProjectClaudeMD`; temp `__demo` endpoint REMOVED.
- `internal/routing/routing.go` — **M**: `classifyCapability` now delegates to
  `store.Classify` (one classifier).

### projx-engine/cell/  (NEW MODULE — the engine cell)
- `go.mod` (module `projxenginecell`; deps `projx-store` + `Fiber`; replaces `../../`).
- `main.go` — `pulp.OnInit`: auto-seed floor if empty, register routes, dispatch.
- `store.go` — `/api/store` list/put/delete/history/undo + `syncClaudeMD` (pulp.FS).
- `history.go` — `.projx/store-history.jsonl` journal via `pulp.FS`.
- `route.go` — `/api/route` (store-driven model tier).
- `gate.go` — `/api/gate` + `/api/agent/spec` (the assembled contract).
- `pulp.cell.toml` — manifest (caps: http.inbound, storage.fs/sqlite, spawn.process/pty, entropy).

### projx-engine/host/  (NEW MODULE — the engine host)
- `go.mod` (module `projx-engine-host`; Pulp + ext-fs/http/sqlite/process/pty/entropy).
- `main.go` — `run.Main()` + `-project` → `PULP_FS_ROOT_PROJX_ENGINE`. Headless.

## 4. What's PROVEN (live, headless)

- **Native serve** (`projx-engine serve`): store CRUD + history + undo + CLAUDE.md gen.
- **Engine cell** (via `projx-engine-host`): cell loads + serves; fresh boot auto-seeds
  the floor (4 conventions + 4 gates + 3 routes); writes CLAUDE.md + journal into the
  repo via storage.fs; `/api/route` returns correct tier (redesign→opus, rename→haiku,
  test→sonnet); `/api/gate` returns deny rules; `/api/agent/spec?task=…` returns
  `{class, cmd(model), deny}`.
- All tests green: projx-store, projx-engine (incl. internal/routing), native build clean.

## 5. How to run it (resume)

```bash
# build cell (GOWORK=off — there is a go.work in projx-engine)
cd projx-engine/cell && GOWORK=off GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o cell.wasm .
# build host
cd ../host && GOWORK=off go build -o projx-engine-host.exe .
# run (any repo)
proj=<repo>; stor=$(mktemp -d)
projx-engine/host/projx-engine-host.exe -project "$proj" -manifest projx-engine/cell/pulp.cell.toml -http-port 7878 -storage-root "$stor"
# drive
curl localhost:7878/api/store
curl "localhost:7878/api/agent/spec?task=redesign%20the%20auth"
```

## 6. What's STILL MISSING (next bricks, in rough order)

1. **Workbench → engine cell** — THE integration. Workbench cell still has its own
   store/gate/CLAUDE.md/history; point it at the engine cell (relay), retire its copies.
2. **Cage as a native capability** — expose `RunCagedAgent` so the cell can request a
   caged run via `spawn.process`. (Linux only; verify on WSL.)
3. **Agent EXEC** — actually launch the agent (uncaged via spawn.pty; caged via #2),
   applying the assembled contract. Today only the *spec* is built.
4. **Cell two-file / repo store** — cell still uses ONE `storage.sqlite` db in HOST
   storage; native uses `<repo>/.projx/store.db`. Needs `ext-sqlite` repo-path so
   cell + native share the committable project store. (Only CLAUDE.md + journal are
   in the repo today.)
5. **Remaining policy leaks → store**: `cage.json` (net/tools) and `agents.json`
   (templates) are still files; native `routing.LoadConfig` still reads `routing.json`
   instead of `KRoute`; deterministic-op triggers + `agentWritableKind` still hardcoded.
6. **Retire native `serve.go`** (superseded by the cell) OR keep as the CLI path — decide.
7. **Cosmetic**: cell `/api/store?kind=` filter not honored like native serve's.

> Honest caveat: there are temporarily THREE store impls in flight (workbench cell,
> native engine, engine cell). That resolves once #1 + #6 land.

## 7. COMMIT PLAN (for approval — nothing committed yet)

Branch `audit-2026-06-25`, no upstream → fully safe to amend/fixup. One-line
conventional messages, NO Co-Authored-By.

**First: gitignore the build artifacts** (do NOT commit):
`projx-engine/cell/cell.wasm`, `projx-engine/host/projx-engine-host.exe`,
`projx-engine/projx-engine.exe`, `projx-engine/projx-engine.exe~`. Delete `*.exe~`.
(Generated `CLAUDE.md` files: decide commit-or-ignore.)

Suggested commits:
- **projx-store**: `feat: shared definitions — CLAUDE.md renderer, floor seed, KRoute routing, gate deny`
  (or split into 4: claudemd / seed / routing / gate).
- **projx-engine native**: fold the store-convergence fixes into their matching
  existing feature commits (`271b291` auto-seed, `0313ac2` serve) via `--fixup` +
  autosquash so history reads first-time-right; OR new commits:
  `feat: two-file Workspace store`, `feat: full store API in serve`, `refactor: floor/routing single-definition via projx-store`.
- **projx-engine cell+host**: NEW — `feat: engine as a Pulp cell (store/routing/gate/contract control plane)` + `feat: minimal engine host`.

## 8. Verification still owed (can't do headless on Windows)

- A real agent run through the engine.
- The cage end-to-end (Linux/WSL).
- The live cockpit using the engine.

---
*Engine works as a control-plane brain. The hands (exec + cage) and the Workbench
integration are the next, separately-verified phase.*
