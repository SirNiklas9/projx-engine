package store

import "strings"

// Routing — the auto model-tier policy, declared in the store. KRoute records map
// a capability class to the agent launch command (model tier). Classify is the
// deterministic task->class triage (no LLM). Route ties them together. This is the
// SINGLE routing definition shared by the native engine and the engine cell.

// FloorRoutes are the default class -> launch-command tiers seeded into every
// project. The model IDs live HERE, once — not hardcoded in the engine binary, so
// updating a tier is a store edit, not a recompile.
// The tier commands launch an AUTONOMOUS worker: --permission-mode acceptEdits lets it
// apply file edits without an interactive approval (a dispatched worker is headless — no
// one is there to click "allow"). ProjX's own PreToolUse gate (off-limits paths + the
// optional cage) remains the real guardrail. Model IDs live HERE, once (store-editable).
var FloorRoutes = []SeedRec{
	{"cheap-fast", "claude --permission-mode acceptEdits --model claude-haiku-4-5-20251001"}, // mechanical: moves, grep, format
	{"default", "claude --permission-mode acceptEdits --model claude-sonnet-4-6"},            // standard: code, tests, review
	{"deep-reasoning", "claude --permission-mode acceptEdits --model claude-opus-4-8"},       // hard: architecture, debugging
	{"elevate", "claude --permission-mode acceptEdits --model claude-fable-5"},               // top rung — DELIBERATE only (@elevate / pin / floor)
}

// deepKeywords / cheapKeywords are the BUILT-IN floor signals for the deterministic
// classifier. They are the shipped defaults; a project EXTENDS them with its own
// domain vocabulary via store records (see ClassifyStore) — "one definition of
// anything", tunable by `store commit`, not a recompile.
var (
	// NB: "refactor" is deliberately NOT here — a routine refactor is standard coding
	// (default → sonnet), not deep reasoning. Cross-file/architecture-level refactors get
	// caught by "architecture"/"redesign", or a project can add "refactor" back via a
	// store keyword record. "rewrite"/"re-architect" stay deep.
	deepKeywords = []string{"design", "architect", "architecture", "re-architect", "rewrite",
		"why", "plan", "debug", "diagnose", "analyse", "analyze", "redesign", "strategy",
		"tradeoff", "trade-off"}
	cheapKeywords = []string{"rename", "typo", "format", "comment", "small", "trivial",
		"one-liner", "oneliner", "quick fix", "quickfix", "spelling"}
	// stdKeywords positively route standard coding work to the default tier (sonnet) — a
	// CONFIDENT match, so the decider takes it for free and does NOT fall through to
	// triage. A routine refactor/implement lives here; escalate only on the deep signals.
	stdKeywords = []string{"refactor", "implement", "rework", "reimplement"}
)

// FloorKeywordSeeds seed the classifier's built-in vocabulary into the store as
// EDITABLE setting/route-keywords/<class> records ("make it a rule, not a hardcode"):
// once seeded, ClassifyStore reads ONLY the store's copy, so removing or adding a
// word actually changes routing — not just extends it. deepKeywords/cheapKeywords/
// stdKeywords above remain the seed DEFAULTS and the fallback for a store that hasn't
// been (re-)seeded yet (see ClassifyStore).
var FloorKeywordSeeds = []SeedRec{
	{"deep-reasoning", strings.Join(deepKeywords, " ")},
	{"cheap-fast", strings.Join(cheapKeywords, " ")},
	{"default", strings.Join(stdKeywords, " ")},
	{"risk", strings.Join(riskKeywords, " ")}, // correctness-critical → floor = deep-reasoning
}

// ClassifyConfident maps a task to a capability class by keyword and reports whether
// a keyword actually MATCHED (true) or the task fell through to the default class with
// no signal (false). The matched flag is what the decider uses to tell a confident
// keyword route from the ambiguous middle that warrants cheap model triage.
// Deterministic; no LLM. Priority: deep-reasoning > cheap-fast > default.
func ClassifyConfident(task string) (class string, matched bool) {
	return classifyWith(task, deepKeywords, cheapKeywords, stdKeywords)
}

