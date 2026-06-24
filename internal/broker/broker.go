// Package broker defines the command/file/network Action broker boundary.
// In milestone 1 this is a scaffold only — the Permissive impl allows everything.
// The red-team tests in redteam_test.go enumerate the bypass attempts that a
// RestrictiveBroker (milestone 2) must deny; they are currently skipped but will
// go green when the real broker lands.
package broker

// Action is one thing an agent (or any caller) wants to do.
type Action struct {
	Kind   string // "exec" | "read" | "net"
	Target string // binary name, file path, or URL
}

// Decision is the broker's verdict.
type Decision struct {
	Allow  bool
	Reason string
}

// Broker is the boundary gate.
type Broker interface {
	Check(a Action) Decision
}

// Permissive is the current (milestone 1) no-op broker — it allows everything.
// It exists so the engine compiles and runs; it is NOT safe for agent use.
// Replace with RestrictiveBroker in milestone 2.
type Permissive struct{}

// Check always allows. This is intentionally unsafe; the red-team tests document
// what must be denied once the real broker is implemented.
func (Permissive) Check(_ Action) Decision {
	return Decision{Allow: true, Reason: "permissive (milestone 1 — no restrictions)"}
}

// compile-time assertion
var _ Broker = Permissive{}
