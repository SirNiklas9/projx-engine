package store

import (
	"reflect"
	"testing"
)

func TestDecompose(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single task untouched", "rename the config var", []string{"rename the config var"}},
		{
			"prose 'then' chain is ONE task (no connector split)",
			"we need to rename this, and then we need to design the cache, then refactor the auth module",
			[]string{"we need to rename this, and then we need to design the cache, then refactor the auth module"},
		},
		{"numbered list", "1. rename foo\n2) add tests\n3. design the api", []string{"rename foo", "add tests", "design the api"}},
		{"prose semicolons stay ONE task", "fix the typo; write the test; document it", []string{"fix the typo; write the test; document it"}},
		{"bullets", "- rename foo\n- refactor bar", []string{"rename foo", "refactor bar"}},
		{"empty", "   ", nil},
		{"no over-split on bare comma", "rename foo, bar, and baz", []string{"rename foo, bar, and baz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decompose(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Decompose(%q)\n  got  %#v\n  want %#v", c.in, got, c.want)
			}
		})
	}
}
