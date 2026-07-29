package grants

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseDecision(t *testing.T) {
	cases := []struct {
		reply string
		want  int
		acc   int
		scope Scope
		ttl   time.Duration
	}{
		{"", 1, 0, "", 0},
		{"n\n", 1, 0, "", 0},
		{"no", 2, 0, "", 0},
		{"y\n", 1, 1, ScopeOnce, 0},
		{"y", 2, 2, ScopeOnce, 0},
		{"y forever\n", 1, 1, ScopePermanent, 0},
		{"Y FOREVER", 2, 2, ScopePermanent, 0},
		{"y 30m\n", 1, 1, ScopeTTL, 30 * time.Minute},
		{"y bogus", 1, 1, ScopeOnce, 0}, // unknown qualifier -> one-shot, not deny
	}
	for _, c := range cases {
		d := ParseDecision(c.reply, c.want)
		if d.Access != c.acc || d.Scope != c.scope || d.TTL != c.ttl {
			t.Errorf("ParseDecision(%q,%d) = %+v; want acc=%d scope=%q ttl=%v",
				c.reply, c.want, d, c.acc, c.scope, c.ttl)
		}
	}
}

// TestCLIApproverThroughBroker proves the human-facing path end-to-end: a typed
// "y forever" reply flows approver -> broker -> a persisted permanent grant.
func TestCLIApproverThroughBroker(t *testing.T) {
	ap := NewCLIApprover(strings.NewReader("y forever\n"), &bytes.Buffer{})
	store := NewMemStore()
	b := &Broker{Store: store, Approver: ap, Timeout: time.Second, CellID: "c", GrantedBy: "cli"}

	if acc := b.Decide(KindFS, "data/x", 1); acc != 1 {
		t.Fatalf("expected grant of Read, got %d", acc)
	}
	act, _ := store.Active("c", KindFS)
	if len(act) != 1 || act[0].Scope != ScopePermanent || act[0].Subject != "data/x" {
		t.Fatalf("expected one permanent grant for data/x, got %+v", act)
	}

	// A denying reply must not grant or persist.
	ap2 := NewCLIApprover(strings.NewReader("n\n"), &bytes.Buffer{})
	b2 := &Broker{Store: NewMemStore(), Approver: ap2, Timeout: time.Second, CellID: "c"}
	if acc := b2.Decide(KindNet, "evil.test", 1); acc != 0 {
		t.Fatalf("deny reply must block, got %d", acc)
	}
}
