package routing

import "sort"

// Effort is ProjX's provider-neutral reasoning control. Provider adapters map
// these values to native flags (for example medium, high, xhigh, max, or
// adaptive thinking).
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortUltra  Effort = "ultra"
)

// Availability is deliberately provider-specific. A published model that the
// current account cannot invoke is not eligible for automatic routing.
type Availability string

const (
	AvailabilityUsable      Availability = "usable"
	AvailabilityUnavailable Availability = "unavailable"
	AvailabilityUnknown     Availability = "unknown"
	AvailabilityQuarantined Availability = "quarantined"
)

// ModelProfile is the atomic routing unit: provider + exact model + effort.
// It is suitable for durable catalog snapshots and execution manifests.
type ModelProfile struct {
	Provider       string             `json:"provider"`
	Model          string             `json:"model"`
	Family         string             `json:"family,omitempty"`
	Effort         Effort             `json:"effort"`
	NativeEffort   string             `json:"native_effort,omitempty"`
	Availability   Availability       `json:"availability"`
	Source         string             `json:"source,omitempty"`
	CheckedAt      int64              `json:"checked_at,omitempty"`
	FailureReason  string             `json:"failure_reason,omitempty"`
	Quality        float64            `json:"quality,omitempty"`
	Cost           float64            `json:"cost,omitempty"`
	Latency        float64            `json:"latency,omitempty"`
	Reliability    float64            `json:"reliability,omitempty"`
	Capability     float64            `json:"capability_score,omitempty"`
	Efficiency     float64            `json:"efficiency,omitempty"`
	Freshness      float64            `json:"freshness_confidence,omitempty"`
	Capabilities   []string           `json:"capabilities,omitempty"`
	Permission     string             `json:"permission_profile,omitempty"`
	ContextWindow  int64              `json:"context_window,omitempty"`
	InputCost      float64            `json:"input_cost_per_mtok,omitempty"`
	OutputCost     float64            `json:"output_cost_per_mtok,omitempty"`
	Score          float64            `json:"score,omitempty"`
	ScoreBreakdown map[string]float64 `json:"score_breakdown,omitempty"`
}

// ModelCatalog is the last-known-good provider inventory. Refreshes replace
// profiles atomically; a failed refresh must not erase the previous snapshot.
type ModelCatalog struct {
	UpdatedAt int64          `json:"updated_at"`
	Profiles  []ModelProfile `json:"profiles"`
}

// NormalizeEffort maps provider-native effort names to the stable ProjX
// contract. Unknown values are conservatively quarantined by callers.
func NormalizeEffort(native string) (Effort, bool) {
	switch native {
	case "minimal", "none", "low":
		return EffortLow, true
	case "medium":
		return EffortMedium, true
	case "high", "xhigh":
		return EffortHigh, true
	case "max", "ultra", "adaptive-high", "adaptive":
		return EffortUltra, true
	default:
		return "", false
	}
}

// CatalogScore applies the same deterministic objective used by routing.json.
// Higher quality/reliability are better; lower cost/latency are better.
func CatalogScore(p ModelProfile, weights SelectionWeights) float64 {
	return weights.Quality*p.Quality + weights.Reliability*p.Reliability -
		weights.Cost*p.Cost - weights.Latency*p.Latency +
		weights.Capability*p.Capability + weights.Context*normalizedContext(p.ContextWindow) +
		weights.Freshness*p.Freshness + weights.Efficiency*p.Efficiency
}

func normalizedContext(tokens int64) float64 {
	switch {
	case tokens >= 1_000_000:
		return 1
	case tokens >= 400_000:
		return .9
	case tokens >= 200_000:
		return .8
	case tokens >= 100_000:
		return .7
	case tokens > 0:
		return .5
	default:
		return 0
	}
}

func scoreBreakdown(p ModelProfile, weights SelectionWeights) map[string]float64 {
	return map[string]float64{
		"quality":     weights.Quality * p.Quality,
		"reliability": weights.Reliability * p.Reliability,
		"cost":        -weights.Cost * p.Cost,
		"latency":     -weights.Latency * p.Latency,
		"capability":  weights.Capability * p.Capability,
		"context":     weights.Context * normalizedContext(p.ContextWindow),
		"freshness":   weights.Freshness * p.Freshness,
		"efficiency":  weights.Efficiency * p.Efficiency,
	}
}

// RankProfiles returns usable profiles in deterministic descending score order.
// Provider and model are stable tie-breakers, so repeated refreshes cannot
// randomly change the selected model.
func RankProfiles(profiles []ModelProfile, weights SelectionWeights) []ModelProfile {
	out := make([]ModelProfile, 0, len(profiles))
	for _, p := range profiles {
		if p.Availability != AvailabilityUsable {
			continue
		}
		p.Score = CatalogScore(p, weights)
		p.ScoreBreakdown = scoreBreakdown(p, weights)
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Effort < out[j].Effort
	})
	return out
}

