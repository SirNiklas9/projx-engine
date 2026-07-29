package core

import (
	"fmt"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// goNorm is the first concrete Normalizer — Go, via gotreesitter (a PURE-GO
// tree-sitter runtime: no CGo, no C toolchain, cross-compiles to wasip1 so core
// itself can be a wasm cell). It walks the CST structurally by node-kind (Tier 1,
// no tags.scm). Every other language plugs in the same way behind Normalizer —
// that is what makes core language-agnostic. (Fresh build; pith/projx are
// reference for the approach only. gotreesitter is a library choice, like Monaco.)
type goNorm struct{}

func init() { Register(goNorm{}, ".go") }

func (goNorm) Lang() string { return "go" }

func (goNorm) Normalize(path string, src []byte) (*File, error) {
	lang := grammars.GoLanguage()
	tree, err := gts.NewParser(lang).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("core: parse %s: %w", path, err)
	}
	if tree == nil {
		return nil, fmt.Errorf("core: tree-sitter returned no tree for %s", path)
	}
	b := &goBuilder{src: src, lang: lang}
	root := tree.RootNode()
	f := &File{Path: path, Lang: "go", Root: &Node{Kind: KFile, Span: b.span(root)}}

	for i := 0; i < root.NamedChildCount(); i++ {
		decl := root.NamedChild(i)
		switch b.kind(decl) {
		case "function_declaration":
			name := nodeText(b.field(decl, "name"), src)
			body := b.buildFunc(decl, name)
			f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: SymFunc, Name: name, Span: b.span(decl), Body: body, Calls: b.collectCalls(decl)})
			f.Root.Children = append(f.Root.Children, body)

		case "method_declaration":
			name := nodeText(b.field(decl, "name"), src)
			recv := b.recvName(decl)
			body := b.buildFunc(decl, name)
			f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, recv, name), Kind: SymMethod, Name: name, Recv: recv, Span: b.span(decl), Body: body, Calls: b.collectCalls(decl)})
			f.Root.Children = append(f.Root.Children, body)

		case "type_declaration":
			for j := 0; j < decl.NamedChildCount(); j++ {
				spec := decl.NamedChild(j)
				if k := b.kind(spec); k != "type_spec" && k != "type_alias" {
					continue
				}
				name := nodeText(b.field(spec, "name"), src)
				if name == "" {
					name = b.firstChildText(spec, "type_identifier")
				}
				if name == "" || name == "_" {
					continue
				}
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: SymType, Name: name, Span: b.span(spec)})
			}

		case "const_declaration", "var_declaration":
			kind := SymVar
			if b.kind(decl) == "const_declaration" {
				kind = SymConst
			}
			for j := 0; j < decl.NamedChildCount(); j++ {
				spec := decl.NamedChild(j) // const_spec / var_spec
				for k := 0; k < spec.NamedChildCount(); k++ {
					nm := spec.NamedChild(k)
					if b.kind(nm) != "identifier" {
						continue // names are direct identifiers; values live under expression_list
					}
					name := nm.Text(src)
					if name == "_" {
						continue
					}
					f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: kind, Name: name, Span: b.span(spec)})
				}
			}
		}
	}
	return f, nil
}

// goBuilder walks a gotreesitter Go tree into the abstract control-flow model.
type goBuilder struct {
	src  []byte
	lang *gts.Language
}

func (b *goBuilder) kind(n *gts.Node) string                  { return n.Type(b.lang) }
func (b *goBuilder) field(n *gts.Node, name string) *gts.Node { return n.ChildByFieldName(name, b.lang) }

func (b *goBuilder) span(n *gts.Node) Span {
	if n == nil {
		return Span{}
	}
	return Span{Start: int(n.StartByte()), End: int(n.EndByte())}
}

func (b *goBuilder) recvName(method *gts.Node) string {
	recv := b.field(method, "receiver")
	if recv == nil {
		return ""
	}
	return b.firstDescendant(recv, "type_identifier")
}

