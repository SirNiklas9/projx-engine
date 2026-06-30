package main

import (
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestParseTaskFlag covers the --task extraction used by the UserPromptSubmit hook.
func TestParseTaskFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"absent", []string{}, ""},
		{"space form", []string{"--task", "fix the login"}, "fix the login"},
		{"equals form", []string{"--task=fix the login"}, "fix the login"},
		{"among others", []string{"-x", "--task", "y", "z"}, "y"},
		{"trailing --task with no value", []string{"--task"}, ""},
	}
	for _, c := range cases {
		if got := parseTaskFlag(c.args); got != c.want {
			t.Errorf("%s: parseTaskFlag(%v) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// TestCompileStorePreambleForTaskSlices proves the engine delegate slices: law
// always present, the relevant doc in, the canary out, and sliced < full.
func TestCompileStorePreambleForTaskSlices(t *testing.T) {
	m := store.NewMem()
	put := func(id string, k store.Kind, key, body string) {
		if err := m.Put(store.Record{ID: id, Kind: k, Scope: store.ScopeProject, Key: key, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	put("gate-rule/secrets", store.KGateRule, "secrets", "secret/**")
	put("convention/naming", store.KConvention, "naming", "use camelCase")
	put("doc/mc-login", store.KDoc, "minecraft/login/backend", "JWT auth in internal/auth/login.go")
	put("doc/canary", store.KDoc, "canary/up", "Up has balloons")

	sliced := compileStorePreambleForTask(m, "look at the minecraft login backend")
	full := compileStorePreamble(m)

	if !strings.Contains(sliced, "secret/**") || !strings.Contains(sliced, "use camelCase") {
		t.Error("sliced contract dropped the law")
	}
	if !strings.Contains(sliced, "minecraft/login/backend") {
		t.Error("sliced contract missing the relevant doc")
	}
	if strings.Contains(sliced, "balloons") {
		t.Error("sliced contract leaked the canary")
	}
	if len(sliced) >= len(full) {
		t.Errorf("sliced (%d bytes) should be < full (%d bytes)", len(sliced), len(full))
	}
}
