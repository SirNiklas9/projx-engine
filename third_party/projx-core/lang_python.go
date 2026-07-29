package core

import (
	"fmt"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// pyNorm is the SECOND concrete Normalizer — Python. It proves the core is
// language-agnostic: Python is structurally unlike Go (classes hold methods,
// blocks have no statement_list, if uses elif/else clauses, for uses left/right)
// yet it maps onto the SAME abstract model + the SAME StableID scheme. The Go
// normalizer is untouched; this just registers another mapping behind Normalizer.
//
// (Helpers are duplicated from the Go normalizer on purpose — a shared base will
// be extracted once a third language makes the common shape obvious. Discover the
// abstraction, don't impose it early.)
type pyNorm struct{}

func init() { Register(pyNorm{}, ".py") }

func (pyNorm) Lang() string { return "python" }

func (pyNorm) Normalize(path string, src []byte) (*File, error) {
	lang := grammars.PythonLanguage()
	tree, err := gts.NewParser(lang).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("core: parse %s: %w", path, err)
	}
	if tree == nil {
		return nil, fmt.Errorf("core: tree-sitter returned no tree for %s", path)
	}
	b := &pyBuilder{src: src, lang: lang}
	root := tree.RootNode()
	f := &File{Path: path, Lang: "python", Root: &Node{Kind: KFile, Span: b.span(root)}}
	for i := 0; i < root.NamedChildCount(); i++ {
		b.declare(root.NamedChild(i), "", f)
	}
	return f, nil
}

type pyBuilder struct {
	src  []byte
	lang *gts.Language
}

func (b *pyBuilder) kind(n *gts.Node) string                  { return n.Type(b.lang) }
func (b *pyBuilder) field(n *gts.Node, name string) *gts.Node { return n.ChildByFieldName(name, b.lang) }
func (b *pyBuilder) text(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return n.Text(b.src)
}
func (b *pyBuilder) span(n *gts.Node) Span {
	if n == nil {
		return Span{}
	}
	return Span{Start: int(n.StartByte()), End: int(n.EndByte())}
}

// declare emits a symbol for a module- or class-body statement. recv is the
// enclosing class name ("" at module level) — so methods get path::Class.name.
func (b *pyBuilder) declare(n *gts.Node, recv string, f *File) {
	switch b.kind(n) {
	case "function_definition":
		name := b.text(b.field(n, "name"))
		kind := SymFunc
		if recv != "" {
			kind = SymMethod
		}
		body := b.buildFunc(n, name)
		f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: kind, Name: name, Recv: recv, Span: b.span(n), Body: body, Calls: b.collectCalls(n)})
		f.Root.Children = append(f.Root.Children, body)

	case "class_definition":
		cname := b.text(b.field(n, "name"))
		f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, "", cname), Kind: SymType, Name: cname, Span: b.span(n)})
		if body := b.field(n, "body"); body != nil {
			for i := 0; i < body.NamedChildCount(); i++ {
				b.declare(body.NamedChild(i), cname, f) // methods carry recv=class
			}
		}

	case "decorated_definition":
		if def := b.field(n, "definition"); def != nil {
			b.declare(def, recv, f)
		}

	case "assignment":
		if recv != "" {
			return // class-body attribute assignment; skip for now
		}
		if lhs := b.field(n, "left"); lhs != nil && b.kind(lhs) == "identifier" {
			name := b.text(lhs)
			if name != "_" {
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, "", name), Kind: SymVar, Name: name, Span: b.span(n)})
			}
		}
	}
}

func (b *pyBuilder) buildFunc(decl *gts.Node, name string) *Node {
	n := &Node{Kind: KFunc, Span: b.span(decl), Label: name}
	if body := b.field(decl, "body"); body != nil {
		n.Children = b.buildStmts(b.stmts(body))
	}
	return n
}

// stmts returns a Python block's statements directly (no statement_list wrapper).
func (b *pyBuilder) stmts(block *gts.Node) []*gts.Node {
	out := make([]*gts.Node, 0, block.NamedChildCount())
	for i := 0; i < block.NamedChildCount(); i++ {
		out = append(out, block.NamedChild(i))
	}
	return out
}

func (b *pyBuilder) buildStmts(stmts []*gts.Node) []*Node {
	var out []*Node
	var pending []*gts.Node
	flush := func() {
		if len(pending) == 0 {
			return
		}
		blk := &Node{Kind: KBlock, Span: Span{Start: int(pending[0].StartByte()), End: int(pending[len(pending)-1].EndByte())}}
		for _, s := range pending {
			sp := b.span(s)
			blk.Children = append(blk.Children, &Node{Kind: KRaw, Span: sp, Slots: []Slot{{Role: "stmt", Span: sp, Text: sp.Text(b.src)}}})
		}
		out = append(out, blk)
		pending = nil
	}
	for _, s := range stmts {
		if pyIsControl(b.kind(s)) {
			flush()
			out = append(out, b.buildControl(s))
		} else {
			pending = append(pending, s)
		}
	}
	flush()
	return out
}

func pyIsControl(k string) bool {
	switch k {
	case "if_statement", "for_statement", "while_statement", "match_statement",
		"return_statement", "break_statement", "continue_statement", "raise_statement":
		return true
	}
	return false
}

