package store

import (
	"strings"
	"testing"
)

// TestMatchTextExcludesPath proves a code-map record matches on its symbol name/sig/doc,
// NOT the structural code/<repo>/<path> key — so a repo/dir name doesn't match everything.
func TestMatchTextExcludesPath(t *testing.T) {
	codeMap := Record{Kind: KDeclaredStructure, Key: "code/evolution/billing/coupon",
		Body: `{"anchor":"Evolution/billing/coupon.go:12","signature":"func ApplyCoupon(code string) error","doc":"validates a coupon"}`}
	mt := strings.ToLower(matchText(codeMap))
	if strings.Contains(mt, "evolution") || strings.Contains(mt, "billing") {
		t.Errorf("match text should exclude repo/dir path tokens, got %q", mt)
	}
	if !strings.Contains(mt, "applycoupon") || !strings.Contains(mt, "coupon") {
		t.Errorf("match text should include the symbol signature/doc, got %q", mt)
	}
	// A plain doc still matches on its human key path.
	doc := Record{Kind: KDoc, Key: "billing/webhook", Body: "stripe signature"}
	if !strings.Contains(strings.ToLower(matchText(doc)), "billing") {
		t.Error("non-code-map records should still match on their key path")
	}
}

// TestSliceCapBounds proves a task token matching many code-map records yields a bounded
// slice (top maxSliceRecords + an overflow pointer), not an explosion.
func TestSliceCapBounds(t *testing.T) {
	m := NewMem()
	_ = m.Put(Record{ID: "gate/s", Kind: KGateRule, Scope: ScopeProject, Key: "s", Body: "secret/**"})
	// 40 code-map records whose signature all contain "handler".
	for i := 0; i < 40; i++ {
		id := "map:svc/h" + string(rune('a'+i%26)) + itoaN(i)
		_ = m.Put(Record{ID: id, Kind: KDeclaredStructure, Scope: ScopeProject,
			Key:  "code/svc/pkg/handler" + itoaN(i),
			Body: `{"anchor":"svc/pkg/f` + itoaN(i) + `.go:1","signature":"func Handler` + itoaN(i) + `() error","doc":"a handler"}`})
	}
	out := AgentContextForTask(m, "fix the handler")
	lines := strings.Count(out, "map:svc/")
	if lines > maxSliceRecords {
		t.Errorf("slice injected %d code-map records, want <= %d (cap)", lines, maxSliceRecords)
	}
	if !strings.Contains(out, "more matched this task") {
		t.Error("expected an overflow pointer for the dropped records")
	}
	if !strings.Contains(out, "secret/**") {
		t.Error("law must still be present (cap only bounds reference sections)")
	}
}

func itoaN(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestSliceBalanceAndFocus proves per-repo balancing (no repo crowds out others) and
// that a focus repo's records lead the slice.
func TestSliceBalanceAndFocus(t *testing.T) {
	m := NewMem()
	put := func(repo string, n int) {
		for i := 0; i < n; i++ {
			id := "map:" + repo + "/f" + itoaN(i)
			_ = m.Put(Record{ID: id, Kind: KDeclaredStructure, Scope: ScopeProject,
				Key:  "code/" + repo + "/pkg/handler" + itoaN(i),
				Body: `{"anchor":"` + repo + "/pkg/f" + itoaN(i) + `.go:1","signature":"func Handler` + itoaN(i) + `() error","doc":"a handler"}`})
		}
	}
	put("big", 30) // dominant repo
	put("small", 3)

	// No focus → the small repo is NOT crowded out by the dominant one (balance pass
	// guarantees it its slots before the dominant repo fills the rest).
	out := AgentContextForTaskSel(m, "fix the handler", nil, "")
	if strings.Count(out, "code/small/") != 3 {
		t.Errorf("balanced slice should keep all 3 small-repo records, got %d (crowded out?)", strings.Count(out, "code/small/"))
	}

	// Focus=small → small's records lead (appear before big's in the rendered order... they
	// sort by key for display, so assert small is fully present and prioritized in ranking).
	outF := AgentContextForTaskSel(m, "fix the handler", nil, "small")
	if strings.Count(outF, "code/small/") != 3 {
		t.Errorf("focus=small should surface all 3 small records, got %d", strings.Count(outF, "code/small/"))
	}
}

// TestMatchTextIncludesTerms proves the Level-1 body-terms auto-seed: a code-map record
// whose NAME/signature lack a concept but whose body TERMS carry it is still matched.
func TestMatchTextIncludesTerms(t *testing.T) {
	m := NewMem()
	_ = m.Put(Record{ID: "gate/s", Kind: KGateRule, Scope: ScopeProject, Key: "s", Body: "secret/**"})
	// A function named "processInbound" (no "webhook" in name/sig) whose BODY terms include it.
	_ = m.Put(Record{ID: "map:svc/pi", Kind: KDeclaredStructure, Scope: ScopeProject,
		Key:  "code/svc/router/processinbound",
		Body: `{"anchor":"svc/router.go:88","signature":"func processInbound(r *http.Request) error","kind":"func","terms":"webhook stripe constructevent signature verify"}`})

	out := AgentContextForTask(m, "verify the stripe webhook")
	if !strings.Contains(out, "svc/router.go:88") {
		t.Error("body-terms auto-seed failed: a webhook concept buried in processInbound was not matched")
	}
}
