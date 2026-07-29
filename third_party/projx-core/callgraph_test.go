package core

import "testing"

const callGo = `package x

func helper() int { return 1 }

func use() int { return helper() + helper() }

func other() { use() }
`

func TestGoCallGraph(t *testing.T) {
	f, err := Parse("x.go", []byte(callGo))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// helper() is called twice in use() but collapses to ONE edge; sorted by From,To.
	want := []Edge{
		{From: "x.go::other", To: "x.go::use"},
		{From: "x.go::use", To: "x.go::helper"},
	}
	got := f.CallEdges()
	if len(got) != len(want) {
		t.Fatalf("CallEdges = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("edge[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

const callPy = `def helper():
    return 1

def use():
    return helper() + helper()

class C:
    def run(self):
        return self.missing() + use()
`

func TestPyCallGraph(t *testing.T) {
	f, err := Parse("x.py", []byte(callPy))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := f.CallEdges()
	// use→helper (intra-file), and method C.run→use. self.missing() resolves to no
	// local symbol, so it's dropped — proving external calls don't leak edges.
	wantPresent := []Edge{
		{From: "x.py::use", To: "x.py::helper"},
		{From: "x.py::C.run", To: "x.py::use"},
	}
	for _, w := range wantPresent {
		found := false
		for _, e := range got {
			if e == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing edge %+v in %v", w, got)
		}
	}
	for _, e := range got {
		if e.To == "x.py::missing" || e.From == e.To {
			t.Errorf("unexpected edge %+v (external/self should be dropped)", e)
		}
	}
}
