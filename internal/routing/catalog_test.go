package routing_test

import (
	"path/filepath"
	"testing"

	"github.com/SirNiklas9/projx-engine/internal/routing"
)

func TestCatalogRoundTripUsesLastKnownGoodSnapshot(t *testing.T) {
	root := t.TempDir()
	want := routing.ModelCatalog{UpdatedAt: 42, Profiles: []routing.ModelProfile{{
		Provider: "codex", Model: "gpt-5.6-luna", Effort: routing.EffortMedium,
		Availability: routing.AvailabilityUsable, Score: 9.5,
	}}}
	if err := routing.SaveCatalog(root, want); err != nil {
		t.Fatal(err)
	}
	got, err := routing.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 1 || got.Profiles[0].Model != "gpt-5.6-luna" || got.Profiles[0].Effort != routing.EffortMedium {
		t.Fatalf("catalog round trip = %+v", got)
	}
	if filepath.Dir(routing.CatalogPath(root)) == root {
		t.Fatal("catalog must be stored under .projx")
	}
}

func TestRankProfilesUsesGranularDeterministicWeights(t *testing.T) {
	profiles := []routing.ModelProfile{
		{Provider: "codex", Model: "fast", Effort: routing.EffortMedium, Availability: routing.AvailabilityUsable, Quality: .7, Cost: .1, Latency: .1, Reliability: .9, Capability: .7, Efficiency: 1, Freshness: 1},
		{Provider: "codex", Model: "deep", Effort: routing.EffortMedium, Availability: routing.AvailabilityUsable, Quality: 1, Cost: .9, Latency: .9, Reliability: .9, Capability: 1, Efficiency: .4, Freshness: 1},
	}
	fast := routing.RankProfiles(profiles, routing.SelectionWeights{Cost: 1, Latency: 1, Efficiency: 1})
	if fast[0].Model != "fast" || len(fast[0].ScoreBreakdown) != 8 {
		t.Fatalf("efficiency ranking = %+v", fast)
	}
	deep := routing.RankProfiles(profiles, routing.SelectionWeights{Quality: 1, Capability: 1})
	if deep[0].Model != "deep" {
		t.Fatalf("quality ranking = %+v", deep)
	}
}

func TestNormalizeEffort(t *testing.T) {
	tests := map[string]routing.Effort{
		"minimal":  routing.EffortLow,
		"medium":   routing.EffortMedium,
		"xhigh":    routing.EffortHigh,
		"max":      routing.EffortUltra,
		"adaptive": routing.EffortUltra,
	}
	for native, want := range tests {
		got, ok := routing.NormalizeEffort(native)
		if !ok || got != want {
			t.Errorf("NormalizeEffort(%q) = %q, %v; want %q, true", native, got, ok, want)
		}
	}
	if _, ok := routing.NormalizeEffort("future-effort"); ok {
		t.Fatal("unknown effort must not be silently normalized")
	}
}

func TestRankProfilesFiltersUnavailableAndBreaksTiesDeterministically(t *testing.T) {
	profiles := []routing.ModelProfile{
		{Provider: "claude", Model: "sonnet", Effort: routing.EffortMedium, Availability: routing.AvailabilityUsable, Quality: 1, Reliability: 1},
		{Provider: "codex", Model: "gpt", Effort: routing.EffortMedium, Availability: routing.AvailabilityUsable, Quality: 1, Reliability: 1},
		{Provider: "codex", Model: "unavailable", Effort: routing.EffortHigh, Availability: routing.AvailabilityUnavailable, Quality: 100},
	}
	got := routing.RankProfiles(profiles, routing.SelectionWeights{Quality: 1, Reliability: 1})
	if len(got) != 2 {
		t.Fatalf("ranked %d profiles, want 2", len(got))
	}
	if got[0].Provider != "claude" || got[1].Provider != "codex" {
		t.Fatalf("tie order = %s, %s; want claude, codex", got[0].Provider, got[1].Provider)
	}
}

