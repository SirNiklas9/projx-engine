package grants

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// CLIApprover is the harness-drivable Approver: it prints a request to Out and
// reads one reply line from In, parsing:
//
//	y            -> grant the requested access, once (not persisted)
//	y 30m        -> grant for a TTL (any time.ParseDuration string)
//	y forever    -> grant permanently (until revoked)
//	n / <blank>  -> deny
//
// In/Out are typically the two ends of a host control socket, so the same
// approver works for a local terminal or a remote relay. Decide is serialized
// so concurrent fs+net requests prompt one at a time.
type CLIApprover struct {
	r   *bufio.Reader
	out io.Writer
	mu  sync.Mutex
}

// NewCLIApprover wires an approver to a reply source and a prompt sink.
func NewCLIApprover(in io.Reader, out io.Writer) *CLIApprover {
	return &CLIApprover{r: bufio.NewReader(in), out: out}
}

// Decide prompts for one request and returns the parsed decision.
func (a *CLIApprover) Decide(req Request) Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintf(a.out, "[projx] %s access requested: %q (level %d) — grant? (y / y 30m / y forever / n): ",
		req.Kind, req.Subject, req.Want)
	line, _ := a.r.ReadString('\n')
	return ParseDecision(line, req.Want)
}

// ParseDecision parses an approver reply into a Decision, granting the caller's
// requested access level (want) on a yes. Exported for reuse by other approver
// front-ends (e.g. a phone relay that maps a tap to the same grammar).
func ParseDecision(reply string, want int) Decision {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(reply)))
	if len(f) == 0 || (f[0] != "y" && f[0] != "yes") {
		return Decision{Access: 0} // deny
	}
	if len(f) == 1 {
		return Decision{Access: want, Scope: ScopeOnce}
	}
	switch f[1] {
	case "forever", "always", "permanent":
		return Decision{Access: want, Scope: ScopePermanent}
	default:
		if d, err := time.ParseDuration(f[1]); err == nil && d > 0 {
			return Decision{Access: want, Scope: ScopeTTL, TTL: d}
		}
		// unrecognized qualifier → safest is a one-shot grant, not a deny:
		// the human said "y".
		return Decision{Access: want, Scope: ScopeOnce}
	}
}