func (b *goBuilder) buildFunc(decl *gts.Node, name string) *Node {
	n := &Node{Kind: KFunc, Span: b.span(decl), Label: name}
	if body := b.field(decl, "body"); body != nil {
		n.Children = b.buildStmts(b.stmtList(body))
	}
	return n
}

// stmtList returns the statement nodes inside a block (Go wraps them in a
// statement_list; fall back to the block's own named children).
func (b *goBuilder) stmtList(block *gts.Node) []*gts.Node {
	for i := 0; i < block.NamedChildCount(); i++ {
		if c := block.NamedChild(i); b.kind(c) == "statement_list" {
			return b.namedChildren(c)
		}
	}
	return b.namedChildren(block)
}

// buildStmts collapses consecutive SIMPLE statements into one Block (anti-spaghetti)
// and spawns a node only at control structures and terminals.
func (b *goBuilder) buildStmts(stmts []*gts.Node) []*Node {
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
		if isControlKind(b.kind(s)) {
			flush()
			out = append(out, b.buildControl(s))
		} else {
			pending = append(pending, s)
		}
	}
	flush()
	return out
}

func isControlKind(k string) bool {
	switch k {
	case "if_statement", "for_statement", "expression_switch_statement",
		"type_switch_statement", "select_statement", "return_statement",
		"break_statement", "continue_statement", "goto_statement",
		"fallthrough_statement", "block":
		return true
	}
	return false
}

func (b *goBuilder) buildControl(s *gts.Node) *Node {
	switch b.kind(s) {
	case "if_statement":
		return b.buildIf(s)
	case "for_statement":
		n := &Node{Kind: KLoop, Span: b.span(s)}
		if h := b.loopHeader(s); h != nil {
			n.Slots = append(n.Slots, Slot{Role: "header", Span: b.span(h), Text: h.Text(b.src)})
		}
		if body := b.field(s, "body"); body != nil {
			n.Children = []*Node{b.wrapBlock("body", body)}
		}
		return n
	case "expression_switch_statement", "type_switch_statement":
		n := &Node{Kind: KSwitch, Span: b.span(s)}
		if v := b.field(s, "value"); v != nil {
			n.Slots = append(n.Slots, Slot{Role: "subject", Span: b.span(v), Text: v.Text(b.src)})
		}
		n.Children = b.buildCases(s)
		return n
	case "select_statement":
		n := &Node{Kind: KSwitch, Span: b.span(s), Label: "select"}
		n.Children = b.buildCases(s)
		return n
	case "return_statement":
		n := &Node{Kind: KReturn, Span: b.span(s)}
		if s.NamedChildCount() > 0 { // expression_list, if any
			v := s.NamedChild(0)
			n.Slots = append(n.Slots, Slot{Role: "value", Span: b.span(v), Text: v.Text(b.src)})
		}
		return n
	case "break_statement", "continue_statement", "goto_statement", "fallthrough_statement":
		return &Node{Kind: KBranch, Span: b.span(s), Label: branchLabel(b.kind(s))}
	case "block":
		return b.wrapBlock("", s)
	}
	sp := b.span(s)
	return &Node{Kind: KRaw, Span: sp, Slots: []Slot{{Role: "stmt", Span: sp, Text: sp.Text(b.src)}}}
}

func (b *goBuilder) buildIf(s *gts.Node) *Node {
	n := &Node{Kind: KIf, Span: b.span(s)}
	if c := b.field(s, "condition"); c != nil {
		n.Slots = append(n.Slots, Slot{Role: "cond", Span: b.span(c), Text: c.Text(b.src)})
	}
	if cons := b.field(s, "consequence"); cons != nil {
		n.Children = append(n.Children, b.wrapBlock("then", cons))
	}
	if alt := b.field(s, "alternative"); alt != nil {
		if b.kind(alt) == "if_statement" {
			ei := b.buildIf(alt)
			ei.Role = "else"
			n.Children = append(n.Children, ei)
		} else {
			n.Children = append(n.Children, b.wrapBlock("else", alt))
		}
	}
	return n
}

