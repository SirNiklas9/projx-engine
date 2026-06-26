//go:build linux

package main

import (
	"fmt"
	"strings"

	cage "github.com/BananaLabs-OSS/Pulp-cage"
	fusecore "github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
	grants "github.com/BananaLabs-OSS/Pulp-grants"
	store "github.com/SirNiklas9/projx-store"
)

// runAgentCaged runs the agent worker inside the FULL kernel cage (FUSE floor +
// Landlock + egress netns) — the opt-in, Linux-only path for a verify-loop
// iteration. The project is RW through the FUSE floor EXCEPT gate prefixes
// (secrets), egress is the seeded allowlist, and misses fail closed (no human to
// approve during an autonomous loop). Per feedback-cage-optional-not-required
// this is additive — runAgentHeadless (uncaged) is the cross-platform default.
func runAgentCaged(absRoot, task string) error {
	cfg := loadCageConfig(absRoot)

	// FUSE static policy: whole project RW (empty-prefix catch-all), with gate
	// prefixes denied (None → OnMiss → the deny-approver → blocked).
	rules := []fusecore.Rule{{Prefix: "", Access: fusecore.ReadWrite}}
	for _, p := range gatePrefixes(absRoot) {
		rules = append(rules, fusecore.Rule{Prefix: p, Access: fusecore.None})
	}

	name, args := resolveAgentArgv(absRoot, task)
	spec := cage.AgentSpec{
		Argv:        append([]string{name}, args...),
		ProjectRoot: absRoot,
		Store:       grants.NewMemStore(),
		Approver:    denyApprover{}, // autonomous → fail closed on misses
		NetAllow:    cfg.NetAllow,
		FSAllow:     rules,
		Env:         map[string]string{"PROJX_AGENT_CONTEXT": "1"},
	}
	res, err := cage.RunCagedAgent(spec)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("caged agent exited %d", res.ExitCode)
	}
	return nil
}

// denyApprover refuses every live request — fail-closed for autonomous runs.
type denyApprover struct{}

func (denyApprover) Decide(grants.Request) grants.Decision { return grants.Decision{Access: 0} }

// gatePrefixes reads the project's gate rules and extracts a path prefix from
// each pattern (up to the first glob char): "secret/**" → "secret". Patterns
// that start with a glob ("**/*.key") yield no usable prefix and are skipped —
// the agent's own deny-gate + Landlock still apply to those.
func gatePrefixes(absRoot string) []string {
	st := openStore(absRoot)
	defer st.Close()
	var out []string
	for _, r := range st.List(store.OfKind(store.KGateRule)) {
		if p := globPrefix(r.Body); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func globPrefix(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if i := strings.IndexAny(pattern, "*?["); i >= 0 {
		pattern = pattern[:i]
	}
	return strings.Trim(pattern, "/")
}
