package store

import "testing"

// seedTierMap puts the floor KRoute tier-map records so routeCmd can resolve a cmd.
func seedTierMap(t *testing.T, m Store) {
	t.Helper()
	for _, r := range FloorRoutes {
		if err := m.Put(Record{ID: "route/" + r.Key, Kind: KRoute, Scope: ScopeProject, Key: r.Key, Body: r.Body}); err != nil {
			t.Fatal(err)
		}
	}
}

func setSetting(t *testing.T, m Store, key, body string) {
	t.Helper()
	if err := m.Put(Record{ID: key, Kind: KRoute, Scope: ScopeProject, Key: key, Body: body}); err != nil {
		t.Fatal(err)
	}
}

// failTriage fails the test if the decider ever calls triage when it shouldn't.
func failTriage(t *testing.T) TriageFunc {
	return func(task string) (string, bool) {
		t.Errorf("triage called when it should not have been (task=%q)", task)
		return "", false
	}
}

func TestRouteDecideLadder(t *testing.T) {
	t.Run("keyword match routes free, no triage", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		d := RouteDecide(m, "please redesign the auth module", failTriage(t))
		if d.Class != "deep-reasoning" || d.Source != "keyword" {
			t.Fatalf("got %+v, want deep-reasoning/keyword", d)
		}
		if d.Cmd == "" {
			t.Error("expected a resolved launch cmd from the tier map")
		}
	})

	t.Run("ambiguous calls triage", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		called := false
		d := RouteDecide(m, "handle the widget thing", func(string) (string, bool) {
			called = true
			return "default", true
		})
		if !called {
			t.Error("triage was not called for an ambiguous task")
		}
		if d.Class != "default" || d.Source != "triage" {
			t.Fatalf("got %+v, want default/triage", d)
		}
	})

	t.Run("triage uncertain escalates up a tier", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		d := RouteDecide(m, "handle the widget thing", func(string) (string, bool) {
			return "cheap-fast", false // unsure
		})
		if d.Class != "default" || d.Source != "triage-escalated" {
			t.Fatalf("got %+v, want default/triage-escalated (cheap-fast escalated)", d)
		}
	})

	t.Run("ambiguous with nil triage falls to default", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		d := RouteDecide(m, "handle the widget thing", nil)
		if d.Class != "default" || d.Source != "default" {
			t.Fatalf("got %+v, want default/default", d)
		}
	})

	t.Run("pin hard-locks and skips triage", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		setSetting(t, m, SettingRoutePin, "deep-reasoning")
		// Even a cheap-looking task + a triage that would object is overridden.
		d := RouteDecide(m, "rename a variable", failTriage(t))
		if d.Class != "deep-reasoning" || d.Source != "pin" {
			t.Fatalf("got %+v, want deep-reasoning/pin", d)
		}
	})

	t.Run("explicit @-override beats pin and floor", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		setSetting(t, m, SettingRoutePin, "deep-reasoning")
		setSetting(t, m, SettingRouteFloor, "default")
		d := RouteDecide(m, "@cheap just rename this", failTriage(t))
		if d.Class != "cheap-fast" || d.Source != "override" {
			t.Fatalf("got %+v, want cheap-fast/override (override wins over pin+floor)", d)
		}
	})

	t.Run("floor raises a cheap keyword route", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		setSetting(t, m, SettingRouteFloor, "default")
		d := RouteDecide(m, "rename the symbol", failTriage(t)) // cheap-fast keyword
		if d.Class != "default" || d.Source != "keyword+floor" {
			t.Fatalf("got %+v, want default/keyword+floor", d)
		}
	})

	t.Run("editable classifier: store keyword adds a signal", func(t *testing.T) {
		m := NewMem()
		seedTierMap(t, m)
		setSetting(t, m, settingRouteKeywords+"/deep-reasoning", "migration, schema")
		d := RouteDecide(m, "run the migration now", failTriage(t))
		if d.Class != "deep-reasoning" || d.Source != "keyword" {
			t.Fatalf("got %+v, want deep-reasoning/keyword from store signal", d)
		}
	})
}

// TestClassifyConfident covers the matched flag the decider keys on.
func TestClassifyConfident(t *testing.T) {
	if c, ok := ClassifyConfident("redesign this"); c != "deep-reasoning" || !ok {
		t.Errorf("deep keyword: got %s/%v", c, ok)
	}
	// A routine refactor is standard coding → default (sonnet), and a CONFIDENT match
	// (std keyword) so the decider takes it for free without escalating via triage.
	if c, ok := ClassifyConfident("refactor this"); c != "default" || !ok {
		t.Errorf("refactor should be default/true (confident), got %s/%v", c, ok)
	}
	if c, ok := ClassifyConfident("rename x"); c != "cheap-fast" || !ok {
		t.Errorf("cheap keyword: got %s/%v", c, ok)
	}
	if c, ok := ClassifyConfident("do the widget thing"); c != "default" || ok {
		t.Errorf("no signal: got %s/%v, want default/false", c, ok)
	}
}