// SelectCatalogProfile chooses one usable profile for a provider policy. Exact
// model/native-effort pins win. If a pin is unavailable, the selector falls
// back only to profiles for the same provider and requested neutral effort;
// callers must opt into cross-provider fallback separately. This keeps every
// automatic choice inspectable and deterministic.
func SelectCatalogProfile(profiles []ModelProfile, requested Provider, weights SelectionWeights) (ModelProfile, bool) {
	provider := requested.Provider
	model := requested.Model
	native := requested.NativeEffort
	effort := Effort(requested.Effort)
	if effort == "" && native != "" {
		effort, _ = NormalizeEffort(native)
	}
	filter := func(requireModel, requireNative bool) []ModelProfile {
		out := make([]ModelProfile, 0, len(profiles))
		for _, profile := range profiles {
			if profile.Availability != AvailabilityUsable {
				continue
			}
			if provider != "" && profile.Provider != provider {
				continue
			}
			if requireModel && model != "" && profile.Model != model {
				continue
			}
			if requireNative && native != "" && profile.NativeEffort != native {
				continue
			}
			if effort != "" && profile.Effort != effort {
				continue
			}
			out = append(out, profile)
		}
		return RankProfiles(out, weights)
	}
	for _, candidates := range [][]ModelProfile{
		filter(true, true),   // exact provider/model/native effort
		filter(true, false),  // same model, neutral effort band
		filter(false, false), // same provider, neutral effort band
	} {
		if len(candidates) > 0 {
			return candidates[0], true
		}
	}
	if requested.AllowCrossProviderFallback {
		fallback := requested
		fallback.Provider, fallback.Model, fallback.NativeEffort = "", "", ""
		fallback.AllowCrossProviderFallback = false
		return SelectCatalogProfile(profiles, fallback, weights)
	}
	return ModelProfile{}, false
}

// CatalogSelectionReason describes the deterministic selection path taken for
// a requested policy and an eligible catalog profile. It is derived from the
// two persisted values so a completed run remains explainable after refresh.
func CatalogSelectionReason(requested Provider, selected ModelProfile) string {
	if requested.Provider != "" && requested.Provider != selected.Provider {
		return "cross-provider fallback"
	}
	if requested.Model != "" && requested.Model != selected.Model {
		return "same-provider model fallback"
	}
	if requested.NativeEffort != "" && requested.NativeEffort != selected.NativeEffort {
		return "same-model effort fallback"
	}
	return "exact catalog profile"
}

// MergeProviderProfiles applies a successful provider refresh without losing
// locally curated quality/cost/reliability/capability metadata. Profiles no
// longer reported by that provider remain visible as unavailable evidence;
// profiles for every other provider are untouched.
func MergeProviderProfiles(previous, fresh []ModelProfile, provider string, checkedAt int64) []ModelProfile {
	prior := make(map[string]ModelProfile, len(previous))
	key := func(p ModelProfile) string { return p.Provider + "\x00" + p.Model + "\x00" + p.NativeEffort }
	for _, p := range previous {
		prior[key(p)] = p
	}
	seen := map[string]bool{}
	out := make([]ModelProfile, 0, len(previous)+len(fresh))
	for _, p := range fresh {
		if old, ok := prior[key(p)]; ok {
			p.Quality = preferCurated(old.Quality, p.Quality)
			p.Cost = preferCurated(old.Cost, p.Cost)
			p.Latency = preferCurated(old.Latency, p.Latency)
			p.Reliability = preferCurated(old.Reliability, p.Reliability)
			p.Capability = preferCurated(old.Capability, p.Capability)
			p.Efficiency = preferCurated(old.Efficiency, p.Efficiency)
			p.Capabilities, p.Permission = append([]string(nil), old.Capabilities...), old.Permission
			p.ContextWindow, p.InputCost, p.OutputCost = old.ContextWindow, old.InputCost, old.OutputCost
		}
		p.Availability, p.FailureReason, p.CheckedAt, p.Freshness = AvailabilityUsable, "", checkedAt, 1
		out = append(out, p)
		seen[key(p)] = true
	}
	for _, old := range previous {
		if old.Provider != provider {
			out = append(out, old)
			continue
		}
		if seen[key(old)] {
			continue
		}
		old.Availability, old.FailureReason, old.CheckedAt = AvailabilityUnavailable, "not reported by latest provider refresh", checkedAt
		out = append(out, old)
	}
	return out
}

func preferCurated(curated, discovered float64) float64 {
	if curated != 0 {
		return curated
	}
	return discovered
}
