package core

import "testing"

func TestMatchName(t *testing.T) {
	tests := []struct {
		name      string
		allowList []string
		input     string
		want      bool
	}{
		{
			name:      "exact match works",
			allowList: []string{"api.example.com"},
			input:     "api.example.com",
			want:      true,
		},
		{
			name:      "case-insensitive exact match",
			allowList: []string{"API.Example.COM"},
			input:     "api.example.com",
			want:      true,
		},
		{
			name:      "trailing dot stripped",
			allowList: []string{"api.example.com"},
			input:     "api.example.com.",
			want:      true,
		},
		{
			name:      "suffix prefix matches bare domain",
			allowList: []string{".foo.com"},
			input:     "foo.com",
			want:      true,
		},
		{
			name:      "suffix prefix matches subdomain",
			allowList: []string{".foo.com"},
			input:     "sub.foo.com",
			want:      true,
		},
		{
			name:      "suffix prefix does not match unrelated domain",
			allowList: []string{".foo.com"},
			input:     "otherfoo.com",
			want:      false,
		},
		{
			name:      "unlisted name is blocked",
			allowList: []string{"allowed.test"},
			input:     "blocked.test",
			want:      false,
		},
		{
			name:      "empty policy blocks everything",
			allowList: []string{},
			input:     "anything.com",
			want:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Policy{AllowNames: tc.allowList}
			got := p.MatchName(tc.input)
			if got != tc.want {
				t.Errorf("MatchName(%q, allowList=%v) = %v; want %v", tc.input, tc.allowList, got, tc.want)
			}
		})
	}
}
