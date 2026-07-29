package core

import (
	"fmt"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// odinNorm is the Odin language pack (the user's language). Odin has no method
// receivers — every procedure is a plain function; a procedure's body lives at
// procedure_declaration → procedure → block. Control-flow handling is positional
// (condition = first non-block child) since the grammar is less field-rich.
type odinNorm struct{}

func init() { Register(odinNorm{}, ".odin") }

func (odinNorm) Lang() string { return "odin" }

func (odinNorm) Normalize(path string, src []byte) (*File, error) {
	lang := grammars.OdinLanguage()
	tree, err := gts.NewParser(lang).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("core: parse %s: %w", path, err)
	}
	if tree == nil {
		return nil, fmt.Errorf("core: tree-sitter returned no tree for %s", path)
	}
	b := &odinBuilder{src: src, lang: lang}
	root := tree.RootNode()
	f := &File{Path: path, Lang: "odin", Root: &Node{Kind: KFile, Span: b.span(root)}}
	for i := 0; i < root.NamedChildCount(); i++ {
		decl := root.NamedChild(i)
		switch b.kind(decl) {
		case "struct_declaration", "enum_declaration", "union_declaration", "bit_set_declaration":
			if name := b.declName(decl); name != "" {
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: SymType, Name: name, Span: b.span(decl)})
			}
		case "procedure_declaration":
			name := b.declName(decl)
			body := b.buildFunc(decl)
			f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: SymFunc, Name: name, Span: b.span(decl), Body: body, Calls: b.collectCalls(decl)})
			f.Root.Children = append(f.Root.Children, body)
		case "const_declaration":
			if name := b.declName(decl); name != "" {
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: SymConst, Name: name, Span: b.span(decl)})
			}
		case "variable_declaration", "var_declaration":
			if name := b.declName(decl); name != "" {
				f.Symbols = append(f.Symbols, Symbol{ID: StableID(path, "", name), Kind: SymVar, Name: name, Span: b.span(decl)})
			}
		}
	}
	return f, nil
}

type odinBuilder struct {
	src  []byte
	lang *gts.Language
}

func (b *odinBuilder) kind(n *gts.Node) string                  { return n.Type(b.lang) }
func (b *odinBuilder) field(n *gts.Node, name string) *gts.Node { return n.ChildByFieldName(name, b.lang) }
func (b *odinBuilder) text(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return n.Text(b.src)
}
func (b *odinBuilder) span(n *gts.Node) Span {
	if n == nil {
		return Span{}
	}
	return Span{Start: int(n.StartByte()), End: int(n.EndByte())}
}
func (b *odinBuilder) namedChildren(n *gts.Node) []*gts.Node {
	out := make([]*gts.Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}
func (b *odinBuilder) firstChild(n *gts.Node, kind string) *gts.Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); b.kind(c) == kind {
			return c
		}
	}
	return nil
}
func (b *odinBuilder) firstNonBlock(n *gts.Node) *gts.Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); b.kind(c) != "block" {
			return c
		}
	}
	return nil
}
func (b *odinBuilder) lastBlock(n *gts.Node) *gts.Node {
	var blk *gts.Node
	for i := 0; i < n.NamedChildCount(); i++ {
		if c := n.NamedChild(i); b.kind(c) == "block" {
			blk = c
		}
	}
	return blk
}

func (b *odinBuilder) declName(decl *gts.Node) string {
	if id := b.firstChild(decl, "identifier"); id != nil {
		return b.text(id)
	}
	return ""
}

func (b *odinBuilder) buildFunc(decl *gts.Node) *Node {
	n := &Node{Kind: KFunc, Span: b.span(decl), Label: b.declName(decl)}
	if blk := b.procBlock(decl); blk != nil {
		n.Children = b.buildStmts(b.namedChildren(blk))
	}
	return n
}

// procBlock finds a procedure's body: procedure_declaration → procedure → block.
func (b *odinBuilder) procBlock(decl *gts.Node) *gts.Node {
	proc := b.firstChild(decl, "procedure")
	if proc == nil {
		return nil
	}
	return b.lastBlock(proc)
}

func (b *odinBuilder) buildStmts(stmts []*gts.Node) []*Node {
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
		if odinIsControl(b.kind(s)) {
			flush()
			out = append(out, b.buildControl(s))
		} else {
			pending = append(pending, s)
		}
	}
	flush()
	return out
}

func odinIsControl(k string) bool {
	switch k {
	case "if_statement", "for_statement", "switch_statement", "return_statement",
		"break_statement", "continue_statement", "when_statement", "block":
		return true
	}
	return false
}

