package verify

import (
	"encoding/json"

	store "github.com/SirNiklas9/projx-store"
)

// RulesFromStore loads declared boundary rules from a store: every record of kind
// KDeclaredStructure whose Body is a JSON-encoded Rule. Records that don't parse,
// or that lack a From/To, are skipped — a malformed rule never crashes a check.
//
// This is the seam that makes verify read the DECLARED side (store) against the
// ACTUAL side (core): the rules come from what you wrote down, the edges from what
// the code does.
func RulesFromStore(s store.Store) []Rule {
	var rules []Rule
	for _, rec := range s.List(store.OfKind(store.KDeclaredStructure)) {
		var r Rule
		if err := json.Unmarshal([]byte(rec.Body), &r); err != nil {
			continue
		}
		if r.From == "" || r.To == "" {
			continue
		}
		rules = append(rules, r)
	}
	return rules
}
