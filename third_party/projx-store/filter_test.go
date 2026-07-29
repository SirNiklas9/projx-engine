package store

import (
	"path/filepath"
	"testing"
)

// filterSeed: real-ish records + canary probes (Up/McQueen) for unmistakable
// retrieval verification — querying "balloons" must return ONLY canary/up.
var filterSeed = []Record{
	{ID: "doc/mc-login-backend", Kind: KDoc, Scope: ScopeProject, Key: "minecraft/login/backend", Body: "JWT auth lives in internal/auth/login.go"},
	{ID: "doc/mc-login-ui", Kind: KDoc, Scope: ScopeProject, Key: "minecraft/login/ui", Body: "the login form component"},
	{ID: "doc/billing-checkout", Kind: KDoc, Scope: ScopeProject, Key: "billing/checkout", Body: "stripe checkout flow"},
	{ID: "canary/up", Kind: KDoc, Scope: ScopeProject, Key: "canary/up", Body: "Up has balloons"},
	{ID: "canary/cars", Kind: KDoc, Scope: ScopeProject, Key: "canary/cars", Body: "Lightning McQueen is a car"},
}

func seedFilterStore(t *testing.T, s Store) {
	t.Helper()
	for _, r := range filterSeed {
		if err := s.Put(r); err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}
}

func recIDs(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func eqIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFilterKeyPrefixAndText proves task-slicing: key-prefix narrowing, text
// search (the canary probe), and Mem↔SQLite parity (List sorts by ID).
func TestFilterKeyPrefixAndText(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"keyprefix minecraft/login", Filter{KeyPrefix: "minecraft/login"}, []string{"doc/mc-login-backend", "doc/mc-login-ui"}},
		{"keyprefix narrowed to /backend", Filter{KeyPrefix: "minecraft/login/backend"}, []string{"doc/mc-login-backend"}},
		{"keyprefix case-insensitive", Filter{KeyPrefix: "Minecraft/Login"}, []string{"doc/mc-login-backend", "doc/mc-login-ui"}},
		{"CANARY: text balloons -> only Up", Filter{Text: "balloons"}, []string{"canary/up"}},
		{"CANARY: text McQueen -> only cars", Filter{Text: "McQueen"}, []string{"canary/cars"}},
		{"text matches body (JWT)", Filter{Text: "JWT"}, []string{"doc/mc-login-backend"}},
		{"keyprefix + text", Filter{KeyPrefix: "minecraft", Text: "form"}, []string{"doc/mc-login-ui"}},
		{"empty filter = all", Filter{}, []string{"canary/cars", "canary/up", "doc/billing-checkout", "doc/mc-login-backend", "doc/mc-login-ui"}},
	}

	mem := NewMem()
	seedFilterStore(t, mem)

	sq, err := Open(filepath.Join(t.TempDir(), "filter.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer sq.Close()
	seedFilterStore(t, sq)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMem := recIDs(mem.List(tc.f))
			if !eqIDs(gotMem, tc.want) {
				t.Errorf("Mem.List = %v, want %v", gotMem, tc.want)
			}
			gotSQ := recIDs(sq.List(tc.f))
			if !eqIDs(gotSQ, gotMem) {
				t.Errorf("PARITY: SQLite.List = %v, Mem.List = %v", gotSQ, gotMem)
			}
		})
	}
}
