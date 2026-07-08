# ProjX Engine — v0.4.0 (pre-release) · session handoff

**One line:** the engine now knows and reports its own version — `projx-engine
version [--check]` — the version is **stamped from git at build time (never
hardcoded)**, and the `projx` skill **self-updates** a stale binary before
bootstrapping. Everything below is **committed locally but NOT pushed**; the
`v0.4.0` tag is local-only.

---

## What changed since v0.3.0

**`version` command (new):**
- `projx-engine version` — prints the release version + the `go build`-stamped
  commit / build time / toolchain. No more `strings`-ing the binary.
- `projx-engine version --check` — queries the GitHub latest-release tag and
  reports `up to date` / `update: available vX -> vY`. Read-only; fails soft
  when offline, rate-limited, or under confined egress. Dev builds report
  "dev build — build from a tagged release to compare" instead of a false hit.
- Wired into dispatch (with `--version` / `-v` aliases), the usage text, and the
  agent-context read-only allowlist.

**Version is stamped from git, not hardcoded (the important fix):**
- Source carries only `var version = "dev"`. The real number is injected at build
  time via `-ldflags "-X main.version=$(git describe --tags --always --dirty)"`.
- Resolution order at runtime: ldflag-stamped value → Go module version
  (`go install pkg@vX`) → Go's built-in VCS stamping → `dev`.
- Canonical stamped builds: **`Makefile`** (`make build` / `make install` /
  `make version`) and **`install.ps1`** (Windows). README + connector docs
  updated to point at these instead of a raw `go build`.

**Skill auto-update (new):**
- `skill/SKILL.md` step 1 now runs `version --check` before bootstrapping and,
  if the binary is stale, downloads the latest asset and swaps it — with an
  explicit **Windows-safe** note (write `projx-engine.exe.new`, then
  `Move-Item -Force`, since a running `.exe` can't be overwritten in place).

## Correction logged this session (audit note)

The local clone was **behind the remote**: it only had the `v0.2.0` tag, so an
initial attempt to cut `v0.3.0` collided with an **already-published** remote
`v0.3.0` release (commit `fa8168d`, assets: linux amd64/arm64 + windows amd64).
That mistaken local tag was deleted, real tags were fetched, and the
version-command work was correctly assigned to **v0.4.0**. **Lesson:** `git
fetch --tags` before cutting a release; the highest local tag is not the truth.

Also: `RELEASE-v0.3.0.md` was never written — the release-notes pattern lapsed
at v0.3.0. This doc restarts it at v0.4.0.

## Verification done

- Builds clean (`go vet` clean for the new file; pre-existing `cmd_bootstrap.go`
  unkeyed-field warnings are untouched).
- `version` and `version --check` exercised against the live GitHub API →
  correctly reports `up to date (ahead of latest release v0.3.0)`.
- Both build paths verified: plain `go build` (VCS-stamped) and ldflags-stamped.
- Installed binary at `~/.local/bin/projx-engine` reports clean `v0.4.0`.

## Git state (local, unpushed)

```
0e86393  refactor: stamp version from git at build time instead of hardcoding
b181b06  docs(skill): auto-update via 'version --check' before bootstrap
cb0c3f8  feat: add 'version [--check]' command
```
Tag `v0.4.0` → `0e86393`. Nothing pushed; remote latest release is still v0.3.0.

## To publish (still TODO — deploy step, needs Nick)

1. `git push origin main --tags`  (pushes `v0.4.0`)
2. Build the three release assets, git-stamped:
   - `projx-engine_linux_amd64`, `projx-engine_linux_arm64`,
     `projx-engine_windows_amd64.exe`
   - e.g. `GOOS=linux GOARCH=amd64 make build` (rename per asset)
3. `gh release create v0.4.0 <assets>` with notes from this file.

Until a v0.4.0 release is published, `version --check` on other machines will
still see v0.3.0 as latest and the skill won't auto-update to v0.4.0.
