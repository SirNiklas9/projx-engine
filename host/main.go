// Command projx-engine-host is a Pulp host carrying the capabilities the
// projx-engine CELL declares: transport.http.inbound, storage.fs,
// storage.sqlite, spawn.process, spawn.pty, entropy.read. It loads the engine
// cell and serves its control plane — headless by default (the engine is a
// control plane, not a UI). Many cells per host: pass -manifest repeatedly.
//
//	projx-engine-host -project /repo -manifest cell/pulp.cell.toml -http-port 7878
//
// -project wires the cell's storage.fs scope to that repo (so CLAUDE.md + the
// history journal land in the repo). The cage stays NATIVE — it is not loaded
// here; the cell invokes it via spawn.process when a caged run is requested.
package main

import (
	"os"
	"path/filepath"
	"strings"

	_ "github.com/BananaLabs-OSS/Pulp-ext-entropy"
	_ "github.com/BananaLabs-OSS/Pulp-ext-fs"
	_ "github.com/BananaLabs-OSS/Pulp-ext-http"
	_ "github.com/BananaLabs-OSS/Pulp-ext-process"
	_ "github.com/BananaLabs-OSS/Pulp-ext-pty"
	_ "github.com/BananaLabs-OSS/Pulp-ext-sqlite"

	"github.com/BananaLabs-OSS/Pulp/run"
)

// cellEnvKey must match the engine cell's manifest name, upper-cased: ext-fs
// reads the fs root from PULP_FS_ROOT_<CELLNAME>. Cell name is "projx-engine".
const cellEnvKey = "PROJX_ENGINE"

func main() {
	// Translate -project <repo> into the env knobs the capabilities read, then hand
	// the remaining flags to run.Main (which owns -manifest/-http-port/-storage-root).
	if proj, rest := takeFlag(os.Args[1:], "project"); proj != "" {
		if abs, err := filepath.Abs(proj); err == nil {
			setIfUnset("PULP_FS_ROOT_"+cellEnvKey, abs)
			setIfUnset("PROCESS_RUN_ROOTS", abs)
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