func (b *odinBuilder) buildControl(s *gts.Node) *Node {
	switch b.kind(s) {
	case "if_statement", "when_statement":
		return b.buildIf(s)
	case "for_statement":
		n := &Node{Kind: KLoop, Span: b.span(s)}
		if first := b.firstNonBlock(s); first != nil {
			end := first.EndByte()
			for i := 0; i < s.NamedChildCount(); i++ {
				if c := s.NamedChild(i); b.kind(c) != "block" && c.EndByte() > end {
					end = c.EndByte()
				}
			}
			sp := Span{Start: int(first.StartByte()), End: int(end)}
			n.Slots = append(n.Slots, Slot{Role: "header", Span: sp, Text: sp.Text(b.src)})
		}
		if blk := b.lastBlock(s); blk != nil {
			n.Children = []*Node{b.wrapBlock("body", blk)}
		}
		return n
	case "switch_statement":
		n := &Node{Kind: KSwitch, Span: b.span(s)}
		if subj := b.firstNonBlock(s); subj != nil {
			n.Slots = append(n.Slots, Slot{Role: "subject", Span: b.span(subj), Text: b.text(subj)})
		}
		n.Children = b.buildCases(s)
		return n
	case "return_statement":
		n := &Node{Kind: KReturn, Span: b.span(s)}
		if s.NamedChildCount() > 0 {
			v := s.NamedChild(0)
			n.Slots = append(n.Slots, Slot{Role: "value", Span: b.span(v), Text: b.text(v)})
		}
		return n
	case "break_statement", "continue_statement":
		return &Node{Kind: KBranch, Span: b.span(s), Label: odinBranch(b.kind(s))}
	case "block":
		return b.wrapBlock("", s)
	}
	sp := b.span(s)
	return &Node{Kind: KRaw, Span: sp, Slots: []Slot{{Role: "stmt", Span: sp, Text: sp.Text(b.src)}}}
}

func (b *odinBuilder) buildIf(s *gts.Node) *Node {
	n := &Node{Kind: KIf, Span: b.span(s)}
	if c := b.firstNonBlock(s); c != nil {
		n.Slots = append(n.Slots, Slot{Role: "cond", Span: b.span(c), Text: b.text(c)})
	}
	blocks := 0
	for i := 0; i < s.NamedChildCount(); i++ {
		c := s.NamedChild(i)
		switch b.kind(c) {
		case "block":
			blocks++
			role := "then"
			if blocks > 1 {
				role = "else"
			}
			n.Children = append(n.Children, b.wrapBlock(role, c))
		case "if_statement", "when_statement": // else-if chain
			ei := b.buildIf(c)
			ei.Role = "else"
			n.Children = append(n.Children, ei)
		}
	}
	return n
}

func (b *odinBuilder) wrapBlock(role string, block *gts.Node) *Node {
	return &Node{Kind: KBlock, Role: role, Span: b.span(block), Children: b.buildStmts(b.namedChildren(block))}
}

// buildCases turns a switch_statement's switch_case arms into KCase nodes (was
// missing — Odin switch bodies vanished from the model). Each case: match value(s)
// before the ':' become the match slot (none = default), statements after = body.
func (b *odinBuilder) buildCases(sw *gts.Node) []*Node {
	var cases []*Node
	for i := 0; i < sw.NamedChildCount(); i++ {
		c := sw.NamedChild(i)
		if b.kind(c) != "switch_case" {
			continue
		}
		cases = append(cases, b.buildCaseClause(c))
	}
	return cases
}

func (b *odinBuilder) buildCaseClause(c *gts.Node) *Node {
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

func (b *odinBuilder) collectCalls(n *gts.Node) []string {
	var calls []string
	var walk func(*gts.Node)
	walk = func(m *gts.Node) {
		if m == nil {
			return
		}
		if k := b.kind(m); k == "call_expression" || k == "call" {
			if name := b.calleeName(m); name != "" {
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

func (b *odinBuilder) calleeName(call *gts.Node) string {
	if fn := b.field(call, "function"); fn != nil && b.kind(fn) == "identifier" {
		return b.text(fn)
	}
	if id := b.firstChild(call, "identifier"); id != nil {
		return b.text(id)
	}
	return ""
}

func odinBranch(kind string) string {
	switch kind {
	case "break_statement":
		return "break"
	case "continue_statement":
		return "continue"
	}
	return kind
}
