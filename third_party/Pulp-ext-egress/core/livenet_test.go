package core

import "testing"

// TestPolicyLiveGrantAndRevoke is the Slice-2 proof for the network cage: a name
// that matches neither the static AllowNames nor the Live set is refused; the
// OnConnectMiss broker hook can grant it live; the grant is recorded in Live so
// later checks bypass the broker; and Revoke removes it (the "remove" half).
// Platform-neutral — the gateway's Decide chokepoint is exercised without a netns.
func TestPolicyLiveGrantAndRevoke(t *testing.T) {
	live := NewLiveNames()
	calls := 0
	p := Policy{
		AllowNames: []string{"static.test", ".sub.test"},
		Live:       live,
		OnConnectMiss: func(name string) bool {
			calls++
			return name == "api.example.com" // broker grants only this host
		},
	}

	// Static list still works (and never consults the broker).
	if !p.Decide("static.test") {
		t.Fatal("static name should be allowed")
	}
	if !p.Decide("a.sub.test") {
		t.Fatal("suffix entry should allow subdomain")
	}
	if calls != 0 {
		t.Fatalf("broker consulted for statically-allowed names: %d", calls)
	}

	// Unknown name: broker is asked and denies.
	if p.Decide("evil.test") {
		t.Fatal("evil.test must be denied")
	}
	if calls != 1 {
		t.Fatalf("broker should have been asked once for the miss, got %d", calls)
	}

	// Granted name: broker allows, gateway records it in Live.
	if !p.Decide("api.example.com") {
		t.Fatal("api.example.com should be granted live")
	}
	if !p.MatchName("api.example.com") {
		t.Fatal("live grant should be visible to MatchName")
	}

	// Second check is covered by Live — broker NOT re-consulted.
	before := calls
	if !p.Decide("api.example.com") {
		t.Fatal("name should remain allowed after live grant")
	}
	if calls != before {
		t.Fatalf("broker re-consulted after live grant: %d -> %d", before, calls)
	}

	// REVOKE: removing the live grant denies the name again.
	live.Revoke("api.example.com")
	if p.MatchName("api.example.com") {
		t.Fatal("revoke should remove the live grant from MatchName")
	}
	// With the broker still granting it, Decide would re-grant — simulate a
	// revoked broker decision to prove a removed name truly blocks.
	p.OnConnectMiss = func(string) bool { return false }
	if p.Decide("api.example.com") {
		t.Fatal("after revoke + broker-deny, the name must be blocked")
	}
}
