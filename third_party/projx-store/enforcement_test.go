package store

import "testing"

// TestEnforcementPersists round-trips the enforcement column through SQLite,
// proving migration #4 added it and Put/Get/List carry it.
func TestEnforcementPersists(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Put(Record{ID: "gate-rule/x", Kind: KGateRule, Scope: ScopeProject,
		Key: "setting/dispatcher-mode", Body: "on", Enforcement: EnforcementSoft}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("gate-rule/x")
	if !ok || got.Enforcement != EnforcementSoft {
		t.Fatalf("Get enforcement = %q ok=%v, want soft", got.Enforcement, ok)
	}
	if list := s.List(OfKind(KGateRule)); len(list) != 1 || list[0].Enforcement != EnforcementSoft {
		t.Fatalf("List did not carry enforcement: %+v", list)
	}
}

// TestTierDerivation covers Tier(): explicit value wins; empty derives by identity.
func TestTierDerivation(t *testing.T) {
	cases := []struct {
		name string
		r    Record
		want string
	}{
		{"explicit soft on gate", Record{Kind: KGateRule, Key: "secret/**", Enforcement: EnforcementSoft}, EnforcementSoft},
		{"off-limits glob → hard", Record{Kind: KGateRule, Key: "dotenv", Body: ".env*"}, EnforcementHard},
		{"dispatcher setting → soft", Record{Kind: KGateRule, Key: "setting/dispatcher-mode"}, EnforcementSoft},
		{"unknown setting → advisory", Record{Kind: KGateRule, Key: "setting/cage-mode"}, EnforcementAdvisory},
		{"convention → advisory", Record{Kind: KConvention, Key: "read before acting"}, EnforcementAdvisory},
	}
	for _, c := range cases {
		if got := Tier(c.r); got != c.want {
			t.Errorf("%s: Tier = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRuleTierAndSoft covers name-based resolution and the soft/hard retier via data.
func TestRuleTierAndSoft(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	SeedFloor(s) // seeds dispatcher-mode (soft); gates are global-scope now, not project

	if !IsSoftRule(s, "dispatcher-mode") {
		t.Error("seeded dispatcher-mode should be soft")
	}
	if RuleTier(s, "unknown-rule") != EnforcementAdvisory {
		t.Error("unknown rule should default to advisory")
	}

	// Retier dispatcher-mode to hard as DATA; RuleTier must reflect it.
	_ = s.Put(Record{ID: "gate-rule/setting-dispatcher-mode", Kind: KGateRule, Scope: ScopeProject,
		Key: "setting/dispatcher-mode", Body: "on", Enforcement: EnforcementHard})
	if IsSoftRule(s, "dispatcher-mode") {
		t.Error("after retier to hard, dispatcher-mode must not be soft")
	}
	if RuleTier(s, "dispatcher-mode") != EnforcementHard {
		t.Error("RuleTier should report the explicit hard tier")
	}
}
