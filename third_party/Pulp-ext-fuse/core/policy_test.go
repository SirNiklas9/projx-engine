package core

import "testing"

// TestPolicyEmptyPrefixCatchAll proves an empty-prefix rule grants the whole
// project while a longer gate prefix still overrides it — the shape the caged
// "allow project except secrets" policy relies on.
func TestPolicyEmptyPrefixCatchAll(t *testing.T) {
	p := Policy{Rules: []Rule{
		{Prefix: "", Access: ReadWrite},  // catch-all (project root)
		{Prefix: "secret", Access: None}, // deny secrets (longer prefix wins)
	}}
	if got := p.Check("foo/bar.txt"); got != ReadWrite {
		t.Errorf("catch-all should grant arbitrary path, got %v", got)
	}
	if got := p.Check(""); got != ReadWrite {
		t.Errorf("catch-all should grant root, got %v", got)
	}
	if got := p.Check("secret/key.txt"); got != None {
		t.Errorf("longer gate prefix should override catch-all, got %v", got)
	}
	if got := p.Check("secret"); got != None {
		t.Errorf("gate prefix exact should be None, got %v", got)
	}

	// Without a catch-all, uncovered paths still default to None.
	p2 := Policy{Rules: []Rule{{Prefix: "allowed", Access: Read}}}
	if got := p2.Check("other"); got != None {
		t.Errorf("uncovered path should default None, got %v", got)
	}
}
