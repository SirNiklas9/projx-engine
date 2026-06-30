# ProjX Windows cage — runbook

The Windows cage runs an agent inside an **AppContainer** (kernel FS confinement +
network capability gating). Two modes, both built and wired into the engine
(`Pulp-ext-confine` + `Pulp-ext-hook`):

- **Headless** (one-shot subagents, `claude -p …`) — works out of the box, no setup.
- **Interactive** (a `claude` TUI) — needs the two one-time steps below, because a
  Node/libuv TUI under an AppContainer needs (a) a pipe-namespace hook and (b)
  permission to traverse `C:\`.

> Linux is the reference (Landlock + netns). On Windows, network is coarse
> (`internetClient` = any host; no per-host wall) and there is **no shell** under
> AppContainer (`msys-2.0.dll` fails to init) — use WSL for shell/build agents.

---

## One-time setup (interactive only)

### 1. Allow AppContainers to traverse `C:\` (admin, once)
A caged Node app `realpath`s its script path up to the drive root at startup;
without this it dies `EPERM: lstat 'C:\'`. Right-click → **Run as administrator**:

```
projx-engine\host\grant-croot.cmd
```

(Grants `ALL APPLICATION PACKAGES` traverse on the `C:\` directory itself only —
non-recursive. One-time, covers every cage. The cage runs unprivileged after.)

### 2. Build the engine cell + host
```sh
cd projx-engine/cell && GOWORK=off GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o cell.wasm .
cd ../host          && GOWORK=off go build -o projx-engine-host.exe .
# (the hook DLL is embedded/prebuilt; rebuild it only if you change dll/hook.go:
#  cd ../../Pulp-ext-hook/dll && go build -buildmode=c-shared -ldflags "-linkmode external -extldflags -static" -o ../projxhook.dll .)
```

---

## Run a caged agent

### Headless one-shot (no setup needed)
```sh
# in a shell: start the host (headless control plane)
projx-engine-host.exe -project C:\path\to\repo -manifest cell\pulp.cell.toml -http-port 7878
# in another shell: launch a caged subagent
curl -X POST http://127.0.0.1:7878/api/agent/run -H "Content-Type: application/json" \
     -d "{\"task\":\"fix the typo in README\",\"caged\":true}"
curl "http://127.0.0.1:7878/api/agent/run/status?id=<jobID>&caged=true"
```
The subagent runs in the AppContainer (FS-confined to the repo, network gated),
commits what it learns to the store, and exits. Verified e2e on Windows + WSL.

### Interactive TUI (after the one-time setup)
Run the host **in a real terminal** (the ConPTY relays claude's TUI to it) with
ConPTY mode on, then trigger a caged run:
```sh
set PROJX_CONFINE_CONPTY=1
projx-engine-host.exe -project C:\path\to\repo -manifest cell\pulp.cell.toml -http-port 7878
# then (another shell): POST /api/agent/run {"task":"…","caged":true}
```
The confiner creates the agent suspended, injects `projxhook.dll` (IAT-splices the
AppContainer-local `\LOCAL\` pipe namespace into libuv's pipe name + propagates the
hook to child processes), resumes it, and relays the ConPTY to your terminal. The
`claude` TUI renders and takes input.

---

## Verify the hook itself (optional, no engine)
The PoC proves the libuv fix directly (headless):
```sh
A=projx-wincage-poc/achook/achook.exe ; D=projx-wincage-poc/projxhook/projxhook.dll
# Stage A — no hook → HANGS (libuv pipe spin in the AppContainer):
"$A" -root C:\Users\Public\cagetest\A -timeout 12 -- node -e "require('child_process').spawnSync(process.execPath,['-e','0']);console.log('OK')"
# Stage C — inject hook → COMPLETES ("Arm patched 5 IAT slots", prints OK):
"$A" -root C:\Users\Public\cagetest\C -timeout 12 -inject "$D" -- node -e "require('child_process').spawnSync(process.execPath,['-e','0']);console.log('OK')"
```
(Use a shallow root like `C:\Users\Public\…`; the PoC `achook` itself has the old
recursive-icacls hang the engine confiner has since fixed.)

## Troubleshooting
- **`EPERM: lstat 'C:\'`** → step 1 not run (or not as admin).
- **caged run hangs on a Node agent** → the hook didn't arm; confirm step 1 +
  `PROJX_CONFINE_CONPTY=1`; run the host with `PROJX_CONFINE_DEBUG=1` and look for
  `hook injected + armed` / `Arm patched N IAT slots` on stderr.
- **launch fails finding the agent** → ensure `claude` (or `PROJX_AGENT`) and
  `projx-engine` are on the host's PATH so the jail can resolve them.