// Classify is the back-compat single-return classifier (the class only).
func Classify(task string) string { c, _ := ClassifyConfident(task); return c }

// ClassifyStore is the store-driven classifier: for EACH class independently, it
// reads that class's vocabulary from the store's `setting/route-keywords/<class>`
// record if one exists — so editing that class's word list with `store commit`
// genuinely changes routing for it, not just extends a hardcoded list underneath.
// A class with NO store record (never seeded, or this class specifically) falls back
// to that class's built-in default — PER CLASS, not all-or-nothing, so a project with
// only ONE class seeded (partial/legacy state) doesn't silently lose the other two.
// A nil store == ClassifyConfident. Honest caveat: this means a class can't be edited
// down to a literal empty vocabulary (an empty/absent record reads as "unseeded" and
// falls back to defaults) — pick different words instead of blanking one out.
// riskKeywords mark correctness-critical work where a cheap model is dangerous — matching
// any of them raises the routing FLOOR to deep-reasoning (see IsRiskyTask). Broad by
// design; editable per-project via setting/route-keywords/risk.
var riskKeywords = []string{
	"concurren", "goroutine", "mutex", "deadlock", "data race", "atomic", "thread-saf", "thread saf",
	"serializ", "deserializ", "marshal", "unmarshal", "jsonconverter", "encoding/json",
	"auth", "authz", "authn", "oauth", "jwt", "token", "crypto", "encrypt", "decrypt", "signature", "password hash",
	"payment", "billing", "invoice", "stripe", "charge card",
	"migration", "schema change", "alter table", "drop table", "drop column",
	"rm -rf", "truncate table", "mass delete", "hard delete",
}

// IsRiskyTask reports whether a task is correctness-critical (concurrency, serialization,
// auth/crypto, money, migrations, destructive ops). The decider raises the tier FLOOR to
// deep-reasoning for these so triage can't send subtle work to a cheap model. The keyword
// set is editable via the store's setting/route-keywords/risk record (else the built-in).
func IsRiskyTask(s Store, task string) bool {
	words := riskKeywords
	if s != nil {
		if kw := storeKeywords(s, "risk"); len(kw) > 0 {
			words = kw
		}
	}
	return containsAny(strings.ToLower(task), words...)
}

func ClassifyStore(s Store, task string) (class string, matched bool) {
	if s == nil {
		return ClassifyConfident(task)
	}
	pick := func(class string, fallback []string) []string {
		if kw := storeKeywords(s, class); len(kw) > 0 {
			return kw
		}
		return fallback
	}
	deep := pick("deep-reasoning", deepKeywords)
	cheap := pick("cheap-fast", cheapKeywords)
	std := pick("default", stdKeywords)
	return classifyWith(task, deep, cheap, std)
}

// classifyWith is the shared keyword match: deep beats cheap beats a CONFIDENT default
// (std keyword) beats the no-signal fall-through. A std match returns ("default", true)
// so the decider takes sonnet for free instead of escalating via triage.
func classifyWith(task string, deep, cheap, std []string) (string, bool) {
	t := strings.ToLower(task)
	if containsAny(t, deep...) {
		return "deep-reasoning", true
	}
	if containsAny(t, cheap...) {
		return "cheap-fast", true
	}
	if containsAny(t, std...) {
		return "default", true
	}
	return "default", false
}

// Route classifies task and resolves the launch command from the store's KRoute
// records. Returns the class and the command (cmd is "" if no record for the class).
func Route(s Store, task string) (class, cmd string) {
	class = Classify(task)
	return class, routeCmd(s, class)
}

