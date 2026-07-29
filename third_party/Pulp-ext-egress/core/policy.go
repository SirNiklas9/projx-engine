// Package core provides the egress-gateway primitives for Pulp-ext-egress.
// Policy and name-matching are platform-neutral and unit-testable on Windows.
// RunConfinedNetns is Linux-only; a stub is provided for other platforms.
package core

import (
	"strings"
	"sync"
)

// Policy defines what a confined process is allowed to reach over the network.
type Policy struct {
	// AllowNames is the list of DNS names the process may resolve and connect to.
	// Entries may be exact ("api.example.com") or suffix-prefixed (".example.com"
	// to allow all subdomains of example.com, plus example.com itself).
	// Matching is case-insensitive; trailing dots are stripped.
	AllowNames []string

	// AllowIPs is an optional list of IP addresses (dotted-decimal) that are
	// pre-approved without requiring a DNS resolution step. These are added to
	// the allowed-IP set at gateway startup.
	AllowIPs []string

	// Resolve is the function used to look up a hostname. If nil, the gateway
	// uses net.DefaultResolver.LookupHost. Inject a custom resolver in tests.
	// The function must return a list of dotted-decimal IPv4 addresses.
	Resolve func(name string) ([]string, error)

	// TCPRedirect maps "resolvedIP:port" → "realAddr" for TCP connections.
	// When a TCP connection arrives for an allowed IP, the gateway splices it
	// to the real address. If a key is absent, the connection is accepted but
	// immediately closed (no real backend). In production this would be the
	// real destination; in tests it points to a local echo server.
	TCPRedirect map[string]string

	// Live is an optional runtime-mutable set of additionally-allowed names,
	// layered on top of AllowNames. It is the live-grant surface: Allow adds a
	// host while the process runs, Revoke removes it. Shared via pointer so a
	// copied Policy still references the same set.
	Live *LiveNames

	// OnConnectMiss, if set, is consulted when a name matches neither AllowNames
	// nor Live. It returns true to grant the name live (the gateway then records
	// it in Live and proceeds to resolve). This is the seam the host-side grants
	// Broker plugs into — kept as a plain func so core needs no dependency on the
	// grants package.
	OnConnectMiss func(name string) bool
}

// LiveNames is a mutex-guarded, runtime-mutable set of allowed DNS names. It
// supports live grant (Allow) and revoke (Revoke), so the network cage can open
// and close individual hosts while the confined process runs.
type LiveNames struct {
	mu    sync.RWMutex
	names map[string]struct{}
}

// NewLiveNames returns an empty live set.
func NewLiveNames() *LiveNames {
	return &LiveNames{names: map[string]struct{}{}}
}

// Allow adds name to the live set (idempotent).
func (l *LiveNames) Allow(name string) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	l.mu.Lock()
	l.names[name] = struct{}{}
	l.mu.Unlock()
}

// Revoke removes name from the live set (the "remove access" operation).
func (l *LiveNames) Revoke(name string) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	l.mu.Lock()
	delete(l.names, name)
	l.mu.Unlock()
}

// Match reports whether name is permitted by the live set, honoring the same
// exact/suffix semantics as the static list.
func (l *LiveNames) Match(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	l.mu.RLock()
	defer l.mu.RUnlock()
	for entry := range l.names {
		if matchOne(name, entry) {
			return true
		}
	}
	return false
}

// MatchName reports whether name is permitted by the policy — either the static
// AllowNames list or the runtime Live set. name should be a bare hostname (no
// trailing dot). Matching is case-insensitive. A list entry ".foo.com" matches
// "foo.com" and any subdomain; "foo.com" matches exactly.
func (p Policy) MatchName(name string) bool {
	if matchName(name, p.AllowNames) {
		return true
	}
	return p.Live != nil && p.Live.Match(name)
}

// Decide is the gateway's per-name authorization chokepoint. It returns true if
// the name is already permitted (static or live); otherwise it consults the
// OnConnectMiss broker hook, and on a grant records the name in Live so the
// subsequent connection (and future checks) succeed. Platform-neutral so it is
// unit-testable without a netns.
func (p Policy) Decide(name string) bool {
	if p.MatchName(name) {
		return true
	}
	if p.OnConnectMiss != nil && p.OnConnectMiss(name) {
		if p.Live != nil {
			p.Live.Allow(name)
		}
		return true
	}
	return false
}

// matchName reports whether name matches any entry in allowList.
func matchName(name string, allowList []string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, entry := range allowList {
		if matchOne(name, entry) {
			return true
		}
	}
	return false
}

// matchOne applies the exact/suffix rule for a single list entry against a
// pre-normalized (lowercased, trailing-dot-stripped) name.
func matchOne(name, entry string) bool {
	entry = strings.ToLower(entry)
	if bare, ok := strings.CutPrefix(entry, "."); ok {
		// ".foo.com" matches "foo.com" and "bar.foo.com"
		return name == bare || strings.HasSuffix(name, entry)
	}
	return name == entry
}
