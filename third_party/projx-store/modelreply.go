package store

// modelreply.go — parsing a cheap model's reply for the decider (tier) and the v2
// context selector (relevant keys). Pure + shared so every face (native CLI, WASM cell)
// interprets a model reply identically — the model transport differs per face, the
// PARSING is one definition.

import (
	"encoding/json"
	"strings"
)

// ParseTierReply extracts a valid capability tier + confidence from a triage model's
// reply. It reads the strict JSON shape {"tier":..,"confident":..} but tolerates
// surrounding prose and falls back to a bare tier word (confident=false so the decider
// escalates rather than trusting a sloppy reply). Returns ("", false) when no valid tier
// is present.
func ParseTierReply(content string) (tier string, confident bool) {
	content = strings.TrimSpace(content)
	if i := strings.IndexByte(content, '{'); i >= 0 {
		if j := strings.LastIndexByte(content, '}'); j > i {
			var obj struct {
				Tier      string `json:"tier"`
				Confident *bool  `json:"confident"`
			}
			if json.Unmarshal([]byte(content[i:j+1]), &obj) == nil && validTier(obj.Tier) {
				return obj.Tier, obj.Confident == nil || *obj.Confident // absent → confident
			}
		}
	}
	low := strings.ToLower(content)
	for _, t := range []string{"deep-reasoning", "cheap-fast", "default"} {
		if strings.Contains(low, t) {
			return t, false
		}
	}
	return "", false
}

// ParseSelectedKeys extracts a JSON array from a selector model's reply and keeps only
// keys present in the candidate set (the model cannot invent or rename a key). Order
// follows the reply; nil when no usable array is present.
func ParseSelectedKeys(reply string, candidates []string) []string {
	valid := make(map[string]bool, len(candidates))
	for _, k := range candidates {
		valid[k] = true
	}
	i := strings.IndexByte(reply, '[')
	j := strings.LastIndexByte(reply, ']')
	if i < 0 || j <= i {
		return nil
	}
	var arr []string
	if json.Unmarshal([]byte(reply[i:j+1]), &arr) != nil {
		return nil
	}
	var out []string
	for _, k := range arr {
		if k = strings.TrimSpace(k); valid[k] {
			out = append(out, k)
		}
	}
	return out
}