// routeCmd resolves a capability class to its launch command from the KRoute tier-map
// records ("" if none). Setting records (key `setting/...`) never match a class name.
func routeCmd(s Store, class string) string {
	if s == nil {
		return ""
	}
	for _, r := range s.List(OfKind(KRoute)) {
		if strings.EqualFold(r.Key, class) {
			return r.Body
		}
	}
	return ""
}

// classRank orders the tiers cheap → standard → deep → elevate, so floor (a minimum)
// and escalate-on-uncertainty (go up one) are simple integer moves. Unknown classes
// rank as "default" (1) so a typo never silently routes to the cheapest tier.
// elevate IS a valid, rankable tier (so `route pin/floor elevate` and @elevate work),
// but it is deliberately OMITTED from tierByRank below so auto escalate-on-uncertainty
// tops out at deep-reasoning (opus) — the priciest model is opt-in, never accidental.
var classRank = map[string]int{"cheap-fast": 0, "default": 1, "deep-reasoning": 2, "elevate": 3}

var tierByRank = []string{"cheap-fast", "default", "deep-reasoning"}

func rankOf(class string) int {
	if r, ok := classRank[class]; ok {
		return r
	}
	return 1
}

// escalate returns the next tier UP (capped at the top) — the escalate-on-uncertainty
// move: when triage is unsure, spend more, never less.
func escalate(class string) string {
	r := rankOf(class) + 1
	if r >= len(tierByRank) {
		r = len(tierByRank) - 1
	}
	return tierByRank[r]
}

// Routing-setting record keys. They live in the store as KRoute records under a
// `setting/` key prefix so they are routing knowledge that travels + is journaled,
// yet are excluded from context injection (dropSettings).
const (
	SettingRoutePin      = "setting/route-pin"      // Body = class: hard-lock every task to this tier
	SettingRouteFloor    = "setting/route-floor"    // Body = class: minimum tier; triage may go above
	settingRouteKeywords = "setting/route-keywords" // /<class> Body = extra trigger words
)

// settingBody returns the trimmed Body of a setting record by its key ("" if absent).
func settingBody(s Store, key string) string {
	if s == nil {
		return ""
	}
	for _, r := range s.List(OfKind(KRoute)) {
		if r.Key == key {
			return strings.TrimSpace(r.Body)
		}
	}
	return ""
}

// validTier reports whether class is one of the known tiers.
func validTier(class string) bool { _, ok := classRank[class]; return ok }

