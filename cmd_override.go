package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmd_override.go — `projx-engine override <rule> --reason <why> [--ttl N]`.
//
// SOFT enforcement (see doc/enforcement-follow-override-plan): a soft rule is
// FOLLOWED by default — the PreToolUse hook denies the blocked action — but the
// agent or human may proceed by recording a REASONED override. This command:
//
//	(1) writes a one-shot (default) / N-use TTL GRANT to <root>/.projx/overrides.json
//	    that the hook consumes to allow the next matching action, and
//	(2) appends an audit entry to <root>/.projx/override-log.jsonl (rule, reason,
//	    session, timestamp) so every deviation is explicit and logged — never silent.
//
// HARD rules (the secrets + off-limits floor) are NOT overridable and this command
// refuses them: an off-limits gate deny is a wall, not a speed bump.

// overrideGrant is one pending bypass for a soft rule. Uses is decremented each time
// the hook consumes it; the grant is dropped when Uses reaches 0 or Expiry passes.
type overrideGrant struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	Uses   int    `json:"uses"`
	Expiry int64  `json:"expiry"` // unix millis; 0 = no time bound (use-count only)
	TS     string `json:"ts"`
}

// overrideGrants is the on-disk grant set, keyed by rule name.
type overrideGrants struct {
	Rules map[string]overrideGrant `json:"rules"`
}

// softRules are the overridable (deny-by-default-but-reasoned-override) rules. HARD
// rules (secrets/off-limits gate patterns) are absent here on purpose — they cannot
// be overridden. Advisory rules are context-only and never reach the gate.
var softRules = map[string]bool{
	"dispatcher-mode":     true,
	"confirm-before-push": true,
	"commit-style":        true,
}

func overridesPath(root string) string {
	return filepath.Join(root, ".projx", "overrides.json")
}

func overrideLogPath(root string) string {
	return filepath.Join(root, ".projx", "override-log.jsonl")
}

// loadOverrideGrants reads the grant set (best-effort: missing/corrupt → empty).
func loadOverrideGrants(root string) overrideGrants {
	g := overrideGrants{Rules: map[string]overrideGrant{}}
	if data, err := os.ReadFile(overridesPath(root)); err == nil {
		_ = json.Unmarshal(data, &g)
		if g.Rules == nil {
			g.Rules = map[string]overrideGrant{}
		}
	}
	return g
}

func saveOverrideGrants(root string, g overrideGrants) error {
	path := overridesPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// consumeOverride reports whether an active grant exists for rule and, if so, spends
// one use (dropping the grant when exhausted). It is the hook's gate: a soft deny is
// allowed to proceed exactly when this returns (reason, true). Best-effort — a write
// failure still allows the single action (the audit log already recorded the intent).
func consumeOverride(root, rule string) (reason string, ok bool) {
	g := loadOverrideGrants(root)
	gr, has := g.Rules[rule]
	if !has {
		return "", false
	}
	if gr.Expiry != 0 && nowUnixMilli() > gr.Expiry {
		delete(g.Rules, rule)
		_ = saveOverrideGrants(root, g)
		return "", false
	}
	gr.Uses--
	if gr.Uses <= 0 {
		delete(g.Rules, rule)
	} else {
		g.Rules[rule] = gr
	}
	_ = saveOverrideGrants(root, g)
	return gr.Reason, true
}

// nowUnixMilli is a package var so tests can pin the clock.
var nowUnixMilli = func() int64 { return time.Now().UnixMilli() }

// runOverrideCmd implements `projx-engine override <rule> --reason <why> [--ttl N]`.
func runOverrideCmd(absRoot string, args []string) {
	fs := flag.NewFlagSet("override", flag.ExitOnError)
	reason := fs.String("reason", "", "why you are overriding this rule (required)")
	ttl := fs.Int("ttl", 1, "number of actions this override permits (default 1 = one-shot)")

	// The rule is a positional arg that may appear BEFORE the flags
	// (`override dispatcher-mode --reason ...`). Go's flag package stops at the first
	// non-flag token, so pull the leading positional out before parsing the rest.
	var rule string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		rule, args = args[0], args[1:]
	}
	_ = fs.Parse(args)
	if rule == "" {
		rule = fs.Arg(0)
	}
	rule = strings.TrimSpace(rule)
	if rule == "" {
		die("usage: override <rule> --reason <why> [--ttl N]\n  soft rules: %s", strings.Join(sortedSoftRules(), ", "))
	}

	if !softRules[rule] {
		die("rule %q is not a soft (overridable) rule. Overridable: %s\n"+
			"(the secrets / off-limits floor is HARD and cannot be overridden.)",
			rule, strings.Join(sortedSoftRules(), ", "))
	}
	if strings.TrimSpace(*reason) == "" {
		die("--reason is required: an override must be explicit and logged")
	}
	if *ttl < 1 {
		*ttl = 1
	}

	ts := time.UnixMilli(nowUnixMilli()).UTC().Format(time.RFC3339)

	// (1) write the grant the hook will consume.
	g := loadOverrideGrants(absRoot)
	g.Rules[rule] = overrideGrant{Rule: rule, Reason: *reason, Uses: *ttl, TS: ts}
	if err := saveOverrideGrants(absRoot, g); err != nil {
		die("write override grant: %v", err)
	}

	// (2) append the audit entry — the deviation is now on the record.
	appendOverrideLog(absRoot, rule, *reason, ts)

	fmt.Printf("override granted: %q for %d action(s) — reason: %s\n", rule, *ttl, *reason)
	fmt.Println("logged to .projx/override-log.jsonl; it will surface in your next session.")
}

// appendOverrideLog appends one audit line (best-effort).
func appendOverrideLog(root, rule, reason, ts string) {
	session := strings.TrimSpace(os.Getenv("PROJX_SESSION"))
	if session == "" {
		session = "cli"
	}
	entry := map[string]string{"rule": rule, "reason": reason, "session": session, "ts": ts}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := overrideLogPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// recentOverrides returns up to n most-recent audit entries formatted for context
// injection, so the human sees every deviation the next session (best-effort).
func recentOverrides(root string, n int) []string {
	data, err := os.ReadFile(overrideLogPath(root))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		var e map[string]string
		if json.Unmarshal([]byte(lines[i]), &e) != nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s — %s (%s)", e["rule"], e["reason"], e["ts"]))
	}
	return out
}

func sortedSoftRules() []string {
	out := make([]string, 0, len(softRules))
	for r := range softRules {
		out = append(out, r)
	}
	// small fixed set; simple insertion sort keeps output stable without importing sort here
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
