// Package routing implements the deterministic task-triage front door for
// projx-engine run.  No LLM calls; no exec.  Pure policy + keyword matching.
//
// Decision flow:
//  1. DETERMINISTIC-FIRST: if the task clearly maps to an engine op (keyword
//     match), return Kind="deterministic" with an Op.  The caller executes the
//     appropriate handler directly â€” no agent is launched.
//  2. AGENT: classify the capability-class by keyword (deep-reasoning,
//     cheap-fast, default) and resolve the ProviderCmd from the config.
//
// The routing POLICY (which classes exist, what keywords trigger them) is
// encoded here.  Vendor model names live in the per-project routing.json.
package routing

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// Decision is the result of a routing decision.
//
//   - Kind         "deterministic" or "agent"
//   - Op           set when Kind=="deterministic" (e.g. "verify", "store log", "store list")
//   - Class        set when Kind=="agent" (e.g. "deep-reasoning", "cheap-fast", "default")
//   - ProviderCmd  the resolved agent command string (empty â†’ use PROJX_AGENT_CMD / claude)
//   - Reason       a short human-readable explanation
type Decision struct {
	Kind              string
	Op                string
	Class             string
	ProviderCmd       string
	Provider          string
	Profile           string
	Model             string
	Effort            string
	NativeEffort      string
	Power             string
	Availability      string
	CatalogSource     string
	CatalogUpdatedAt  int64
	Weight            float64
	Quality           float64
	Cost              float64
	Latency           float64
	Reliability       float64
	Capabilities      []string
	PermissionProfile string
	Selection         string
	Reason            string
	// Source is how the class was chosen (override | pin | keyword | triage |
	// triage-escalated | default, +floor) â€” set for Kind=="agent" via the decider.
	Source string
}

// Provider maps a capability-class name to either a legacy concrete agent
// command string or a provider/profile/model tuple that the launcher resolves
// through agent templates.
//
// Cmd is preserved for backward compatibility. When Cmd is non-empty it wins.
// Otherwise Provider/Profile/Model describe the harness-agnostic adapter config.
type Provider struct {
	Class                      string   `json:"class"`
	Cmd                        string   `json:"cmd,omitempty"`
	Provider                   string   `json:"provider,omitempty"`
	Profile                    string   `json:"profile,omitempty"`
	Model                      string   `json:"model,omitempty"`
	Effort                     string   `json:"effort,omitempty"`
	NativeEffort               string   `json:"native_effort,omitempty"`
	Power                      string   `json:"power,omitempty"`
	Availability               string   `json:"availability,omitempty"` // available | unavailable | unknown
	CatalogSource              string   `json:"catalog_source,omitempty"`
	CatalogUpdatedAt           int64    `json:"catalog_updated_at,omitempty"`
	ReasoningEffort            []string `json:"reasoning_effort,omitempty"`
	ContextWindow              int64    `json:"context_window,omitempty"`
	InputCostPerMTok           float64  `json:"input_cost_per_mtok,omitempty"`
	OutputCostPerMTok          float64  `json:"output_cost_per_mtok,omitempty"`
	Weight                     float64  `json:"weight,omitempty"`
	Quality                    float64  `json:"quality,omitempty"`
	Cost                       float64  `json:"cost,omitempty"`
	Latency                    float64  `json:"latency,omitempty"`
	Reliability                float64  `json:"reliability,omitempty"`
	Capabilities               []string `json:"capabilities,omitempty"`
	PermissionProfile          string   `json:"permission_profile,omitempty"`
	Selection                  string   `json:"selection,omitempty"`
	AllowCrossProviderFallback bool     `json:"allow_cross_provider_fallback,omitempty"`
}

// SelectionWeights defines the explicit objective for choosing among provider
// candidates in one capability class. Higher quality/reliability are better;
// lower cost/latency are better. Weight is each candidate's direct preference.
type SelectionWeights struct {
	Quality     float64 `json:"quality,omitempty"`
	Cost        float64 `json:"cost,omitempty"`
	Latency     float64 `json:"latency,omitempty"`
	Reliability float64 `json:"reliability,omitempty"`
	Capability  float64 `json:"capability,omitempty"`
	Context     float64 `json:"context,omitempty"`
	Freshness   float64 `json:"freshness,omitempty"`
	Efficiency  float64 `json:"efficiency,omitempty"`
}

// Config holds the routing configuration.  Constructed by DefaultConfig and
// optionally merged with a project-local routing.json by LoadConfig.
type Config struct {
	Providers []Provider                  `json:"providers"`
	Weights   map[string]SelectionWeights `json:"weights,omitempty"`
	Catalog   ModelCatalog                `json:"-"`
}

