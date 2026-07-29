package core

import "testing"

const sampleJS = `class Server {
  start(x) {
    let total = 0;
    for (let i = 0; i < x; i++) {
      if (i == 3) {
        return total;
      }
      total += this.helper(i);
    }
    return total;
  }
  helper(v) { return v; }
}
`

func TestJavaScript(t *testing.T) {
	f, err := Parse("app.js", []byte(sampleJS))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Lang != "javascript" {
		t.Fatalf("Lang = %q, want javascript", f.Lang)
	}
	want := map[string]SymKind{
		"app.js::Server":        SymType,
		"app.js::Server.start":  SymMethod,
		"app.js::Server.helper": SymMethod,
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
	assertControlFlow(t, f, "app.js::Server.start", "(i == 3)")
	assertEdge(t, f, "app.js::Server.start", "app.js::Server.helper")
}

const sampleTS = `interface Point { x: number; y: number; }

function area(p: Point): number {
  let a = 0;
  for (let i = 0; i < p.x; i++) {
    if (i < 0) {
      return a;
    }
    a += scale(i);
  }
  return a;
}

function scale(v: number): number {
  return v * 2;
}
`

func TestTypeScript(t *testing.T) {
	f, err := Parse("geom.ts", []byte(sampleTS))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Lang != "typescript" {
		t.Fatalf("Lang = %q, want typescript", f.Lang)
	}
	want := map[string]SymKind{
		"geom.ts::Point": SymType, // interface
		"geom.ts::area":  SymFunc,
		"geom.ts::scale": SymFunc, // arrow function assigned to const
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
	assertControlFlow(t, f, "geom.ts::area", "(i < 0)")
	assertEdge(t, f, "geom.ts::area", "geom.ts::scale")
}

func TestJSRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, l := range Languages() {
		have[l] = true
	}
	for _, l := range []string{"javascript", "typescript", "astro"} {
		if !have[l] {
			t.Errorf("Languages() missing %q (got %v)", l, Languages())
		}
	}
}
