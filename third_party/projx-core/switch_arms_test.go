package core

import "testing"

// findSwitch walks an AST and returns the first KSwitch node.
func findSwitch(n *Node) *Node {
	if n == nil {
		return nil
	}
	if n.Kind == KSwitch {
		return n
	}
	for _, c := range n.Children {
		if s := findSwitch(c); s != nil {
			return s
		}
	}
	return nil
}

func matchTexts(sw *Node) (matches []string, hasDefault bool) {
	for _, c := range sw.Children {
		if c.Kind != KCase {
			continue
		}
		if c.Label == "default" {
			hasDefault = true
			continue
		}
		for _, sl := range c.Slots {
			if sl.Role == "match" {
				matches = append(matches, sl.Text)
			}
		}
	}
	return
}

func TestSwitchArmsCSharp(t *testing.T) {
	src := []byte(`class C { int M(int x) { switch (x) { case 1: return 1; case 3: return 23; default: return 0; } } }`)
	f, err := csNorm{}.Normalize("C.cs", src)
	if err != nil {
		t.Fatal(err)
	}
	sw := findSwitch(f.Root)
	if sw == nil {
		t.Fatal("C#: no KSwitch node built")
	}
	m, def := matchTexts(sw)
	if len(m) != 2 || !def {
		t.Fatalf("C#: want 2 case matches + default, got matches=%v default=%v (cases=%d)", m, def, len(sw.Children))
	}
	t.Logf("C# switch arms OK: matches=%v default=%v", m, def)
}

func TestSwitchArmsOdin(t *testing.T) {
	src := []byte("f :: proc(x: int) -> int {\n\tswitch x {\n\tcase 1: return 1\n\tcase 2, 3: return 23\n\tcase: return 0\n\t}\n}\n")
	f, err := odinNorm{}.Normalize("f.odin", src)
	if err != nil {
		t.Fatal(err)
	}
	sw := findSwitch(f.Root)
	if sw == nil {
		t.Fatal("Odin: no KSwitch node built")
	}
	m, def := matchTexts(sw)
	if len(m) != 2 || !def {
		t.Fatalf("Odin: want 2 case matches + default, got matches=%v default=%v (cases=%d)", m, def, len(sw.Children))
	}
	t.Logf("Odin switch arms OK: matches=%v default=%v", m, def)
}
