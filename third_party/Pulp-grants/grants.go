// Package grants is the shared live-permission spine for the ProjX cage.
//
// It provides one model for "request access at point of use" used by both the
// filesystem seam (Pulp-ext-fuse OnMiss) and the network seam (Pulp-ext-egress
// connect-miss): a Broker consults a persistent GrantStore, and on a miss asks
// an Approver, failing CLOSED on timeout. Grants are revocable and may carry a
// TTL — so the cage can dynamically grant AND remove access while the agent
// runs.
//
// Access ints mirror Pulp-ext-fuse core.Access: 1=Read, 2=ReadWrite. For net
// the only meaningful level is 1 (allow).
package grants

import (
	crand "crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Kind distinguishes filesystem grants from network grants.
type Kind string

const (
	KindFS  Kind = "fs"
	KindNet Kind = "net"
)

// Scope controls persistence of a granted decision.
type Scope string

const (
	ScopeOnce      Scope = "once"      // not persisted; covers this miss only
	ScopeTTL       Scope = "ttl"       // persisted until ExpiresAt
	ScopePermanent Scope = "permanent" // persisted until revoked
)

// Grant is one row in the grants table.
type Grant struct {
	ID        string
	CellID    string
	Kind      Kind
	Subject   string // fs: relpath prefix; net: dns name, or ".suffix" for a wildcard
	Access    int    // fs: 1=Read 2=ReadWrite; net: 1
	Scope     Scope
	ExpiresAt int64 // unix ms; 0 for once/permanent
	GrantedBy string
	CreatedAt int64 // unix ms
	RevokedAt int64 // unix ms; 0 = live
}

// nowFn is overridable in tests; production uses the wall clock.
var nowFn = func() int64 { return time.Now().UnixMilli() }

// IsActive reports whether g currently grants access (not revoked, not expired).
func (g Grant) IsActive(nowMs int64) bool {
	if g.RevokedAt != 0 {
		return false
	}
	return g.ExpiresAt == 0 || g.ExpiresAt > nowMs
}

// SubjectCovers reports whether a grant on grantSubject covers an access to
// query, using longest-prefix semantics for fs and exact/suffix for net. This
// mirrors Pulp-ext-fuse core.Policy.Check and Pulp-ext-egress matchName so the
// live layer agrees with the static floor.
func SubjectCovers(kind Kind, grantSubject, query string) bool {
	if kind == KindNet {
		if strings.HasPrefix(grantSubject, ".") {
			return query == grantSubject[1:] || strings.HasSuffix(query, grantSubject)
		}
		return query == grantSubject
	}
	// fs
	return query == grantSubject || strings.HasPrefix(query, grantSubject+"/")
}

// GrantStore is the persistence + lookup contract. Implementations must honor
// expiry and revocation in Lookup/Active.
type GrantStore interface {
	// Lookup returns the maximum active access covering subject for kind, and
	// ok = (that access >= want).
	Lookup(kind Kind, subject string, want int) (granted int, ok bool)
	// Put inserts (or replaces by ID) a grant.
	Put(g Grant) error
	// Active returns the live (non-revoked, non-expired) grants for a cell+kind,
	// used to pre-seed live policy at mount.
	Active(cellID string, kind Kind) ([]Grant, error)
	// List returns all grants for a cell (including revoked/expired) for admin.
	List(cellID string) ([]Grant, error)
	// Revoke marks one grant revoked by ID. Returns nil even if already revoked.
	Revoke(id string) error
	// RevokeMatching revokes every active grant of kind whose Subject covers
	// query (the "remove access to X now" operation). Returns the count revoked.
	RevokeMatching(kind Kind, query string) (int, error)
}

// Decision is what an Approver returns. Access 0 means deny.
type Decision struct {
	Access int
	Scope  Scope
	TTL    time.Duration // used only when Scope == ScopeTTL
}

// Request is handed to an Approver on a miss.
type Request struct {
	CellID  string
	Kind    Kind
	Subject string
	Want    int
}

// Approver decides a live request. Implementations: a programmatic test stub, a
// CLI control-socket prompt, or (deferred) a phone relay over /remote-control.
type Approver interface {
	Decide(req Request) Decision
}

// Broker is the host-side decision point wired into the fs/net seams.
type Broker struct {
	Store     GrantStore
	Approver  Approver
	Timeout   time.Duration // fail-closed after this; default 60s if zero
	CellID    string
	GrantedBy string
	// Audit, if set, is called after a grant is persisted.
	Audit func(g Grant)
}

// Decide answers a miss: returns the granted access (0 = deny). It first checks
// the store (covers permanent/TTL reload), else asks the Approver with a
// timeout, failing CLOSED. A ttl/permanent grant is persisted; once is not.
func (b *Broker) Decide(kind Kind, subject string, want int) int {
	if b.Store != nil {
		if g, ok := b.Store.Lookup(kind, subject, want); ok {
			return g
		}
	}
	if b.Approver == nil {
		return 0
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	decCh := make(chan Decision, 1) // buffered so the goroutine can't leak past a timeout
	go func() {
		decCh <- b.Approver.Decide(Request{CellID: b.CellID, Kind: kind, Subject: subject, Want: want})
	}()
	var dec Decision
	select {
	case dec = <-decCh:
	case <-time.After(timeout):
		return 0 // fail-closed
	}
	if dec.Access < want {
		return 0 // denied or insufficient
	}
	if (dec.Scope == ScopeTTL || dec.Scope == ScopePermanent) && b.Store != nil {
		g := Grant{
			ID:        newID(),
			CellID:    b.CellID,
			Kind:      kind,
			Subject:   subject,
			Access:    dec.Access,
			Scope:     dec.Scope,
			GrantedBy: b.GrantedBy,
			CreatedAt: nowFn(),
		}
		if dec.Scope == ScopeTTL {
			g.ExpiresAt = nowFn() + dec.TTL.Milliseconds()
		}
		if err := b.Store.Put(g); err == nil && b.Audit != nil {
			b.Audit(g)
		}
	}
	return dec.Access
}

func newID() string {
	var b [8]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}
