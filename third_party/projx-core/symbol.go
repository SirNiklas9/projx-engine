package core

// SymKind is the coarse declaration category — the Tier-1 "shallow tier" that
// works on any language by recognizing declaration node-kinds (no tags.scm, no
// type checking). func/method/type/const/var is enough for symbols, search, the
// call graph, and the context digest.
type SymKind int

const (
	// SymFunc represents a function symbol.
	SymFunc SymKind = iota
	// SymMethod represents a method symbol.
	SymMethod
	// SymType represents a type symbol.
	SymType
	// SymConst represents a constant symbol.
	SymConst
	// SymVar represents a variable symbol.
	SymVar
)

var symKindNames = map[SymKind]string{
	SymFunc: "func", SymMethod: "method", SymType: "type",
	SymConst: "const", SymVar: "var",
}

func (k SymKind) String() string {
	if n, ok := symKindNames[k]; ok {
		return n
	}
	return "sym?"
}

// Symbol is one top-level declaration: its stable ID, kind, name, and span, plus
// — for functions and methods — the control-flow Body model. The ID is the same
// identity context deltas and verify snapshots key on (see StableID).
type Symbol struct {
	ID   string  // stable: path::Name, or path::Recv.Name for methods
	Kind SymKind
	Name string
	Recv string // receiver type for methods; "" otherwise
	Span Span
	Body *Node // control-flow model for func/method bodies; nil for type/const/var
	// Calls are the callee names invoked in this symbol's body (raw, may repeat),
	// as the normalizer found them. File.CallEdges resolves them to edges.
	Calls []string
}
