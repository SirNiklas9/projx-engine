package core

import "testing"

const sampleGo = `package sample

const Max = 64

type Server struct{ n int }

func (s *Server) Start(x int) int {
	total := 0
	for i := 0; i < x; i++ {
		if i == s.n {
			return total
		}
		total += i
	}
	return total
}

func Plain() {}
`

func TestStableID(t *testing.T) {
	if got := StableID("p.go", "Server", "Start"); got != "p.go::Server.Start" {
		t.Errorf("method ID = %q, want p.go::Server.Start", got)
	}
	if got := StableID("p.go", "", "Plain"); got != "p.go::Plain" {
		t.Errorf("func ID = %q, want p.go::Plain", got)
	}
}

func TestSpanOverlap(t *testing.T) {
	a := Span{Start: 10, End: 20}
	if !a.Overlaps(Span{Start: 15, End: 25}) {
		t.Error("overlapping spans reported as non-overlapping")
	}
	if a.Overlaps(Span{Start: 20, End: 30}) {
		t.Error("adjacent spans [10,20) and [20,30) must not overlap")
	}
}

func TestParseSymbols(t *testing.T) {
	f, err := Parse("sample.go", []byte(sampleGo))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string]SymKind{
		"sample.go::Max":          SymConst,
		"sample.go::Server":       SymType,
		"sample.go::Server.Start": SymMethod,
		"sample.go::Plain":        SymFunc,
	}
	got := map[string]SymKind{}
	for _, s := range f.Symbols {
		got[s.ID] = s.Kind
	}
	for id, kind := range want {
		if got[id] != kind {
			t.Errorf("symbol %q: kind = %v, want %v (present=%v)", id, got[id], kind, hasKey(got, id))
		}
	}
	// The method must carry its receiver so its ID is collision-free.
	start, ok := f.SymbolByID("sample.go::Server.Start")
	if !ok || start.Recv != "Server" {
		t.Fatalf("Start symbol: ok=%v recv=%q, want recv=Server", ok, start.Recv)
	}
}

func TestParseControlFlow(t *testing.T) {
	f, err := Parse("sample.go", []byte(sampleGo))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	start, ok := f.SymbolByID("sample.go::Server.Start")
	if !ok || start.Body == nil {
		t.Fatal("Start has no body model")
	}

	kinds := map[Kind]int{}
	var ifNode *Node
	start.Body.Walk(func(n *Node) bool {
		kinds[n.Kind]++
		if n.Kind == KIf {
			ifNode = n
		}
		return true
	})

	for _, k := range []Kind{KLoop, KIf, KReturn, KBlock} {
		if kinds[k] == 0 {
			t.Errorf("control-flow model missing a %v node (kinds=%v)", k, kinds)
		}
	}

	if ifNode == nil || len(ifNode.Slots) == 0 {
		t.Fatal("If node has no condition slot")
	}
	if got := ifNode.Slots[0].Text; got != "i == s.n" {
		t.Errorf("If condition slot = %q, want \"i == s.n\"", got)
	}
	// The condition slot's span must round-trip to the same text in source.
	if rt := ifNode.Slots[0].Span.Text([]byte(sampleGo)); rt != "i == s.n" {
		t.Errorf("condition span round-trip = %q, want \"i == s.n\"", rt)
	}
}

func TestLanguagesRegistered(t *testing.T) {
	found := false
	for _, l := range Languages() {
		if l == "go" {
			found = true
		}
	}
	if !found {
		t.Errorf("Languages() = %v, want it to include \"go\"", Languages())
	}
}

func hasKey(m map[string]SymKind, k string) bool { _, ok := m[k]; return ok }