// wrapBlock builds a Block node of the given role from a block's statements.
func (b *goBuilder) wrapBlock(role string, block *gts.Node) *Node {
	return &Node{Kind: KBlock, Role: role, Span: b.span(block), Children: b.buildStmts(b.stmtList(block))}
}

// loopHeader returns a for-statement's header (for_clause / range_clause / bare
// condition) — the first named child that is not the body block.
func (b *goBuilder) loopHeader(s *gts.Node) *gts.Node {
	for i := 0; i < s.NamedChildCount(); i++ {
		if c := s.NamedChild(i); b.kind(c) != "block" {
			return c
		}
	}
	return nil
}

func (b *goBuilder) buildCases(sw *gts.Node) []*Node {
	var cases []*Node
	for i := 0; i < sw.NamedChildCount(); i++ {
		c := sw.NamedChild(i)
		switch b.kind(c) {
		case "expression_case", "type_case", "communication_case":
			cn := &Node{Kind: KCase, Span: b.span(c)}
			if v := b.field(c, "value"); v != nil {
				cn.Slots = append(cn.Slots, Slot{Role: "match", Span: b.span(v), Text: v.Text(b.src)})
			}
			cn.Children = b.buildStmts(b.caseBody(c))
			cases = append(cases, cn)
		case "default_case":
			cn := &Node{Kind: KCase, Span: b.span(c), Label: "default"}
			cn.Children = b.buildStmts(b.caseBody(c))
			cases = append(cases, cn)
		}
	}
	return cases
}

// caseBody returns the statement nodes of a case clause (everything except its
// match value).
func (b *goBuilder) caseBody(c *gts.Node) []*gts.Node {
	val := b.field(c, "value")
	var out []*gts.Node
	for i := 0; i < c.NamedChildCount(); i++ {
		ch := c.NamedChild(i)
		if val != nil && ch.StartByte() == val.StartByte() && ch.EndByte() == val.EndByte() {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// --- small node helpers -----------------------------------------------------

func (b *goBuilder) namedChildren(n *gts.Node) []*gts.Node {
	out := make([]*gts.Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

func (b *goBuilder) firstChildText(n *gts.Node, kind string) string {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); b.kind(c) == kind {
			return c.Text(b.src)
		}
	}
	return ""
}

func (b *goBuilder) firstDescendant(n *gts.Node, kind string) string {
	if n == nil {
		return ""
	}
	if b.kind(n) == kind {
		return n.Text(b.src)
	}
	for i := 0; i < n.NamedChildCount(); i++ {
		if t := b.firstDescendant(n.NamedChild(i), kind); t != "" {
			return t
		}
	}
	return ""
}

// collectCalls walks a declaration subtree and returns the callee names of every
// call expression in it (raw, may repeat). Shallow tier: just the called name.
func (b *goBuilder) collectCalls(n *gts.Node) []string {
	var calls []string
	var walk func(*gts.Node)
	walk = func(m *gts.Node) {
		if m == nil {
			return
		}
		if b.kind(m) == "call_expression" {
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

// calleeName extracts the called name from a call's function expression:
// `foo` → "foo"; `pkg.Foo` / `x.Method` → "Foo" / "Method".
func (b *goBuilder) calleeName(fn *gts.Node) string {
	if fn == nil {
		return ""
	}
	switch b.kind(fn) {
	case "identifier":
		return fn.Text(b.src)
	case "selector_expression":
		if fld := b.field(fn, "field"); fld != nil {
			return fld.Text(b.src)
		}
	}
	return ""
}

func nodeText(n *gts.Node, src []byte) string {
	if n == nil {
		return ""
	}
	return n.Text(src)
}

func branchLabel(kind string) string {
	switch kind {
	case "break_statement":
		return "break"
	case "continue_statement":
		return "continue"
	case "goto_statement":
		return "goto"
	case "fallthrough_statement":
		return "fallthrough"
	}
	return kind
}
