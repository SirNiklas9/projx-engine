package grants

import (
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock makes nowFn deterministic for a test; returns a restore func.
func fakeClock(t *testing.T, start int64) (set func(int64), restore func()) {
	t.Helper()
	prev := nowFn
	var cur atomic.Int64
	cur.Store(start)
	nowFn = func() int64 { return cur.Load() }
	return cur.Store, func() { nowFn = prev }
}

// countApprover records calls and returns a canned Decision.
type countApprover struct {
	calls atomic.Int32
	dec   Decision
	block time.Duration
}

func (a *countApprover) Decide(Request) Decision {
	a.calls.Add(1)
	if a.block > 0 {
		time.Sleep(a.block)
	}
	return a.dec
}

func TestSubjectCovers(t *testing.T) {
	cases := []struct {
		kind          Kind
		grant, query  string
		want          bool
	}{
		{KindFS, "secret", "secret", true},
		{KindFS, "secret", "secret/a/b", true},
		{KindFS, "secret", "secretsauce", false}, // prefix must be path-bounded
		{KindFS, "secret", "other", false},
		{KindNet, "example.com", "example.com", true},
		{KindNet, "example.com", "a.example.com", false}, // exact only
		{KindNet, ".example.com", "a.example.com", true}, // wildcard suffix
		{KindNet, ".example.com", "example.com", true},   // bare apex
		{KindNet, ".example.com", "evilexample.com", false},
	}
	for _, c := range cases {
		if got := SubjectCovers(c.kind, c.grant, c.query); got != c.want {
			t.Errorf("SubjectCovers(%s,%q,%q)=%v want %v", c.kind, c.grant, c.query, got, c.want)
		}
	}
}

// storeContract exercises any GrantStore implementation identically.
func storeContract(t *testing.T, newStore func() GrantStore) {
	set, restore := fakeClock(t, 1_000_000)
	defer restore()
	s := newStore()

	// fs grant: Read on prefix "data"
	must(t, s.Put(Grant{ID: "g1", CellID: "c", Kind: KindFS, Subject: "data", Access: 1, Scope: ScopePermanent, CreatedAt: nowFn()}))
	if acc, ok := s.Lookup(KindFS, "data/file.txt", 1); !ok || acc != 1 {
		t.Fatalf("fs read lookup: acc=%d ok=%v", acc, ok)
	}
	if _, ok := s.Lookup(KindFS, "data/file.txt", 2); ok {
		t.Fatal("read grant must not satisfy ReadWrite")
	}
	if _, ok := s.Lookup(KindFS, "other/x", 1); ok {
		t.Fatal("uncovered path must not be granted")
	}

	// higher access wins
	must(t, s.Put(Grant{ID: "g2", CellID: "c", Kind: KindFS, Subject: "data", Access: 2, Scope: ScopePermanent, CreatedAt: nowFn()}))
	if acc, ok := s.Lookup(KindFS, "data/file.txt", 2); !ok || acc != 2 {
		t.Fatalf("rw lookup after upgrade: acc=%d ok=%v", acc, ok)
	}

	// TTL expiry
	must(t, s.Put(Grant{ID: "g3", CellID: "c", Kind: KindNet, Subject: "api.test", Access: 1, Scope: ScopeTTL, ExpiresAt: nowFn() + 100, CreatedAt: nowFn()}))
	if _, ok := s.Lookup(KindNet, "api.test", 1); !ok {
		t.Fatal("ttl grant should be live before expiry")
	}
	set(nowFn() + 200) // advance past expiry
	if _, ok := s.Lookup(KindNet, "api.test", 1); ok {
		t.Fatal("ttl grant should be dead after expiry")
	}
	set(1_000_000) // rewind for remaining checks

	// Revoke by ID
	must(t, s.Revoke("g2"))
	if acc, _ := s.Lookup(KindFS, "data/file.txt", 1); acc != 1 {
		t.Fatalf("after revoking g2, only g1(Read) remains: acc=%d", acc)
	}

	// RevokeMatching: a query under a broad grant revokes the broad grant
	must(t, s.Put(Grant{ID: "g4", CellID: "c", Kind: KindFS, Subject: "logs", Access: 2, Scope: ScopePermanent, CreatedAt: nowFn()}))
	n, err := s.RevokeMatching(KindFS, "logs/today.txt")
	must(t, err)
	if n != 1 {
		t.Fatalf("RevokeMatching(logs/today.txt) revoked %d want 1", n)
	}
	if _, ok := s.Lookup(KindFS, "logs/today.txt", 1); ok {
		t.Fatal("logs grant should be gone after RevokeMatching")
	}

	// Active excludes revoked/expired
	act, err := s.Active("c", KindFS)
	must(t, err)
	if len(act) != 1 || act[0].ID != "g1" {
		t.Fatalf("Active fs = %+v, want only g1", act)
	}
}

func TestMemStoreContract(t *testing.T)    { storeContract(t, func() GrantStore { return NewMemStore() }) }
func TestSQLiteStoreContract(t *testing.T) { storeContract(t, func() GrantStore { return openTestDB(t) }) }

// TestSQLiteDurability proves a grant survives a close+reopen of the DB file.
func TestSQLiteDurability(t *testing.T) {
	path := t.TempDir() + "/grants.db"
	s1, err := OpenSQLiteStore(path)
	must(t, err)
	must(t, s1.Put(Grant{ID: "d1", CellID: "c", Kind: KindFS, Subject: "secret", Access: 1, Scope: ScopePermanent, CreatedAt: nowFn()}))
	must(t, s1.Close())

	s2, err := OpenSQLiteStore(path) // fresh handle, simulates remount/host restart
	must(t, err)
	defer s2.Close()
	if acc, ok := s2.Lookup(KindFS, "secret/x", 1); !ok || acc != 1 {
		t.Fatalf("grant did not survive reopen: acc=%d ok=%v", acc, ok)
	}
}

// TestBrokerGrantPersistReload is the heart of slice 1: deny -> approve ->
// persist -> a fresh broker over the same store covers it with NO approver call.
func TestBrokerGrantPersistReload(t *testing.T) {
	set, restore := fakeClock(t, 5_000_000)
	defer restore()
	_ = set
	store := NewMemStore()
	var audited []Grant
	ap := &countApprover{dec: Decision{Access: 1, Scope: ScopePermanent}}
	b1 := &Broker{Store: store, Approver: ap, Timeout: time.Second, CellID: "c", GrantedBy: "cli", Audit: func(g Grant) { audited = append(audited, g) }}

	if acc := b1.Decide(KindFS, "secret/x.txt", 1); acc != 1 {
		t.Fatalf("first decide should grant Read, got %d", acc)
	}
	if ap.calls.Load() != 1 {
		t.Fatalf("approver should be asked once, got %d", ap.calls.Load())
	}
	if len(audited) != 1 || audited[0].Subject != "secret/x.txt" {
		t.Fatalf("audit not fired correctly: %+v", audited)
	}

	// Fresh broker over the SAME store (simulates remount): no approver call.
	b2 := &Broker{Store: store, Approver: ap, Timeout: time.Second, CellID: "c"}
	if acc := b2.Decide(KindFS, "secret/x.txt", 1); acc != 1 {
		t.Fatalf("reload decide should grant from store, got %d", acc)
	}
	if ap.calls.Load() != 1 {
		t.Fatalf("approver must NOT be re-asked after persist, calls=%d", ap.calls.Load())
	}
}

func TestBrokerFailClosed(t *testing.T) {
	store := NewMemStore()
	ap := &countApprover{dec: Decision{Access: 1, Scope: ScopePermanent}, block: 200 * time.Millisecond}
	b := &Broker{Store: store, Approver: ap, Timeout: 30 * time.Millisecond, CellID: "c"}
	if acc := b.Decide(KindNet, "evil.test", 1); acc != 0 {
		t.Fatalf("slow approver must fail closed (deny), got %d", acc)
	}
	if act, _ := store.Active("c", KindNet); len(act) != 0 {
		t.Fatal("timed-out request must not persist a grant")
	}
}

func TestBrokerOnceNotPersisted(t *testing.T) {
	store := NewMemStore()
	ap := &countApprover{dec: Decision{Access: 1, Scope: ScopeOnce}}
	b := &Broker{Store: store, Approver: ap, Timeout: time.Second, CellID: "c"}
	if acc := b.Decide(KindFS, "tmp/once", 1); acc != 1 {
		t.Fatalf("once grant should allow this miss, got %d", acc)
	}
	if act, _ := store.Active("c", KindFS); len(act) != 0 {
		t.Fatal("once scope must not persist")
	}
}

func TestBrokerDenied(t *testing.T) {
	b := &Broker{Store: NewMemStore(), Approver: &countApprover{dec: Decision{Access: 0}}, Timeout: time.Second, CellID: "c"}
	if acc := b.Decide(KindFS, "secret", 1); acc != 0 {
		t.Fatalf("explicit deny should return 0, got %d", acc)
	}
}

func openTestDB(t *testing.T) GrantStore {
	t.Helper()
	s, err := OpenSQLiteStore(t.TempDir() + "/grants.db")
	must(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
