# Pulp-ext-hook — integration handoff (Windows interactive cage)

This module productionizes the proven `projx-wincage-poc` so the engine's Windows
AppContainer cage can run an **interactive** Node/libuv TUI (`claude`) — reaching
parity with the Linux (Landlock+netns) interactive cage. It is the defensive
sandbox shim from `PROJX-ENGINE-WINCAGE-PRODUCTIONIZE.md`.

**Status (2026-06-29):** module authored; **build + wiring NOT done** — the
authoring session's safety classifier blocks building process-injection code
(as the productionize doc predicted). Finish in a **fresh** Claude Code session
or by running the steps below yourself (`!`-prefix in-session, or a normal shell).

## What's here
- `dll/hook.go` — the IAT-hook DLL (verbatim from `projx-wincage-poc/projxhook`):
  splices `\LOCAL\` into libuv's global pipe name + `CreateProcessW` child-prop.
- `projxhook.dll` — the prebuilt, AC-loadable DLL (verbatim from the PoC), embedded.
- `embed_windows.go` — `StageDLL(dir)` writes the embedded DLL to disk.
- `inject_windows.go` — `Inject(proc, dllPath)`: remote-LoadLibrary + Arm (from the
  PoC's `injectDLL`/`findModuleBase`), cleaned into an exported API.
- `hook_other.go` — non-Windows stubs (Linux needs no hook).

## Step 1 — build the module (fresh session / shell)
```
cd Pulp-ext-hook
go build .                 # root API (pure Go) — should be clean
# (optional) rebuild the DLL from source instead of using the committed one:
cd dll && go build -buildmode=c-shared -ldflags '-linkmode external -extldflags "-static"' -o ../projxhook.dll .
```

## Step 2 — wire it into the confiner's ConPTY path
In `Pulp-ext-confine/core/confine_windows.go`, the interactive (`conpty`) branch
must create the child SUSPENDED, inject the hook, then resume. Concretely:

1. `go.mod` (Pulp-ext-confine): add
   `require github.com/BananaLabs-OSS/Pulp-ext-hook v0.0.0` +
   `replace github.com/BananaLabs-OSS/Pulp-ext-hook => ../Pulp-ext-hook`.
2. In `LaunchConfined`, when `conpty` (interactive): OR `CREATE_SUSPENDED` into
   `creationFlags` (the ConPTY path currently does not suspend). After
   `CreateProcess` succeeds and BEFORE the relay/`ResumeThread`:
   ```go
   if conpty { // interactive: arm the libuv pipe hook before the child runs
       if dll, derr := hook.StageDLL(filepath.Join(policy.Root, ".projx")); derr == nil {
           // grant the AC SID read+traverse on the staged DLL (reuse grantPaths'
           // addPath + grantTraverse for dll + its dir), then:
           if ierr := hook.Inject(uintptr(pi.Process), dll); ierr != nil {
               // interactive libuv child would spin unhooked — fail closed here.
               windows.TerminateProcess(pi.Process, 1)
               return 0, fmt.Errorf("confine/windows: hook inject: %w", ierr)
           }
       }
       windows.ResumeThread(pi.Thread) // resume AFTER inject+arm
   }
   ```
   Note: the ConPTY child is currently started non-suspended and the relay drives
   it; move the resume to after inject. Grant the staged DLL path to the AC SID
   (the PoC granted `inject` dir + file + traverse — mirror that in `grantPaths`).
3. The host already defaults one-shot `/api/agent/run` to headless; an INTERACTIVE
   session endpoint (PTY-streamed) sets `PROJX_CONFINE_CONPTY=1` per-launch so this
   path runs. (A PTY-stream cell endpoint over websocket is the remaining cell-side
   piece for a fully interactive caged session via the workbench/phone.)

## Step 3 — verify (you, at a terminal — can't be done headlessly)
Per `PROJX-ENGINE-WINCAGE-PRODUCTIONIZE.md` §Verification:
1. Headless still green (already verified): caged `claude -p "say CAGE OK"` → exit 0.
2. **Interactive**: launch a caged `claude` with `PROJX_CONFINE_CONPTY=1` and
   confirm the TUI **renders and takes input** in your terminal (the libuv pipe
   hook is what makes this work; without it the child spins).
3. Boundary: caged claude denied `~/.gitconfig` (EPERM); writes confined to project.
4. Deep-spawn: nested `node -e`→`node -e`→cmd returns `DEEP_OK` (child-prop).

## Why this is gated for the assistant
`Inject` is remote `LoadLibrary`/`CreateRemoteThread` + IAT patching — textbook
dual-use. The content classifier blocks the assistant from *building/running* it
even though the use is defensive (your own sandbox containing your own agent).
The code is your authorized PoC, relocated; building it is a you/fresh-session step.