const (
	settingRouteProvider      = "setting/route-provider"
	settingRouteProfile       = "setting/route-profile"
	settingRouteModel         = "setting/route-model"
	settingRouteEffort        = "setting/route-effort"
	settingRouteNativeEffort  = "setting/route-native-effort"
	settingRouteFallback      = "setting/route-fallback"
	settingRouteCrossFallback = "setting/route-cross-provider-fallback"
	settingProviderEnabled    = "setting/provider-enabled"
)

// DefaultConfig returns the built-in provider table.
// All classes default to the ambient agent template (PROJX_AGENT / builtin
// claude) unless overridden in routing.json.
// Users override individual classes in .projx/routing.json.
func DefaultConfig() Config {
	return Config{
		Providers: []Provider{
			{Class: "default"},
			{Class: "cheap-fast"},
			{Class: "deep-reasoning"},
			{Class: "local"},
		},
		Weights: map[string]SelectionWeights{
			"cheap-fast":     {Quality: .7, Cost: 1, Latency: 1, Reliability: .6, Capability: .35, Context: .15, Freshness: .4, Efficiency: 1},
			"default":        {Quality: 1, Cost: .55, Latency: .45, Reliability: .8, Capability: .75, Context: .35, Freshness: .4, Efficiency: .65},
			"deep-reasoning": {Quality: 1.35, Cost: .15, Latency: .1, Reliability: .9, Capability: 1, Context: .55, Freshness: .4, Efficiency: .2},
			"local":          {Quality: .8, Cost: .8, Latency: .7, Reliability: .8, Capability: .6, Context: .3, Freshness: .4, Efficiency: .8},
		},
	}
}

// LoadConfig merges DefaultConfig with <root>/.projx/routing.json if present.
// Any parse error or missing file is silently ignored (returns defaults).
// Only providers listed in the JSON file are merged; unlisted classes keep
// their default provider settings.
func LoadConfig(root string) Config {
	cfg := DefaultConfig()
	if catalog, err := LoadCatalog(root); err == nil {
		cfg.Catalog = catalog
	}

	data, err := os.ReadFile(filepath.Join(root, ".projx", "routing.json"))
	if err != nil {
		return cfg // file absent or unreadable — use defaults
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // tolerate UTF-8 BOM from Windows editors/PowerShell

	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return cfg // parse error â€” use defaults
	}

	// Merge: for each provider in the file, update the matching class in cfg.
	seenFileClasses := map[string]bool{}
	for _, fp := range fileCfg.Providers {
		classKey := strings.ToLower(strings.TrimSpace(fp.Class))
		indices := make([]int, 0, 2)
		for i, dp := range cfg.Providers {
			if strings.EqualFold(dp.Class, fp.Class) {
				indices = append(indices, i)
			}
		}
		if len(indices) == 1 && !seenFileClasses[classKey] {
			i := indices[0]
			cfg.Providers[i].Cmd = fp.Cmd
			cfg.Providers[i].Provider = fp.Provider
			cfg.Providers[i].Profile = fp.Profile
			cfg.Providers[i].Model = fp.Model
			cfg.Providers[i].Effort = fp.Effort
			cfg.Providers[i].NativeEffort = fp.NativeEffort
			cfg.Providers[i].Power = fp.Power
			cfg.Providers[i].Availability = fp.Availability
			cfg.Providers[i].CatalogSource = fp.CatalogSource
			cfg.Providers[i].CatalogUpdatedAt = fp.CatalogUpdatedAt
			cfg.Providers[i].ReasoningEffort = append([]string(nil), fp.ReasoningEffort...)
			cfg.Providers[i].ContextWindow = fp.ContextWindow
			cfg.Providers[i].InputCostPerMTok = fp.InputCostPerMTok
			cfg.Providers[i].OutputCostPerMTok = fp.OutputCostPerMTok
			cfg.Providers[i].Weight = fp.Weight
			cfg.Providers[i].Quality = fp.Quality
			cfg.Providers[i].Cost = fp.Cost
			cfg.Providers[i].Latency = fp.Latency
			cfg.Providers[i].Reliability = fp.Reliability
			cfg.Providers[i].Capabilities = append([]string(nil), fp.Capabilities...)
			cfg.Providers[i].PermissionProfile = fp.PermissionProfile
			cfg.Providers[i].AllowCrossProviderFallback = fp.AllowCrossProviderFallback
			seenFileClasses[classKey] = true
		} else {
			cfg.Providers = append(cfg.Providers, fp)
			seenFileClasses[classKey] = true
		}
	}
	for class, weights := range fileCfg.Weights {
		if cfg.Weights == nil {
			cfg.Weights = map[string]SelectionWeights{}
		}
		cfg.Weights[class] = mergeSelectionWeights(cfg.Weights[class], weights)
	}
	if catalog, err := LoadCatalog(root); err == nil {
		cfg.Catalog = catalog
	}
	return cfg
}

