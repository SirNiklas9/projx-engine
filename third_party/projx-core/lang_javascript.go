package core

import (
	"fmt"
	"strings"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// jsNorm covers the JavaScript/TypeScript family (incl. JSX/TSX and Astro). One
// builder maps every dialect onto the same abstract model — JS and TS share node
// types (TS is a superset), and Astro embeds them. Registered per-extension so each
// file uses the right grammar while reporting the right language label.
type jsNorm struct {
	lang    string
	grammar func() *gts.Language
}

func init() {
	js := jsNorm{"javascript", grammars.JavascriptLanguage}
	ts := jsNorm{"typescript", grammars.TypescriptLanguage}
	tsx := jsNorm{"typescript", grammars.TsxLanguage}
	astro := jsNorm{"astro", grammars.AstroLanguage}
	Register(js, ".js", ".mjs", ".cjs", ".jsx")
	Register(ts, ".ts", ".mts", ".cts")
	Register(tsx, ".tsx")
	Register(astro, ".astro")
}

func (n jsNorm) Lang() string { return n.lang }

func (n jsNorm) Normalize(path string, src []byte) (*File, error) {
	lang := n.grammar()
	tree, err := gts.NewParser(lang).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("core: parse %s: %w", path, err)
	}
	if tree == nil {
		return nil, fmt.Errorf("core: tree-sitter returned no tree for %s", path)
	}
	b := &jsBuilder{src: src, lang: lang}
	root := tree.RootNode()
	f := &File{Path: path, Lang: n.lang, Root: &Node{Kind: KFile, Span: b.span(root)}}
	for i := 0; i < root.NamedChildCount(); i++ {
		b.declare(root.NamedChild(i), "", f)
	}
	return f, nil
}

type jsBuilder struct {
	src  []byte
	lang *gts.Language
}

func (b *jsBuilder) kind(n *gts.Node) string                  { return n.Type(b.lang) }
func (b *jsBuilder) field(n *gts.Node, name string) *gts.Node { return n.ChildByFieldName(name, b.lang) }
func (b *jsBuilder) text(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return n.Text(b.src)
}
func (b *jsBuilder) span(n *gts.Node) Span {
	if n == nil {
		return Span{}
	}
	return Span{Start: int(n.StartByte()), End: int(n.EndByte())}
}
func (b *jsBuilder) firstNamed(n *gts.Node) *gts.Node {
	if n != nil && n.NamedChildCount() > 0 {
		return n.NamedChild(0)
	}
	return nil
}

// declare emits a symbol for a top-level or class-body declaration. recv is the
// enclosing class ("" at module level) so methods get path::Class.name.
func (b *jsBuilder) declare(n *gts.Node, recv string, f *File) {
	switch b.kind(n) {
	case "export_statement":
		if d := b.field(n, "declaration"); d != nil {
			b.declare(d, recv, f)
		} else if v := b.field(n, "value"); v != nil {
			b.declare(v, recv, f)
		}

	case "function_declaration", "generator_function_declaration", "function_expression":
		name := b.text(b.field(n, "name"))
		if name == "" {
			return
		}
		body := b.buildFunc(n, name)
		f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: symFuncOrMethod(recv), Name: name, Recv: recv, Span: b.span(n), Body: body, Calls: b.collectCalls(n)})
		f.Root.Children = append(f.Root.Children, body)

	case "method_definition":
		name := b.text(b.field(n, "name"))
		if acc := b.accessorKind(n); acc != "" {
			name = acc + " " + name // get/set legitimately share a name in JS — keep IDs distinct
		}
		body := b.buildFunc(n, name)
		f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: SymMethod, Name: name, Recv: recv, Span: b.span(n), Body: body, Calls: b.collectCalls(n)})
		f.Root.Children = append(f.Root.Children, body)

	case "class_declaration", "abstract_class_declaration":
		cname := b.text(b.field(n, "name"))
		if cname != "" {
			f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, "", cname), Kind: SymType, Name: cname, Span: b.span(n)})
		}
		if body := b.field(n, "body"); body != nil {
			for i := 0; i < body.NamedChildCount(); i++ {
				b.declare(body.NamedChild(i), cname, f) // methods carry recv=class
			}
		}

	case "lexical_declaration", "variable_declaration":
		for i := 0; i < n.NamedChildCount(); i++ {
			vd := n.NamedChild(i)
			if b.kind(vd) != "variable_declarator" {
				continue
			}
			name := b.text(b.field(vd, "name"))
			if name == "" || name == "_" {
				continue
			}
			val := b.field(vd, "value")
			if val != nil && jsIsFuncExpr(b.kind(val)) {
				body := b.buildFunc(val, name) // arrow/function expression assigned to a name
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: symFuncOrMethod(recv), Name: name, Recv: recv, Span: b.span(vd), Body: body, Calls: b.collectCalls(val)})
				f.Root.Children = append(f.Root.Children, body)
			} else {
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, "", name), Kind: SymVar, Name: name, Span: b.span(vd)})
			}
		}

	case "interface_declaration", "type_alias_declaration", "enum_declaration":
		if name := b.text(b.field(n, "name")); name != "" {
			f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, "", name), Kind: SymType, Name: name, Span: b.span(n)})
		}
	}
}

// accessorKind returns "get"/"set" when a method_definition is an accessor, so the
// two (which share a property name) get distinct symbol IDs instead of colliding.
func (b *jsBuilder) accessorKind(n *gts.Node) string {
	if fs := strings.Fields(b.text(n)); len(fs) > 0 && (fs[0] == "get" || fs[0] == "set") {
		return fs[0]
	}
	return ""
}

