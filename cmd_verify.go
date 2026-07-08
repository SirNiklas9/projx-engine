package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	core "github.com/SirNiklas9/projx-core"
	store "github.com/SirNiklas9/projx-store"
	verify "github.com/SirNiklas9/projx-verify"
)

// runVerifyCmd runs BOTH halves of verify and gates on either:
//  1. boundaries — declared off-limits/architecture rules vs the actual code (as before);
//  2. BEHAVIOR — an auto-detected (or store-declared) build/test command, run on the HOST,
//     gated on its exit code. This closes the field-report gap where a compile-clean change
//     that threw at runtime landed with "success": compile-only no longer counts as verified.
//
// Flags: --no-build skips the behavioral half (boundaries only); --behavior-only skips
// boundaries. `setting/verify-cmd` overrides the detected command.
func runVerifyCmd(absRoot string, args []string) {
	noBuild, behaviorOnly := false, false
	for _, a := range args {
		switch a {
		case "--no-build":
			noBuild = true
		case "--behavior-only":
			behaviorOnly = true
		}
	}

	st := openStore(absRoot)
	defer st.Close()

	failed := false

	// ── 1. Boundaries ─────────────────────────────────────────────────────────
	if !behaviorOnly {
		proj, warns, err := core.ParseDir(absRoot)
		if err != nil {
			die("parse: %v", err)
		}
		for _, w := range warns {
			fmt.Printf("warning: %s\n", w)
		}
		violations := verify.Check(verify.RulesFromStore(st), proj)
		if len(violations) == 0 {
			fmt.Println("verify: boundaries OK — no violations")
		} else {
			failed = true
			for _, v := range violations {
				fmt.Printf("verify: boundary VIOLATION: %s -> %s  [rule: %s->%s note: %s]\n",
					v.Edge.From, v.Edge.To, v.Rule.From, v.Rule.To, v.Rule.Note)
			}
		}
	}

	// ── 1b. Drift (declared facts vs actual filesystem reality) ───────────────
	if !behaviorOnly {
		if checkDrift(absRoot, st) {
			failed = true
		}
	}

	// ── 2. Behavior (build + test) ────────────────────────────────────────────
	if !noBuild {
		cmd := verifyCommand(absRoot, st)
		if cmd == "" {
			fmt.Println("verify: no build/test command detected — set `setting/verify-cmd` to enable the behavioral gate (skipped)")
		} else {
			fmt.Printf("verify: running behavioral gate → %s\n", cmd)
			if err := runHostShell(absRoot, cmd); err != nil {
				failed = true
				fmt.Printf("verify: BEHAVIORAL GATE FAILED (%v) — the change is NOT verified\n", err)
			} else {
				fmt.Println("verify: behavioral gate PASSED (build + tests green)")
			}
		}
	}

	if failed {
		os.Exit(1)
	}
	fmt.Println("verify: OK")
}

// verifyCommand resolves the behavioral command: an explicit `setting/verify-cmd` override
// wins; otherwise it is auto-detected from the project's build system. Empty = no gate.
func verifyCommand(absRoot string, st store.Store) string {
	for _, kind := range []store.Kind{store.KGateRule, store.KConvention} {
		for _, r := range st.List(store.OfKind(kind)) {
			if r.Key == "setting/verify-cmd" && strings.TrimSpace(r.Body) != "" {
				return strings.TrimSpace(r.Body)
			}
		}
	}
	has := func(pattern string) bool {
		m, _ := filepath.Glob(filepath.Join(absRoot, pattern))
		return len(m) > 0
	}
	switch {
	case has("go.mod"):
		return "go build ./... && go test ./..."
	case has("*.sln"), has("*.csproj"):
		return "dotnet build"
	case has("Cargo.toml"):
		return "cargo build && cargo test"
	case has("pom.xml"):
		return "mvn -q -DskipTests=false test"
	case has("package.json"):
		return "npm test --silent"
	}
	return ""
}

// runHostShell runs a (possibly compound) command on the HOST via the platform shell,
// streaming output, from the project root. Non-zero exit → error (the gate fails).
func runHostShell(dir, command string) error {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/c", command)
	} else {
		c = exec.Command("sh", "-c", command)
	}
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
