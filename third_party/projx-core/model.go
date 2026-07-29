package core

// Kind is the language-neutral node vocabulary — the heart of the agnostic core.
//
// The model is CONTROL-FLOW-shaped, not semantics-shaped: it captures structure
// (if / loop / switch / ...) and leaves every expression as an opaque text Slot.
// That is what keeps "add a language" cheap — a per-language normalizer only has
// to recognize control-flow constructs, never parse expressions. Both projections
// (gloss = text, graph = visual) read this one model.
type Kind int

const (
	KFile   Kind = iota // a parsed source file (the root of File.Root)
	KFunc               // a function / method / closure body
	KBlock              // a run of simple statements collapsed together (anti-spaghetti)
	KIf                 // conditional; Slots hold the condition, Children/Role split then/else
	KLoop               // for / while / range / foreach
	KSwitch             // switch / match
	KCase               // one arm of a switch
	KReturn             // return; Slots hold the returned value(s)
	KBranch             // break / continue / goto / fallthrough
	KCall               // a call surfaced as its own node
	KRaw                // a simple statement living inside a Block (opaque text)
)

var kindNames = map[Kind]string{
	KFile: "File", KFunc: "Func", KBlock: "Block", KIf: "If", KLoop: "Loop",
	KSwitch: "Switch", KCase: "Case", KReturn: "Return", KBranch: "Branch",
	KCall: "Call", KRaw: "Raw",
}

func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return "Kind?"
}

// Slot is one verbatim expression inside a node: opaque text plus its byte span.
// The model never parses inside a slot — data-flow scrapes identifiers from Text,
// and editing a slot maps 1:1 back to Span. Role names what the slot is for
// (e.g. "cond", "subject", "value", "receiver").
type Slot struct {
	Role string
	Span Span
	Text string
}

// Node is one element of the abstract control-flow tree.
type Node struct {
	Kind     Kind
	Span     Span
	Slots    []Slot
	Children []*Node
	// Role distinguishes a child's relationship to its parent when it matters —
	// e.g. "then"/"else" under an If, "body" under a Loop, "case" under a Switch.
	Role  string
	Label string // optional precomputed label (a normalizer may fill it; renderers may ignore)
}

// Walk visits n and all descendants in pre-order. Returning false from fn stops
// descent into that node's children (but siblings continue).
func (n *Node) Walk(fn func(*Node) bool) {
	if n == nil {
		return
	}
	if !fn(n) {
		return
	}
	for _, c := range n.Children {
		c.Walk(fn)
	}
}

// Count returns the number of nodes in the subtree rooted at n (n included).
func (n *Node) Count() int {
	if n == nil {
		return 0
	}
	total := 1
	for _, c := range n.Children {
		total += c.Count()
	}
	return total
}
