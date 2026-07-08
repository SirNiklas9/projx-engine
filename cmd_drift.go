package main

// cmd_drift.go — declared-vs-actual DRIFT checking for `verify` (field-report #8: declared
// knowledge was blind to operational reality — paths, versions, config shape). A project
// declares FACTS as records with a "fact/" key and a small JSON body; verify evaluates each
// against the real filesystem and reports drift. Deterministic, no engine.
//
// Declare a fact:
//   store commit --kind doc --key fact/db-path   --body '{"check":"path-exists","target":"data/store.db"}'
//   store commit --kind doc --key fact/version   --body '{"check":"version-equals","target":"go.mod","expect":"v0.5.0"}'
//   store commit --kind doc --key fact/brand      --body '{"check":"file-contains","target":"app.json","expect":"ARSENAL"}'

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

type driftFact struct {
	Check  string `json:"check"`  // path-exists | file-contains | version-equals
	Target string `json:"target"` // path (relative to root, or absolute)
	Expect string `json:"expect"` // substring/version for file-contains/version-equals
}

// checkDrift evaluates every declared fact (records keyed "fact/…") against reality and
// prints OK / DRIFT for each. Returns true if any fact drifted. A fact whose target is an
// off-limits path is refused (never read) — secrets-by-codename holds even here.
func checkDrift(absRoot string, st *projectStore) (failed bool) {
	facts := st.List(store.Filter{KeyPrefix: "fact/"})
	checked := 0
	for _, r := range facts {
		var f driftFact
		if json.Unmarshal([]byte(strings.TrimSpace(r.Body)), &f) != nil || f.Check == "" {
			continue // not a structured fact — skip silently (plain fact/ docs are fine)
		}
		checked++
		if pat, denied := gateDeniedPath(st, gateRelPath(absRoot, absRoot, f.Target)); denied {
			fmt.Printf("verify: fact %s SKIPPED — target %q is off-limits (gate %q)\n", r.Key, f.Target, pat)
			continue
		}
		ok, detail := evalFact(absRoot, f)
		if ok {
			fmt.Printf("verify: fact OK — %s (%s)\n", r.Key, f.Check)
		} else {
			failed = true
			fmt.Printf("verify: DRIFT — %s: %s\n", r.Key, detail)
		}
	}
	if checked == 0 {
		fmt.Println("verify: no declared facts to drift-check (declare with a `fact/…` record)")
	}
	return failed
}

func evalFact(absRoot string, f driftFact) (ok bool, detail string) {
	target := f.Target
	if !filepath.IsAbs(target) {
		target = filepath.Join(absRoot, target)
	}
	switch f.Check {
	case "path-exists":
		if _, err := os.Stat(target); err == nil {
			return true, ""
		}
		return false, fmt.Sprintf("path does not exist: %s", f.Target)
	case "file-contains", "version-equals":
		data, err := os.ReadFile(target)
		if err != nil {
			return false, fmt.Sprintf("cannot read %s: %v", f.Target, err)
		}
		if strings.Contains(string(data), f.Expect) {
			return true, ""
		}
		return false, fmt.Sprintf("%s does not contain expected %q", f.Target, f.Expect)
	}
	return false, fmt.Sprintf("unknown check %q (want path-exists|file-contains|version-equals)", f.Check)
}
