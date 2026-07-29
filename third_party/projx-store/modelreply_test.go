package store

import "testing"

func TestParseTierReply(t *testing.T) {
	cases := []struct {
		in       string
		wantTier string
		wantConf bool
	}{
		{`{"tier":"deep-reasoning","confident":true}`, "deep-reasoning", true},
		{`{"tier":"default","confident":false}`, "default", false},
		{"prose\n{\"tier\":\"cheap-fast\",\"confident\":true}\nmore", "cheap-fast", true},
		{`{"tier":"default"}`, "default", true}, // confident absent → true
		{`{"tier":"medium","confident":true}`, "", false},
		{"I'd say deep-reasoning here", "deep-reasoning", false}, // bare word → not confident
		{"no idea", "", false},
	}
	for _, c := range cases {
		if tier, conf := ParseTierReply(c.in); tier != c.wantTier || conf != c.wantConf {
			t.Errorf("ParseTierReply(%q) = %q/%v, want %q/%v", c.in, tier, conf, c.wantTier, c.wantConf)
		}
	}
}

func TestParseSelectedKeys(t *testing.T) {
	cands := []string{"auth/login", "billing/checkout", "infra/db"}
	cases := []struct {
		in   string
		want []string
	}{
		{`["billing/checkout","auth/login"]`, []string{"billing/checkout", "auth/login"}},
		{"Relevant:\n[\"infra/db\"]\n", []string{"infra/db"}},
		{`["billing/checkout","made/up"]`, []string{"billing/checkout"}}, // invented dropped
		{`[]`, nil},
		{`none`, nil},
	}
	for _, c := range cases {
		got := ParseSelectedKeys(c.in, cands)
		if len(got) != len(c.want) {
			t.Errorf("ParseSelectedKeys(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseSelectedKeys(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