func TestSelectCatalogProfileUsesExactPinThenSameProviderFallback(t *testing.T) {
	profiles := []routing.ModelProfile{
		{Provider: "codex", Model: "gpt-5.6-sol", Effort: routing.EffortUltra, NativeEffort: "ultra", Availability: routing.AvailabilityUnavailable},
		{Provider: "codex", Model: "gpt-5.6-terra", Effort: routing.EffortUltra, NativeEffort: "ultra", Availability: routing.AvailabilityUsable, Quality: 0.8},
		{Provider: "claude", Model: "opus", Effort: routing.EffortUltra, NativeEffort: "max", Availability: routing.AvailabilityUsable, Quality: 1},
	}
	got, ok := routing.SelectCatalogProfile(profiles, routing.Provider{Provider: "codex", Model: "gpt-5.6-sol", Effort: "ultra", NativeEffort: "ultra"}, routing.SelectionWeights{Quality: 1})
	if !ok {
		t.Fatal("expected usable same-provider fallback")
	}
	if got.Provider != "codex" || got.Model != "gpt-5.6-terra" || got.NativeEffort != "ultra" {
		t.Fatalf("selected %+v, want Codex Terra ultra", got)
	}
}

func TestSelectCatalogProfileCrossProviderFallbackIsExplicit(t *testing.T) {
	profiles := []routing.ModelProfile{{Provider: "claude", Model: "opus", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable}}
	request := routing.Provider{Provider: "codex", Model: "missing", Effort: "high"}
	if _, ok := routing.SelectCatalogProfile(profiles, request, routing.SelectionWeights{}); ok {
		t.Fatal("cross-provider fallback occurred without policy opt-in")
	}
	request.AllowCrossProviderFallback = true
	got, ok := routing.SelectCatalogProfile(profiles, request, routing.SelectionWeights{})
	if !ok || got.Provider != "claude" {
		t.Fatalf("explicit cross-provider fallback = %+v, %v", got, ok)
	}
}

func TestCatalogSelectionReasonIsInspectable(t *testing.T) {
	requested := routing.Provider{Provider: "codex", Model: "gpt-5.6-sol", NativeEffort: "ultra"}
	if got := routing.CatalogSelectionReason(requested, routing.ModelProfile{Provider: "codex", Model: "gpt-5.6-sol", NativeEffort: "ultra"}); got != "exact catalog profile" {
		t.Fatalf("exact selection = %q", got)
	}
	if got := routing.CatalogSelectionReason(requested, routing.ModelProfile{Provider: "codex", Model: "gpt-5.6-terra", NativeEffort: "ultra"}); got != "same-provider model fallback" {
		t.Fatalf("model fallback = %q", got)
	}
	if got := routing.CatalogSelectionReason(requested, routing.ModelProfile{Provider: "claude", Model: "claude-sonnet", NativeEffort: "high"}); got != "cross-provider fallback" {
		t.Fatalf("provider fallback = %q", got)
	}
}

func TestMergeProviderProfilesPreservesCuratedMetadataAndMarksMissing(t *testing.T) {
	previous := []routing.ModelProfile{
		{Provider: "codex", Model: "gpt-a", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable, Quality: .9, Cost: .2, Capabilities: []string{"tools"}},
		{Provider: "codex", Model: "retired", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable},
		{Provider: "claude", Model: "sonnet", Effort: routing.EffortHigh, NativeEffort: "high", Availability: routing.AvailabilityUsable},
	}
	fresh := []routing.ModelProfile{{Provider: "codex", Model: "gpt-a", Effort: routing.EffortHigh, NativeEffort: "high", Source: "discovery"}}
	got := routing.MergeProviderProfiles(previous, fresh, "codex", 42)
	if len(got) != 3 || got[0].Quality != .9 || got[0].Cost != .2 || len(got[0].Capabilities) != 1 || got[0].CheckedAt != 42 {
		t.Fatalf("curated metadata was not preserved: %+v", got)
	}
	if got[1].Availability != routing.AvailabilityUnavailable || got[2].Provider != "claude" || got[2].Availability != routing.AvailabilityUsable {
		t.Fatalf("missing/other-provider handling incorrect: %+v", got)
	}
}