// resolveProvider looks up the provider config for the given capability-class.
// Returns the zero Provider when the class is not found.
func resolveProvider(class string, cfg Config) Provider {
	var candidates []Provider
	for _, p := range cfg.Providers {
		if strings.EqualFold(p.Class, class) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return Provider{}
	}
	weights := cfg.Weights[class]
	best := candidates[0]
	bestScore := providerScore(best, weights)
	for _, candidate := range candidates[1:] {
		score := providerScore(candidate, weights)
		if score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

func providerScore(p Provider, weights SelectionWeights) float64 {
	return p.Weight + weights.Quality*p.Quality - weights.Cost*p.Cost -
		weights.Latency*p.Latency + weights.Reliability*p.Reliability
}

func mergeSelectionWeights(base, overlay SelectionWeights) SelectionWeights {
	if overlay.Quality != 0 {
		base.Quality = overlay.Quality
	}
	if overlay.Cost != 0 {
		base.Cost = overlay.Cost
	}
	if overlay.Latency != 0 {
		base.Latency = overlay.Latency
	}
	if overlay.Reliability != 0 {
		base.Reliability = overlay.Reliability
	}
	if overlay.Capability != 0 {
		base.Capability = overlay.Capability
	}
	if overlay.Context != 0 {
		base.Context = overlay.Context
	}
	if overlay.Freshness != 0 {
		base.Freshness = overlay.Freshness
	}
	if overlay.Efficiency != 0 {
		base.Efficiency = overlay.Efficiency
	}
	return base
}

// ProviderForClass exposes the deterministic provider/profile/model lookup to
// execution adapters. Legacy command strings remain part of the returned
// record for compatibility, but structured provider data is preserved.
func ProviderForClass(cfg Config, class string) Provider {
	return resolveProvider(class, cfg)
}

// containsAny returns true if s (lowercased) contains any of the given tokens.
func containsAny(s string, tokens ...string) bool {
	lower := strings.ToLower(s)
	for _, t := range tokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// Decide is the store-free back-compat entry point: deterministic-op triage, then
// the decider with no store (built-in classifier, no pin/floor) and no model triage.
func Decide(task string, cfg Config) Decision {
	return DecideWithStore(nil, task, cfg, nil)
}

// DecideWithStore returns the routing Decision for a task. It first does the
// deterministic-OP triage (verify / store log / store list â€” handled with no agent at
// all), and otherwise hands the capability-tier choice to the store-backed DECIDER
// (store.RouteDecide): per-message @-override > standing pin/floor > keyword classifier
// > cheap model triage > default. Pass triage=nil for deterministic-only routing.
func DecideWithStore(s store.Store, task string, cfg Config, triage store.TriageFunc) Decision {
	// â”€â”€ 0. MUTATION VETO â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	// A task that asks for a CHANGE must never fall into the deterministic-op
	// triage below, whatever else it happens to mention.
	//
	// Why this exists: the arms below are bare substring matches, so a perfectly
	// ordinary edit task â€” "Add the two missing registrations ... VERIFY: run go
	// build" â€” matched on the word "verify" and was silently downgraded to a
	// boundary check. The op then ran the build, reported "verify: behavioral
	// gate PASSED", edited NOTHING, and the dispatch reported `done`. It looked
	// exactly like success. That is the worst possible failure for a dispatcher
	// whose whole contract is "agents mutate, the trunk verifies the diff":
	// nothing mutated, and the report said otherwise.
	//
	// Deterministic ops are read-only by construction, so they can only ever be
	// the right answer for a read-only request. When a task says BOTH "change
	// this" and "verify it", the change is the job and the verify is an
	// acceptance criterion â€” routing to the criterion and skipping the job
	// inverts the request.
	if isMutationTask(task) {
		return decideAgent(s, task, cfg, triage)
	}

	// â”€â”€ 1. DETERMINISTIC-FIRST triage â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	// Each arm maps a set of obvious keywords to an engine op.  Keywords are
	// checked in priority order; first match wins.

	if containsAny(task, "verify", "check boundaries", "check boundary", "violations") {
		return Decision{
			Kind:   "deterministic",
			Op:     "verify",
			Reason: "task clearly requests a boundary/rule check â€” routing to verify op (no agent needed)",
		}
	}

	if containsAny(task, "history", "changelog", "what changed", "show changes", "store log", "show log") {
		return Decision{
			Kind:   "deterministic",
			Op:     "store log",
			Reason: "task requests project history â€” routing to store log op (no agent needed)",
		}
	}

	if containsAny(task,
		"list the store", "what's in the store", "whats in the store",
		"show conventions", "list conventions", "show store", "list store",
	) {
		return Decision{
			Kind:   "deterministic",
			Op:     "store list",
			Reason: "task requests a store listing â€” routing to store list op (no agent needed)",
		}
	}

	// â”€â”€ 2. AGENT path: the DECIDER (precedence ladder) picks the tier â”€â”€â”€â”€â”€â”€â”€â”€â”€
	return decideAgent(s, task, cfg, triage)
}

// decideAgent runs the DECIDER (precedence ladder) and resolves the provider
// command. Split out of DecideWithStore so the mutation veto can reach the agent
// path without duplicating the ladder â€” one definition, so route/run/dispatch
// keep agreeing with each other.
//
// The risk-floor (correctness-critical â†’ deep-reasoning) is applied inside
// store.RouteDecide, so route/run/dispatch all get it consistently.
func decideAgent(s store.Store, task string, cfg Config, triage store.TriageFunc) Decision {
	rd := store.RouteDecide(s, task, triage)
	p := resolveProviderPolicy(s, rd.Class, cfg)
	cmd := rd.Cmd // store KRoute tier-map wins if setâ€¦
	if cmd == "" {
		cmd = p.Cmd // ?else the routing.json provider.
	}
	if providerDisabled(globallyDisabledProviders(s), commandProvider(cmd)) {
		cmd = ""
	}
	return Decision{
		Kind:              "agent",
		Class:             rd.Class,
		ProviderCmd:       cmd,
		Provider:          p.Provider,
		Profile:           p.Profile,
		Model:             p.Model,
		Effort:            p.Effort,
		NativeEffort:      p.NativeEffort,
		Power:             p.Power,
		Availability:      p.Availability,
		CatalogSource:     p.CatalogSource,
		CatalogUpdatedAt:  p.CatalogUpdatedAt,
		Weight:            p.Weight,
		Quality:           p.Quality,
		Cost:              p.Cost,
		Latency:           p.Latency,
		Reliability:       p.Reliability,
		Capabilities:      append([]string(nil), p.Capabilities...),
		PermissionProfile: p.PermissionProfile,
		Selection:         p.Selection,
		Reason:            rd.Reason,
		Source:            rd.Source,
	}
}

func commandProvider(cmd string) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return ""
	}
	name := strings.Trim(fields[0], `"'`)
	name = strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	switch name {
	case "claude", "codex":
		return name
	default:
		return ""
	}
}

func resolveProviderPolicy(s store.Store, class string, cfg Config) Provider {
	disabled := globallyDisabledProviders(s)
	filtered := cfg
	filtered.Providers = filterEnabledProviders(cfg.Providers, disabled)
	filtered.Catalog.Profiles = filterEnabledProfiles(cfg.Catalog.Profiles, disabled)
	p := resolveProvider(class, filtered)
	if body := storeRouteSetting(s, settingRouteProvider+"/"+class); body != "" {
		p.Provider = body
	}
	if body := storeRouteSetting(s, settingRouteProfile+"/"+class); body != "" {
		p.Profile = body
	}
	if body := storeRouteSetting(s, settingRouteModel+"/"+class); body != "" {
		p.Model = body
	}
	if body := storeRouteSetting(s, settingRouteEffort+"/"+class); body != "" {
		p.Effort = body
	}
	if body := storeRouteSetting(s, settingRouteNativeEffort+"/"+class); body != "" {
		p.NativeEffort = body
	}
	if body := storeRouteSetting(s, settingRouteFallback+"/"+class); body != "" {
		p.Cmd = body
	}
	if body := storeRouteSetting(s, settingRouteCrossFallback+"/"+class); body != "" {
		p.AllowCrossProviderFallback = strings.EqualFold(body, "true") || body == "1" || strings.EqualFold(body, "yes")
	}
	if p.Effort == "" && p.NativeEffort == "" {
		p.Effort = defaultEffortForClass(class)
	}
	requested := p
	if providerDisabled(disabled, requested.Provider) {
		requested.Provider, requested.Profile, requested.Model, requested.NativeEffort = "", "", "", ""
		requested.Cmd = ""
		p = resolveProvider(class, filtered)
	}
	if selected, ok := SelectCatalogProfile(filtered.Catalog.Profiles, requested, cfg.Weights[class]); ok {
		p.Selection = CatalogSelectionReason(requested, selected)
		p.Provider = selected.Provider
		p.Model = selected.Model
		p.Effort = string(selected.Effort)
		p.NativeEffort = selected.NativeEffort
		p.Availability = string(selected.Availability)
		p.CatalogSource = selected.Source
		p.CatalogUpdatedAt = selected.CheckedAt
	}
	return p
}

func defaultEffortForClass(class string) string {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "cheap-fast":
		return string(EffortLow)
	case "default":
		return string(EffortMedium)
	case "deep-reasoning":
		return string(EffortHigh)
	case "elevate":
		return string(EffortUltra)
	default:
		return ""
	}
}

