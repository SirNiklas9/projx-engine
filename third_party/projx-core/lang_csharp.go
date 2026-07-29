package core

import (
	"fmt"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// csNorm is the C# language pack. Brace-based and class-oriented like Go+Python
// combined: types hold methods, blocks hold statements directly, calls are
// invocation_expression. Maps onto the same abstract model + StableID + calls.
type csNorm struct{}

func init() { Register(csNorm{}, ".cs") }

func (csNorm) Lang() string { return "csharp" }

func (csNorm) Normalize(path string, src []byte) (*File, error) {
	lang := grammars.CSharpLanguage()
	tree, err := gts.NewParser(lang).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("core: parse %s: %w", path, err)
	}
	if tree == nil {
		return nil, fmt.Errorf("core: tree-sitter returned no tree for %s", path)
	}
	b := &csBuilder{src: src, lang: lang}
	root := tree.RootNode()
	f := &File{Path: path, Lang: "csharp", Root: &Node{Kind: KFile, Span: b.span(root)}}
	for i := 0; i < root.NamedChildCount(); i++ {
		b.declare(root.NamedChild(i), "", f)
	}
	return f, nil
}

type csBuilder struct {
	src  []byte
	lang *gts.Language
}

func (b *csBuilder) kind(n *gts.Node) string                  { return n.Type(b.lang) }
func (b *csBuilder) field(n *gts.Node, name string) *gts.Node { return n.ChildByFieldName(name, b.lang) }
func (b *csBuilder) text(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return n.Text(b.src)
}
func (b *csBuilder) span(n *gts.Node) Span {
	if n == nil {
		return Span{}
	}
	return Span{Start: int(n.StartByte()), End: int(n.EndByte())}
}
func (b *csBuilder) namedChildren(n *gts.Node) []*gts.Node {
	out := make([]*gts.Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}
func (b *csBuilder) firstChild(n *gts.Node, kind string) *gts.Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); b.kind(c) == kind {
			return c
		}
	}
	return nil
}

func (b *csBuilder) declare(n *gts.Node, recv string, f *File) {
	switch b.kind(n) {
	case "namespace_declaration", "file_scoped_namespace_declaration":
		if body := b.field(n, "body"); body != nil {
			for i := 0; i < body.NamedChildCount(); i++ {
				b.declare(body.NamedChild(i), recv, f)
			}
		}
	case "class_declaration", "struct_declaration", "interface_declaration", "record_declaration", "enum_declaration":
		name := b.text(b.field(n, "name"))
		if name == "" {
			name = b.text(b.firstChild(n, "identifier"))
		}
		f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, "", name), Kind: SymType, Name: name, Span: b.span(n)})
		if body := b.field(n, "body"); body != nil {
			for i := 0; i < body.NamedChildCount(); i++ {
				b.declare(body.NamedChild(i), name, f) // members carry recv=type
			}
		}
	case "method_declaration", "constructor_declaration", "local_function_statement":
		name := b.text(b.field(n, "name"))
		kind := SymFunc
		if recv != "" {
			kind = SymMethod
		}
		body := b.buildFunc(n, name)
		f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: kind, Name: name, Recv: recv, Span: b.span(n), Body: body, Calls: b.collectCalls(n)})
		f.Root.Children = append(f.Root.Children, body)
	case "field_declaration":
		b.declareVars(n, recv, f)
	case "property_declaration":
		if name := b.text(b.field(n, "name")); name != "" {
			f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: SymVar, Name: name, Recv: recv, Span: b.span(n)})
		}
	}
}

// declareVars emits a SymVar for each variable_declarator under a field/local decl.
func (b *csBuilder) declareVars(n *gts.Node, recv string, f *File) {
	var walk func(*gts.Node)
	walk = func(m *gts.Node) {
		if b.kind(m) == "variable_declarator" {
			if id := b.firstChild(m, "identifier"); id != nil {
				name := b.text(id)
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(f.Path, recv, name), Kind: SymVar, Name: name, Recv: recv, Span: b.span(m)})
			}
			return
		}
		for i := 0; i < m.NamedChildCount(); i++ {
			walk(m.NamedChild(i))
		}
	}
	walk(n)
}

func (b *csBuilder) buildFunc(decl *gts.Node, name string) *Node {
	nd := &Node{Kind: KFunc, Span: b.span(decl), Label: name}
	if body := b.field(decl, "body"); body != nil {
		nd.Children = b.buildStmts(b.bodyStmts(body))
	}
	return nd
}

// bodyStmts returns the statements of a body that is a block (its children) or a
// single statement (the node itself — C# allows brace-less bodies).
func (b *csBuilder) bodyStmts(body *gts.Node) []*gts.Node {
	if b.kind(body) == "block" {
		return b.namedChildren(body)
	}
	return []*gts.Node{body}
}

func (b *csBuilder) buildStmts(stmts []*gts.Node) []*Node {
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
		if csIsControl(b.kind(s)) {
			flush()
			out = append(out, b.buildControl(s))
		} else {
			pending = append(pending, s)
		}
	}
	flush()
	return out
}

func csIsControl(k string) bool {
	switch k {
	case "if_statement", "for_statement", "foreach_statement", "for_each_statement",
		"while_statement", "do_statement", "switch_statement", "return_statement",
		"break_statement", "continue_statement", "goto_statement", "throw_statement", "block":
		return true
	}
	return false
}

