// Command projx-engine-host is a Pulp host carrying the capabilities the
// projx-engine CELL declares: transport.http.inbound, storage.fs,
// storage.sqlite, spawn.process, spawn.pty, entropy.read. It loads the engine
// cell and serves its control plane — headless by default (the engine is a
// control plane, not a UI). Many cells per host: pass -manifest repeatedly.
//
//	projx-engine-host -project /repo -manifest cell/pulp.cell.toml -http-port 7878
//
// -project wires the cell's storage.fs scope to that repo (so CLAUDE.md + the
// history journal land in the repo).
//
// The CAGE is a Pulp capability here (spawn.confine = Pulp-ext-confine), NOT a
// native shell-out: the cell (brain) requests a caged run via pulp.Confine and
// THIS host (hands) performs the confined launch — Landlock on Linux /
// AppContainer on Windows, egress netns/gVisor on Linux. Build-on-Pulp law:
// brain = cell, hands = Pulp capabilities, no native executor in the path.
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "github.com/BananaLabs-OSS/Pulp-ext-confine"
	_ "github.com/BananaLabs-OSS/Pulp-ext-entropy"
	_ "github.com/BananaLabs-OSS/Pulp-ext-fs"
	_ "github.com/BananaLabs-OSS/Pulp-ext-http"
	_ "github.com/BananaLabs-OSS/Pulp-ext-process"
	_ "github.com/BananaLabs-OSS/Pulp-ext-pty"
	_ "github.com/BananaLabs-OSS/Pulp-ext-sqlite"

	egress "github.com/BananaLabs-OSS/Pulp-ext-egress/core"
	"github.com/BananaLabs-OSS/Pulp/run"
)

// cellEnvKey must match the engine cell's manifest name, upper-cased: ext-fs
// reads the fs root from PULP_FS_ROOT_<CELLNAME>. Cell name is "projx-engine".
const cellEnvKey = "PROJX_ENGINE"

func main() {
	// egress.Init() MUST run first: the Linux composed caged launch re-execs
	// /proc/self/exe with NETGW_MODE=child to become the netns gateway child.
	// Init detects that mode, builds the namespace, applies Landlock via the
	// PreExecHook, execs the confined target, and exits — so the re-exec never
	// falls through to run.Main(). No-op on non-Linux.
	egress.Init()

	// Provide host facts to the cell so the brain can build an OS-correct caged
	// policy without knowing host internals (runtime.GOOS is "wasip1" in the
	// cell). Pulp forwards these to the cell via its env allowlist.
	setIfUnset("PROJX_HOST_OS", runtime.GOOS)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		setIfUnset("PROJX_HOST_HOME", home)
	}
	// Launch mode (headless vs ConPTY vs new-console) is chosen PER-LAUNCH by the
	// cell via policy.Env (e.g. /api/agent/run sets PROJX_CONFINE_HEADLESS for a
	// one-shot subagent). The host does NOT force a global default, so a headless
	// one-shot isn't overridden into an interactive ConPTY relay it has no console
	// for. An interactive session endpoint will set PROJX_CONFINE_CONPTY itself.

	// Translate -project <repo> into the env knobs the capabilities read, then hand
	// the remaining flags to run.Main (which owns -manifest/-http-port/-storage-root).
	if proj, rest := takeFlag(os.Args[1:], "project"); proj != "" {
		if abs, err := filepath.Abs(proj); err == nil {
			setIfUnset("PULP_FS_ROOT_"+cellEnvKey, abs)
			setIfUnset("PROCESS_RUN_ROOTS", abs)
			// Default the caged agent's project root to this repo.
			setIfUnset("PROJX_ROOT", abs)
		}
		os.Args = append([]string{os.Args[0]}, rest...)
	}
	run.Main()
}

func setIfUnset(k, v string) {
	if os.Getenv(k) == "" {
		_ = os.Setenv(k, v)
	}
}

// takeFlag removes "-name value" / "--name=value" from args, returning the value.
func takeFlag(args []string, name string) (string, []string) {
	out := make([]string, 0, len(args))
	val := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-"+name || a == "--"+name:
			if i+1 < len(args) {
				val = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-"+name+"="):
			val = strings.SplitN(a, "=", 2)[1]
		case strings.HasPrefix(a, "--"+name+"="):
			val = strings.SplitN(a, "=", 2)[1]
		default:
			out = append(out, a)
		}
	}
	return val, out
}