func globallyDisabledProviders(s store.Store) map[string]bool {
	disabled := map[string]bool{}
	if s == nil {
		return disabled
	}
	for _, r := range s.List(store.OfKind(store.KRoute)) {
		if r.Scope != store.ScopeGlobal || !strings.HasPrefix(r.Key, settingProviderEnabled+"/") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(r.Key, settingProviderEnabled+"/")))
		value := strings.ToLower(strings.TrimSpace(r.Body))
		if name != "" {
			disabled[name] = value == "false" || value == "0" || value == "no" || value == "disabled"
		}
	}
	return disabled
}

// ProviderEnabled reports the global hard-gate state for one provider. A
// project/workspace policy and a per-run override cannot re-enable a provider
// that the global scope disabled.
func ProviderEnabled(s store.Store, name string) bool {
	return !providerDisabled(globallyDisabledProviders(s), name)
}

func providerDisabled(disabled map[string]bool, name string) bool {
	return disabled[strings.ToLower(strings.TrimSpace(name))]
}

func filterEnabledProviders(in []Provider, disabled map[string]bool) []Provider {
	out := make([]Provider, 0, len(in))
	for _, p := range in {
		if !providerDisabled(disabled, p.Provider) {
			out = append(out, p)
		}
	}
	return out
}

func filterEnabledProfiles(in []ModelProfile, disabled map[string]bool) []ModelProfile {
	out := make([]ModelProfile, 0, len(in))
	for _, p := range in {
		if !providerDisabled(disabled, p.Provider) {
			out = append(out, p)
		}
	}
	return out
}

