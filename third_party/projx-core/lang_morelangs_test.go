package core

import "testing"

const sampleCS = `class Server {
    int n;
    public int Start(int x) {
        int total = 0;
        for (int i = 0; i < x; i++) {
            if (i == n) {
                return total;
            }
            total += Helper(i);
        }
        return total;
    }
    int Helper(int v) { return v; }
}
`

func TestCSharp(t *testing.T) {
	f, err := Parse("Server.cs", []byte(sampleCS))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Lang != "csharp" {
		t.Fatalf("Lang = %q, want csharp", f.Lang)
	}
	want := map[string]SymKind{
		"Server.cs::Server":        SymType,
		"Server.cs::Server.Start":  SymMethod,
		"Server.cs::Server.Helper": SymMethod,
	}
	got := map[string]SymKind{}
	for _, s := range f.Symbols {
		got[s.ID] = s.Kind
	}
	for id, k := range want {
		if got[id] != k {
			t.Errorf("symbol %q = %v, want %v", id, got[id], k)
		}
	}
	assertControlFlow(t, f, "Server.cs::Server.Start", "i == n")
	assertEdge(t, f, "Server.cs::Server.Start", "Server.cs::Server.Helper")
}

const sampleOdin = `package sample

Server :: struct {
    n: int,
}

start :: proc(s: ^Server, x: int) -> int {
    total := 0
    for i := 0; i < x; i += 1 {
        if i == s.n {
            return total
        }
        total += helper(i)
    }
    return total
}

helper :: proc(v: int) -> int {
    return v
}
`

func TestOdin(t *testing.T) {
	f, err := Parse("sample.odin", []byte(sampleOdin))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Lang != "odin" {
		t.Fatalf("Lang = %q, want odin", f.Lang)
	}
	want := map[string]SymKind{
		"sample.odin::Server": SymType,
		"sample.odin::start":  SymFunc, // Odin has no method receivers
		"sample.odin::helper": SymFunc,
	}
	got := map[string]SymKind{}
	for _, s := range f.Symbols {
		got[s.ID] = s.Kind
	}
	for id, k := range want {
		if got[id] != k {
			t.Errorf("symbol %q = %v, want %v", id, got[id], k)
		}
	}
	assertControlFlow(t, f, "sample.odin::start", "i == s.n")
	assertEdge(t, f, "sample.odin::start", "sample.odin::helper")
}

func TestFourLanguages(t *testing.T) {
	have := map[string]bool{}
	for _, l := range Languages() {
		have[l] = true
	}
	for _, l := range []string{"go", "python", "csharp", "odin"} {
		if !have[l] {
			t.Errorf("Languages() missing %q (got %v)", l, Languages())
		}
	}
}

// --- shared assertions ------------------------------------------------------

func assertControlFlow(t *testing.T, f *File, symID, wantCond string) {
	t.Helper()
	s, ok := f.SymbolByID(symID)
	if !ok || s.Body == nil {
		t.Fatalf("%s has no body model", symID)
	}
	kinds := map[Kind]int{}
	var ifNode *Node
	s.Body.Walk(func(n *Node) bool {
		kinds[n.Kind]++
		if n.Kind == KIf {
			ifNode = n
		}
		return true
	})
	for _, k := range []Kind{KLoop, KIf, KReturn} {
		if kinds[k] == 0 {
			t.Errorf("%s: missing %v node (kinds=%v)", symID, k, kinds)
		}
	}
	if ifNode == nil || len(ifNode.Slots) == 0 {
		t.Fatalf("%s: if has no condition slot", symID)
	}
	if got := ifNode.Slots[0].Text; got != wantCond {
		t.Errorf("%s: if cond = %q, want %q", symID, got, wantCond)
	}
}

func assertEdge(t *testing.T, f *File, from, to string) {
	t.Helper()
	for _, e := range f.CallEdges() {
		if e.From == from && e.To == to {
			return
		}
	}
	t.Errorf("missing call edge %s → %s (got %v)", from, to, f.CallEdges())
}
