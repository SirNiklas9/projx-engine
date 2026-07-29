package store

import "testing"

func TestGateDenied(t *testing.T) {
	m := NewMem()
	for _, pat := range []string{"secret/**", "**/*.key", ".env*", "**/.ssh/**"} {
		if err := m.Put(Record{ID: "gate-rule/" + pat, Kind: KGateRule, Scope: ScopeProject, Key: "g", Body: pat}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		path       string
		wantDenied bool
	}{
		{"secret/key.txt", true},
		{"./secret/deep/nested.txt", true}, // ** crosses segments + ./ normalized
		{"internal/tls/server.key", true},  // **/*.key
		{".env.local", true},               // .env*
		{"config/.ssh/id_rsa", true},       // **/.ssh/**
		{"internal/auth/login.go", false},
		{"secrets.md", false}, // NOT under secret/
	}
	for _, c := range cases {
		pat, denied := GateDenied(m, c.path)
		if denied != c.wantDenied {
			t.Errorf("GateDenied(%q) = %v (pat %q), want %v", c.path, denied, pat, c.wantDenied)
		}
	}
}