// storeKeywords reads the project's extra trigger words for a class from its
// `setting/route-keywords/<class>` record, split on whitespace/commas, lowercased.
func storeKeywords(s Store, class string) []string {
	body := settingBody(s, settingRouteKeywords+"/"+class)
	if body == "" {
		return nil
	}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(body), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// TriageFunc is the injectable cheap-model triage seam: given an ambiguous task it
// returns a proposed class and whether it is confident. The store library stays
// OS-/network-free — a consumer (the engine) supplies a real haiku-backed func; nil
// means "deterministic only" (ambiguous tasks fall to the default tier).
type TriageFunc func(task string) (class string, confident bool)

// RouteDecision is the resolved routing choice plus how it was reached.
type RouteDecision struct {
	Class  string // cheap-fast | default | deep-reasoning | elevate
	Cmd    string // launch command from the KRoute tier map ("" if none)
	Source string // override | pin | keyword | triage | triage-escalated | default (+floor)
	Reason string // short human explanation
}

// RouteDecide is the DECIDER: it resolves a task to a tier by the locked precedence
// ladder — cheapest possible, escalating only when earned ("lowest model, highest
// yield"):
//
//	1. per-message @-override   (@cheap/@haiku, @sonnet/@default, @opus/@deep) — always wins,
//	   bypasses pin AND floor (the user asked for it explicitly, this once).
//	2. standing PIN setting     — hard-lock to one tier; triage is skipped entirely.
//	3. deterministic classifier — a matched keyword routes for free (store-augmentable).
//	4. cheap haiku triage       — only for the ambiguous middle; escalate-on-uncertainty.
//	5. default tier             — no signal.
//
// Steps 3–5 are then raised to the standing FLOOR (a minimum tier) if one is set.
// Pure given a pure triage func; deterministic when triage is nil.
func RouteDecide(s Store, task string, triage TriageFunc) RouteDecision {
	floor := settingBody(s, SettingRouteFloor)
	// RISK-FLOOR: correctness-critical work (concurrency, serialization, auth/crypto, money,
	// migrations, destructive ops) forces a deep-reasoning MINIMUM so triage/keywords can't
	// send subtle code to a cheap model. It raises the floor; it never lowers a higher one,
	// and (like the configured floor) it doesn't override an explicit @-override or pin.
	riskFloor := false
	if IsRiskyTask(s, task) && rankOf("deep-reasoning") > rankOf(floor) {
		floor, riskFloor = "deep-reasoning", true
	}
	resolve := func(class, source, reason string, applyFloor bool) RouteDecision {
		if applyFloor && validTier(floor) && rankOf(class) < rankOf(floor) {
			class = floor
			if riskFloor {
				source += "+risk-floor"
				reason += " (risk-floor: correctness-critical → " + floor + ")"
			} else {
				source += "+floor"
				reason += " (raised to floor " + floor + ")"
			}
		}
		return RouteDecision{Class: class, Cmd: routeCmd(s, class), Source: source, Reason: reason}
	}

	// 1. Explicit per-message override — wins over everything, no floor.
	if c, ok := taskTierOverride(task); ok {
		return resolve(c, "override", "explicit @-override in the message", false)
	}
	// 2. Standing pin — hard lock, triage disabled, no floor (pin IS the exact tier).
	if pin := settingBody(s, SettingRoutePin); validTier(pin) {
		return resolve(pin, "pin", "pinned tier (standing setting) — triage disabled", false)
	}
	// 3. Deterministic keyword classifier (store-augmented).
	if class, matched := ClassifyStore(s, task); matched {
		return resolve(class, "keyword", "keyword classifier matched", true)
	}
	// 4. Ambiguous middle → cheap triage, escalate when unsure.
	if triage != nil {
		if class, confident := triage(task); validTier(class) {
			if !confident {
				up := escalate(class)
				return resolve(up, "triage-escalated", "triage unsure on "+class+" → escalated", true)
			}
			return resolve(class, "triage", "haiku triage decided the ambiguous task", true)
		}
	}
	// 5. No signal.
	return resolve("default", "default", "no routing signal — default tier", true)
}

// taskTierOverride parses an explicit per-message tier directive. Tier aliases AND the
// concrete model names both work so the user can think in either. @audit is NOT here:
// it triggers a workflow and lets the decider pick the fitting tier (orthogonal).
func taskTierOverride(task string) (class string, ok bool) {
	t := strings.ToLower(task)
	switch {
	case strings.Contains(t, "@cheap"), strings.Contains(t, "@haiku"):
		return "cheap-fast", true
	case strings.Contains(t, "@sonnet"), strings.Contains(t, "@default"), strings.Contains(t, "@standard"):
		return "default", true
	case strings.Contains(t, "@opus"), strings.Contains(t, "@deep"):
		return "deep-reasoning", true
	case strings.Contains(t, "@elevate"), strings.Contains(t, "@fable"):
		return "elevate", true
	}
	return "", false
}

// floorRoute builds a project-scoped KRoute seed record.
func floorRoute(r SeedRec) Record {
	return Record{
		ID:     KRoute.String() + "/" + seedSlug(r.Key),
		Kind:   KRoute,
		Scope:  ScopeProject,
		Key:    r.Key,
		Body:   r.Body,
		Origin: "seed:floor",
	}
}

func containsAny(s string, toks ...string) bool {
	for _, t := range toks {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}