func (b *pyBuilder) buildControl(s *gts.Node) *Node {
	switch b.kind(s) {
	case "if_statement":
		return b.buildIf(s)
	case "for_statement":
		n := &Node{Kind: KLoop, Span: b.span(s)}
		if left, right := b.field(s, "left"), b.field(s, "right"); left != nil && right != nil {
			sp := Span{Start: int(left.StartByte()), End: int(right.EndByte())}
			n.Slots = append(n.Slots, Slot{Role: "header", Span: sp, Text: sp.Text(b.src)})
		}
		if body := b.field(s, "body"); body != nil {
			n.Children = []*Node{b.wrapBlock("body", body)}
		}
		return n
	case "while_statement":
		n := &Node{Kind: KLoop, Span: b.span(s)}
		if c := b.field(s, "condition"); c != nil {
			n.Slots = append(n.Slots, Slot{Role: "header", Span: b.span(c), Text: b.text(c)})
		}
		if body := b.field(s, "body"); body != nil {
			n.Children = []*Node{b.wrapBlock("body", body)}
		}
		return n
	case "match_statement":
		n := &Node{Kind: KSwitch, Span: b.span(s)}
		if subj := b.field(s, "subject"); subj != nil {
			n.Slots = append(n.Slots, Slot{Role: "subject", Span: b.span(subj), Text: b.text(subj)})
		}
		if body := b.field(s, "body"); body != nil {
			for i := 0; i < body.NamedChildCount(); i++ {
				cc := body.NamedChild(i)
				if b.kind(cc) != "case_clause" {
					continue
				}
				cn := &Node{Kind: KCase, Span: b.span(cc)}
				if cons := b.field(cc, "consequence"); cons != nil {
					cn.Children = b.buildStmts(b.stmts(cons))
				}
				n.Children = append(n.Children, cn)
			}
		}
		return n
	case "return_statement":
		n := &Node{Kind: KReturn, Span: b.span(s)}
		if s.NamedChildCount() > 0 {
			v := s.NamedChild(0)
			n.Slots = append(n.Slots, Slot{Role: "value", Span: b.span(v), Text: b.text(v)})
		}
		return n
	case "break_statement", "continue_statement", "raise_statement":
		return &Node{Kind: KBranch, Span: b.span(s), Label: pyBranchLabel(b.kind(s))}
	}
	sp := b.span(s)
	return &Node{Kind: KRaw, Span: sp, Slots: []Slot{{Role: "stmt", Span: sp, Text: sp.Text(b.src)}}}
}

func (b *pyBuilder) buildIf(s *gts.Node) *Node {
	n := &Node{Kind: KIf, Span: b.span(s)}
	if c := b.field(s, "condition"); c != nil {
		n.Slots = append(n.Slots, Slot{Role: "cond", Span: b.span(c), Text: b.text(c)})
	}
	if cons := b.field(s, "consequence"); cons != nil {
		n.Children = append(n.Children, b.wrapBlock("then", cons))
	}
	if alt := b.field(s, "alternative"); alt != nil {
		b.appendAlt(n, alt)
	}
	return n
}

// appendAlt maps Python's else_clause / elif_clause onto the abstract else branch.
func (b *pyBuilder) appendAlt(n *Node, alt *gts.Node) {
	switch b.kind(alt) {
	case "else_clause":
		blk := b.field(alt, "body")
		if blk == nil {
			blk = b.firstChild(alt, "block")
		}
		if blk != nil {
			n.Children = append(n.Children, b.wrapBlock("else", blk))
		}
	case "elif_clause":
		ei := &Node{Kind: KIf, Span: b.span(alt), Role: "else"}
		if c := b.field(alt, "condition"); c != nil {
			ei.Slots = append(ei.Slots, Slot{Role: "cond", Span: b.span(c), Text: b.text(c)})
		}
		if cons := b.field(alt, "consequence"); cons != nil {
			ei.Children = append(ei.Children, b.wrapBlock("then", cons))
		}
		if a := b.field(alt, "alternative"); a != nil {
			b.appendAlt(ei, a)
		}
		n.Children = append(n.Children, ei)
	}
}

func (b *pyBuilder) wrapBlock(role string, block *gts.Node) *Node {
	return &Node{Kind: KBlock, Role: role, Span: b.span(block), Children: b.buildStmts(b.stmts(block))}
}

func (b *pyBuilder) firstChild(n *gts.Node, kind string) *gts.Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); b.kind(c) == kind {
			return c
		}
	}
	return nil
}

// collectCalls walks a function subtree and returns the callee names of every
// call in it (raw, may repeat). `foo()` → "foo"; `obj.method()` → "method".
func (b *pyBuilder) collectCalls(n *gts.Node) []string {
	var calls []string
	var walk func(*gts.Node)
	walk = func(m *gts.Node) {
		if m == nil {
			return
		}
		if b.kind(m) == "call" {
			if name := b.calleeName(b.field(m, "function")); name != "" {
				calls = append(calls, name)
			}
		}
		for i := 0; i < m.NamedChildCount(); i++ {
			walk(m.NamedChild(i))
		}
	}
	walk(n)
	return calls
}

func (b *pyBuilder) calleeName(fn *gts.Node) string {
	if fn == nil {
		return ""
	}
	switch b.kind(fn) {
	case "identifier":
		return fn.Text(b.src)
	case "attribute":
		if a := b.field(fn, "attribute"); a != nil {
			return a.Text(b.src)
		}
	}
	return ""
}

func pyBranchLabel(kind string) string {
	switch kind {
	case "break_statement":
		return "break"
	case "continue_statement":
		return "continue"
	case "raise_statement":
		return "raise"
	}
	return kind
}
