package core

import "testing"

const samplePy = `class Server:
    def start(self, x):
        total = 0
        for i in range(x):
            if i == self.n:
                return total
            else:
                total += i
        return total

def plain():
    pass

MAX = 64
`

func TestPythonSymbols(t *testing.T) {
	f, err := Parse("sample.py", []byte(samplePy))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Lang != "python" {
		t.Errorf("f.Lang = %q, want python", f.Lang)
	}
	want := map[string]SymKind{
		"sample.py::Server":       SymType,
		"sample.py::Server.start": SymMethod, // method gets recv=class — same scheme as Go
		"sample.py::plain":        SymFunc,
		"sample.py::MAX":          SymVar,
	}
	got := map[string]SymKind{}
	for _, s := range f.Symbols {
		got[s.ID] = s.Kind
	}
	for id, kind := range want {
		if got[id] != kind {
			t.Errorf("symbol %q: kind = %v, want %v", id, got[id], kind)
		}
	}
	m, ok := f.SymbolByID("sample.py::Server.start")
	if !ok || m.Recv != "Server" {
		t.Fatalf("method recv: ok=%v recv=%q, want Server", ok, m.Recv)
	}
}

func TestPythonControlFlow(t *testing.T) {
	f, err := Parse("sample.py", []byte(samplePy))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m, ok := f.SymbolByID("sample.py::Server.start")
	if !ok || m.Body == nil {
		t.Fatal("start has no body model")
	}
	kinds := map[Kind]int{}
	var ifNode *Node
	m.Body.Walk(func(n *Node) bool {
		kinds[n.Kind]++
		if n.Kind == KIf {
			ifNode = n
		}
		return true
	})
	for _, k := range []Kind{KLoop, KIf, KReturn} {
		if kinds[k] == 0 {
			t.Errorf("Python control-flow missing a %v node (kinds=%v)", k, kinds)
		}
	}
	if ifNode == nil || len(ifNode.Slots) == 0 {
		t.Fatal("If node has no condition slot")
	}
	if got := ifNode.Slots[0].Text; got != "i == self.n" {
		t.Errorf("If condition = %q, want \"i == self.n\"", got)
	}
	hasElse := false
	for _, c := range ifNode.Children {
		if c.Role == "else" {
			hasElse = true
		}
	}
	if !hasElse {
		t.Error("If has no else branch")
	}
}

// The agnostic proof: ONE abstract model, TWO structurally-different languages,
// behind the same Normalizer interface and the same StableID scheme.
func TestTwoLanguagesRegistered(t *testing.T) {
	haveGo, havePy := false, false
	for _, l := range Languages() {
		switch l {
		case "go":
			haveGo = true
		case "python":
			havePy = true
		}
	}
	if !haveGo || !havePy {
		t.Errorf("Languages() = %v, want both go and python", Languages())
	}
}
