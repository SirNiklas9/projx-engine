package store

import "strings"

// enforcement.go — the enforcement TIER of a rule: how strictly it is applied.
// See doc/enforcement-follow-override-plan. A rule's tier is DATA (Record.Enforcement,
// persisted since schema #4); when a record leaves it empty — including every row
// written before the column existed — Tier DERIVES it by identity, so behaviour is
// correct with no data backfill. This is the single source of truth every face reads.

const (
	EnforcementHard     = "hard"     // gate denies; no override (secrets / off-limits floor)
	EnforcementSoft     = "soft"     // denies by default; a recorded reasoned override may proceed
	EnforcementAdvisory = "advisory" // context-injected only; never gated
)

// DefaultRuleTiers is the built-in tier for named POLICY rules when a store carries no
// explicit Enforcement for them. These are the overridable soft rules the override
// command and hook understand. A store record with an explicit Enforcement overrides
// this, so a project can retier a rule (e.g. make dispatcher-mode hard) as data.
var DefaultRuleTiers = map[string]string{
	"dispatcher-mode":     EnforcementSoft,
	"confirm-before-push": EnforcementSoft,
	"commit-style":        EnforcementSoft,
}

// Tier returns a record's enforcement tier: its explicit Enforcement if set, else a
// value derived from its identity — off-limits gate globs are hard, a known soft
// setting is soft, everything else advisory. This keeps pre-schema-#4 rows correct.
func Tier(r Record) string {
	if t := normTier(r.Enforcement); t != "" {
		return t
	}
	if r.Kind == KGateRule {
		if isSettingKey(r.Key) {
			if t, ok := DefaultRuleTiers[settingLeaf(r.Key)]; ok {
				return t
			}
			return EnforcementAdvisory
		}
		return EnforcementHard // a real off-limits deny glob
	}
	return EnforcementAdvisory
}

// RuleTier resolves the tier of a rule referenced by NAME (e.g. "dispatcher-mode").
// An explicit Enforcement on a matching store record wins; otherwise the built-in
// default; otherwise advisory. s may be nil (defaults only).
func RuleTier(s Store, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if s != nil {
		for _, r := range s.List(Filter{}) {
			if t := normTier(r.Enforcement); t != "" && ruleKeyMatches(r.Key, name) {
				return t
			}
		}
	}
	if t, ok := DefaultRuleTiers[name]; ok {
		return t
	}
	return EnforcementAdvisory
}

// IsSoftRule reports whether a named rule is soft (deny-by-default, overridable).
func IsSoftRule(s Store, name string) bool { return RuleTier(s, name) == EnforcementSoft }

// SoftRuleNames lists the names of soft rules: the built-in defaults plus any store
// rule explicitly marked soft. Used for override-command messaging.
func SoftRuleNames(s Store) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for n, t := range DefaultRuleTiers {
		if t == EnforcementSoft {
			add(n)
		}
	}
	if s != nil {
		for _, r := range s.List(Filter{}) {
			if normTier(r.Enforcement) == EnforcementSoft {
				add(settingLeaf(r.Key))
			}
		}
	}
	return sortStrings(out) // stable, small set
}

// normTier normalizes/validates an enforcement value; unknown strings → "" (derive).
func normTier(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case EnforcementHard:
		return EnforcementHard
	case EnforcementSoft:
		return EnforcementSoft
	case EnforcementAdvisory:
		return EnforcementAdvisory
	}
	return ""
}

// isSettingKey reports whether a gate-rule key is a setting (setting/*), not a deny glob.
func isSettingKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.HasPrefix(k, "setting/") || k == "setting"
}

// settingLeaf returns the trailing segment of a key ("setting/dispatcher-mode" →
// "dispatcher-mode"); a key with no slash is returned as-is.
func settingLeaf(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// ruleKeyMatches reports whether a record Key names the rule `name` — as the whole key,
// as "setting/<name>", or as the key's trailing segment.
func ruleKeyMatches(key, name string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == name || key == "setting/"+name || settingLeaf(key) == name
}

// sortStrings returns s sorted ascending (small helper; avoids importing sort here).
func sortStrings(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
	return s
}