func storeRouteSetting(s store.Store, key string) string {
	if s == nil {
		return ""
	}
	for _, r := range s.List(store.OfKind(store.KRoute)) {
		if r.Key == key {
			return strings.TrimSpace(r.Body)
		}
	}
	return ""
}

// mutationVerbs are the openers of a task that asks for a CHANGE. Matched as
// whole words against the task's LEADING clause, not anywhere in the body: a
// read-only request like "verify nothing added a new export" mentions "add" but
// asks for nothing to change, while "Add the missing registration" leads with it.
//
// Deliberately conservative. A false positive costs an agent run on something an
// op could have answered â€” cheap, visible, correctable. A false negative silently
// turns a code change into a no-op that reports success, which is what happened
// on 2026-07-16 and cost real debugging time on payment code. Prefer the cheap
// failure.
var mutationVerbs = []string{
	"add", "insert", "append", "create", "write", "implement",
	"fix", "change", "edit", "update", "modify", "patch",
	"remove", "delete", "drop", "rename", "move",
	"refactor", "rewrite", "replace", "wire", "register",
	"bug fix", "bugfix",
}

// isMutationTask reports whether the task's opening asks for a change.
//
// Only the leading clause is considered â€” up to the first sentence break â€” so an
// acceptance criterion further down ("... then verify with go build") cannot flip
// the decision, and a genuinely read-only task that merely mentions a verb in
// passing is not dragged onto the agent path.
func isMutationTask(task string) bool {
	lead := strings.ToLower(strings.TrimSpace(task))
	// The lead clause: whichever sentence break comes first.
	for _, brk := range []string{".", ":", ";", "\n", " â€” ", " - "} {
		if i := strings.Index(lead, brk); i > 0 && i < len(lead) {
			lead = lead[:i]
		}
	}
	for _, v := range mutationVerbs {
		if hasWord(lead, v) {
			return true
		}
	}
	return false
}

// hasWord reports whether s contains tok as a WHOLE word. Substring matching is
// what created the bug this guard exists for ("verify" inside a sentence), so
// the guard itself must not repeat it â€” "readd" must not match "add".
func hasWord(s, tok string) bool {
	i := 0
	for {
		j := strings.Index(s[i:], tok)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(tok)
		beforeOK := start == 0 || !isWordByte(s[start-1])
		afterOK := end == len(s) || !isWordByte(s[end])
		if beforeOK && afterOK {
			return true
		}
		i = start + 1
		if i >= len(s) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