func (b *csBuilder) buildControl(s *gts.Node) *Node {
	switch b.kind(s) {
	case "if_statement":
		return b.buildIf(s)
	case "for_statement", "while_statement", "do_statement", "foreach_statement", "for_each_statement":
		n := &Node{Kind: KLoop, Span: b.span(s)}
		if c := b.field(s, "condition"); c != nil {
			n.Slots = append(n.Slots, Slot{Role: "header", Span: b.span(c), Text: b.text(c)})
		}
		if body := b.field(s, "body"); body != nil {
			n.Children = []*Node{b.wrapBody("body", body)}
		}
		return n
	case "switch_statement":
		n := &Node{Kind: KSwitch, Span: b.span(s)}
		if v := b.field(s, "value"); v != nil {
			n.Slots = append(n.Slots, Slot{Role: "subject", Span: b.span(v), Text: b.text(v)})
		}
		if body := b.firstChild(s, "switch_body"); body != nil {
			n.Children = b.buildCases(body)
		}
		return n
	case "return_statement":
		n := &Node{Kind: KReturn, Span: b.span(s)}
		if s.NamedChildCount() > 0 {
			v := s.NamedChild(0)
			n.Slots = append(n.Slots, Slot{Role: "value", Span: b.span(v), Text: b.text(v)})
		}
		return n
	case "break_statement", "continue_statement", "goto_statement", "throw_statement":
		return &Node{Kind: KBranch, Span: b.span(s), Label: csBranch(b.kind(s))}
	case "block":
		return b.wrapBody("", s)
	}
	sp := b.span(s)
	return &Node{Kind: KRaw, Span: sp, Slots: []Slot{{Role: "stmt", Span: sp, Text: sp.Text(b.src)}}}
}

func (b *csBuilder) buildIf(s *gts.Node) *Node {
	n := &Node{Kind: KIf, Span: b.span(s)}
	if c := b.field(s, "condition"); c != nil {
		n.Slots = append(n.Slots, Slot{Role: "cond", Span: b.span(c), Text: b.text(c)})
	}
	if cons := b.field(s, "consequence"); cons != nil {
		n.Children = append(n.Children, b.wrapBody("then", cons))
	}
	if alt := b.field(s, "alternative"); alt != nil {
		if b.kind(alt) == "if_statement" {
			ei := b.buildIf(alt)
			ei.Role = "else"
			n.Children = append(n.Children, ei)
		} else {
			n.Children = append(n.Children, b.wrapBody("else", alt))
		}
	}
	return n
}

func (b *csBuilder) wrapBody(role string, body *gts.Node) *Node {
	return &Node{Kind: KBlock, Role: role, Span: b.span(body), Children: b.buildStmts(b.bodyStmts(body))}
}

// buildCases turns a switch_body's switch_sections into KCase arms (was missing —
// switch bodies vanished from the model). Each section: its pattern label(s) before
// the ':' become the match slot (empty = default); statements after become children.
func (b *csBuilder) buildCases(body *gts.Node) []*Node {
	var cases []*Node
	for i := 0; i < body.NamedChildCount(); i++ {
		sec := body.NamedChild(i)
		if b.kind(sec) != "switch_section" {
			continue
		}
		cases = append(cases, b.buildCaseClause(sec))
	}
	return cases
}

// buildCaseClause splits a case/section at its ':' — named children before it are the
// match label(s) (none = default), statements after become the arm's body.
func (b *csBuilder) buildCaseClause(c *gts.Node) *Node {
	cn := &Node{Kind: KCase, Span: b.span(c)}
	colon := -1
	for i := 0; i < c.ChildCount(); i++ {
		if b.kind(c.Child(i)) == ":" {
			colon = int(c.Child(i).StartByte())
			break
		}
	}
	var firstM, lastM *gts.Node
	var bodyNodes []*gts.Node
	for i := 0; i < c.NamedChildCount(); i++ {
		ch := c.NamedChild(i)
		if colon >= 0 && int(ch.EndByte()) <= colon {
			if firstM == nil {
				firstM = ch
			}
			lastM = ch
		} else {
			bodyNodes = append(bodyNodes, ch)
		}
	}
	cn.Children = b.buildStmts(bodyNodes)
	if firstM == nil {
		cn.Label = "default"
	} else {
		sp := Span{Start: int(firstM.StartByte()), End: int(lastM.EndByte())}
		cn.Slots = append(cn.Slots, Slot{Role: "match", Span: sp, Text: string(b.src[sp.Start:sp.End])})
	}
	return cn
}

func (b *csBuilder) collectCalls(n *gts.Node) []string {
	var calls []string
	var walk func(*gts.Node)
	walk = func(m *gts.Node) {
		if m == nil {
			return
		}
		if b.kind(m) == "invocation_expression" {
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

func (b *csBuilder) calleeName(fn *gts.Node) string {
	if fn == nil {
		return ""
	}
	switch b.kind(fn) {
	case "identifier":
		return fn.Text(b.src)
	case "member_access_expression":
		if nm := b.field(fn, "name"); nm != nil {
			return nm.Text(b.src)
		}
	}
	return ""
}

func csBranch(kind string) string {
	switch kind {
	case "break_statement":
		return "break"
	case "continue_statement":
		return "continue"
	case "goto_statement":
		return "goto"
	case "throw_statement":
		return "throw"
	}
	return kind
}