func jsIsFuncExpr(k string) bool {
	return k == "arrow_function" || k == "function" || k == "function_expression" || k == "generator_function"
}

func symFuncOrMethod(recv string) SymKind {
	if recv != "" {
		return SymMethod
	}
	return SymFunc
}

func (b *jsBuilder) buildFunc(decl *gts.Node, name string) *Node {
	n := &Node{Kind: KFunc, Span: b.span(decl), Label: name}
	if body := b.field(decl, "body"); body != nil && b.kind(body) == "statement_block" {
		n.Children = b.buildStmts(b.stmts(body))
	}
	return n
}

// stmts returns a statement_block's statements (its named children).
func (b *jsBuilder) stmts(block *gts.Node) []*gts.Node {
	out := make([]*gts.Node, 0, block.NamedChildCount())
	for i := 0; i < block.NamedChildCount(); i++ {
		out = append(out, block.NamedChild(i))
	}
	return out
}

func (b *jsBuilder) buildStmts(stmts []*gts.Node) []*Node {
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
		if jsIsControl(b.kind(s)) {
			flush()
			out = append(out, b.buildControl(s))
		} else {
			pending = append(pending, s)
		}
	}
	flush()
	return out
}

func jsIsControl(k string) bool {
	switch k {
	case "if_statement", "for_statement", "for_in_statement", "while_statement", "do_statement",
		"switch_statement", "return_statement", "break_statement", "continue_statement", "throw_statement":
		return true
	}
	return false
}

func (b *jsBuilder) buildControl(s *gts.Node) *Node {
	switch b.kind(s) {
	case "if_statement":
		return b.buildIf(s)
	case "for_statement", "for_in_statement", "while_statement", "do_statement":
		n := &Node{Kind: KLoop, Span: b.span(s)}
		body := b.field(s, "body")
		n.Slots = append(n.Slots, b.loopHeader(s, body))
		if body != nil {
			n.Children = []*Node{b.wrapBlock("body", body)}
		}
		return n
	case "switch_statement":
		n := &Node{Kind: KSwitch, Span: b.span(s)}
		if v := b.field(s, "value"); v != nil {
			n.Slots = append(n.Slots, Slot{Role: "subject", Span: b.span(v), Text: b.text(v)})
		}
		if body := b.field(s, "body"); body != nil {
			for i := 0; i < body.NamedChildCount(); i++ {
				cc := body.NamedChild(i)
				k := b.kind(cc)
				if k != "switch_case" && k != "switch_default" {
					continue
				}
				cn := &Node{Kind: KCase, Span: b.span(cc)}
				start := 0
				if k == "switch_case" {
					start = 1 // first named child is the case value
				}
				var cs []*gts.Node
				for j := start; j < cc.NamedChildCount(); j++ {
					cs = append(cs, cc.NamedChild(j))
				}
				cn.Children = b.buildStmts(cs)
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
	case "break_statement", "continue_statement", "throw_statement":
		return &Node{Kind: KBranch, Span: b.span(s), Label: jsBranchLabel(b.kind(s))}
	}
	sp := b.span(s)
	return &Node{Kind: KRaw, Span: sp, Slots: []Slot{{Role: "stmt", Span: sp, Text: sp.Text(b.src)}}}
}

func (b *jsBuilder) buildIf(s *gts.Node) *Node {
	n := &Node{Kind: KIf, Span: b.span(s)}
	if c := b.field(s, "condition"); c != nil {
		n.Slots = append(n.Slots, Slot{Role: "cond", Span: b.span(c), Text: b.text(c)})
	}
	if cons := b.field(s, "consequence"); cons != nil {
		n.Children = append(n.Children, b.wrapBlock("then", cons))
	}
	if alt := b.field(s, "alternative"); alt != nil {
		// alternative is an else_clause; its child is a block or another if (else-if)
		child := b.firstNamed(alt)
		if child != nil {
			if b.kind(child) == "if_statement" {
				ei := b.buildIf(child)
				ei.Role = "else"
				n.Children = append(n.Children, ei)
			} else {
				n.Children = append(n.Children, b.wrapBlock("else", child))
			}
		}
	}
	return n
}

// wrapBlock wraps a statement_block (or a single braceless statement) as a KBlock.
func (b *jsBuilder) wrapBlock(role string, blk *gts.Node) *Node {
	if b.kind(blk) == "statement_block" {
		return &Node{Kind: KBlock, Role: role, Span: b.span(blk), Children: b.buildStmts(b.stmts(blk))}
	}
	return &Node{Kind: KBlock, Role: role, Span: b.span(blk), Children: b.buildStmts([]*gts.Node{blk})}
}

func (b *jsBuilder) loopHeader(s, body *gts.Node) Slot {
	end := int(s.EndByte())
	if body != nil {
		end = int(body.StartByte())
	}
	sp := Span{Start: int(s.StartByte()), End: end}
	return Slot{Role: "header", Span: sp, Text: strings.TrimSpace(sp.Text(b.src))}
}

func (b *jsBuilder) collectCalls(n *gts.Node) []string {
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

func (b *jsBuilder) calleeName(fn *gts.Node) string {
	if fn == nil {
		return ""
	}
	switch b.kind(fn) {
	case "identifier":
		return fn.Text(b.src)
	case "member_expression":
		if p := b.field(fn, "property"); p != nil {
			return p.Text(b.src)
		}
	}
	return ""
}

func jsBranchLabel(kind string) string {
	switch kind {
	case "break_statement":
		return "break"
	case "continue_statement":
		return "continue"
	case "throw_statement":
		return "throw"
	}
	return kind
}
