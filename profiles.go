package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// A Profile is a bundle of declared records that pre-fills a fresh project's
// store so nobody starts from zero. Facets map onto the cage + orchestration:
// Conventions -> verbatim steering in the agent preamble; Gates -> KGateRule
// (gate-deny + cage); ModelTiers -> routing.json (which model per task class);
// NetAllow/Tools -> cage.json (the cage's egress allowlist + exec jail).
//
// Profiles are agent-agnostic in intent: ModelTiers cmds are the claude
// rendering of the abstract tiers (cheap/standard/deep); a per-agent adapter
// re-renders them for Codex/etc.
type Profile struct {
	Name        string
	Desc        string
	Conventions []profileRec
	Gates       []profileRec
	ModelTiers  map[string]string // routing class -> agent launch cmd
	NetAllow    []string          // hosts the cage permits by default
	Tools       []string          // binaries the exec jail exposes
}

type profileRec struct{ Key, Body string }

// floor is the universal base applied to EVERY project.
var floor = Profile{
	Name: "floor",
	Desc: "universal base — applied to every project",
	Conventions: []profileRec{
		{"read before acting", "Read this store contract first. The store is authoritative project knowledge — not any README or .md file. Never act before reading it."},
		{"commit what you learn", "When you decide or learn something durable, commit it to the store (convention/adr) — not a markdown file."},
		{"deterministic first", "Prefer deterministic ops (verify, store, tests) over agent reasoning whenever a tool can do the job."},
		{"secrets by codename", "Never read, edit, or print secret material. Reference secrets only by codename."},
	},
	Gates: []profileRec{
		{"secrets dir", "secret/**"},
		{"dotenv files", ".env*"},
		{"private keys", "**/*.key"},
		{"ssh material", "**/.ssh/**"},
	},
	ModelTiers: map[string]string{
		"cheap-fast":     "claude --model claude-haiku-4-5-20251001", // mechanical: moves, grep, format, classify
		"default":        "claude --model claude-sonnet-4-6",         // standard: code, tests, review
		"deep-reasoning": "claude --model claude-opus-4-8",           // hard: architecture, multi-file, debugging
	},
	NetAllow: []string{"api.anthropic.com", ".anthropic.com"},
	Tools:    []string{"git"},
}

// stacks layer on top of the floor (opt-in by name).
var stacks = map[string]Profile{
	"go": {
		Name: "go", Desc: "Go projects",
		Conventions: []profileRec{
			{"go build hygiene", "Go: GOWORK=off with go.mod path replaces (never go.work). Run `go test ./...` and `gofmt -l` before declaring done."},
		},
		NetAllow: []string{"proxy.golang.org", "sum.golang.org", "github.com", ".github.com"},
		Tools:    []string{"go", "gofmt", "git"},
	},
	"node": {
		Name: "node", Desc: "Node / TypeScript projects",
		Conventions: []profileRec{
			{"node package manager", "Node: use the project's package manager (pnpm if pnpm-lock.yaml is present, else npm). Run the test script before declaring done."},
		},
		NetAllow: []string{"registry.npmjs.org"},
		Tools:    []string{"node", "npm", "pnpm", "git"},
	},
	"python": {
		Name: "python", Desc: "Python projects",
		Conventions: []profileRec{
			{"python toolchain", "Python: prefer uv (or the project's venv). Run the test suite (pytest/unittest) before declaring done."},
		},
		NetAllow: []string{"pypi.org", "files.pythonhosted.org"},
		Tools:    []string{"python", "python3", "uv", "pip", "git"},
	},
}

// profileNames returns the available stack names, sorted, for error messages.
func profileNames() string {
	names := make([]string, 0, len(stacks))
	for n := range stacks {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Seed writes the floor profile plus any named stacks into the store and the
// project's .projx config. Returns the number of records written. The floor is
// always applied; names select additional stacks.
func Seed(st store.Store, root string, names []string) (int, error) {
	picked := []Profile{floor}
	seen := map[string]bool{"floor": true}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			continue
		}
		p, ok := stacks[n]
		if !ok {
			return 0, fmt.Errorf("unknown profile %q (available: floor, %s)", n, profileNames())
		}
		picked = append(picked, p)
		seen[n] = true
	}

	n := 0
	tiers := map[string]string{}
	netSet := map[string]bool{}
	var netAllow, tools []string
	toolSet := map[string]bool{}

	for _, p := range picked {
		for _, c := range p.Conventions {
			if err := st.Put(seedRecord(store.KConvention, c, p.Name)); err != nil {
				return n, err
			}
			n++
		}
		for _, g := range p.Gates {
			if err := st.Put(seedRecord(store.KGateRule, g, p.Name)); err != nil {
				return n, err
			}
			n++
		}
		for k, v := range p.ModelTiers {
			tiers[k] = v
		}
		for _, h := range p.NetAllow {
			if !netSet[h] {
				netSet[h] = true
				netAllow = append(netAllow, h)
			}
		}
		for _, t := range p.Tools {
			if !toolSet[t] {
				toolSet[t] = true
				tools = append(tools, t)
			}
		}
	}

	if err := writeRoutingJSON(root, tiers); err != nil {
		return n, err
	}
	if err := writeCageJSON(root, netAllow, tools); err != nil {
		return n, err
	}
	return n, nil
}

func seedRecord(kind store.Kind, r profileRec, profile string) store.Record {
	return store.Record{
		ID:     kind.String() + "/" + seedSlug(r.Key),
		Kind:   kind,
		Scope:  store.ScopeProject,
		Key:    r.Key,
		Body:   r.Body,
		Origin: "seed:" + profile,
	}
}

// seedSlug normalizes a key into an id-safe token.
func seedSlug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func writeRoutingJSON(root string, tiers map[string]string) error {
	if len(tiers) == 0 {
		return nil
	}
	type provider struct {
		Class string `json:"class"`
		Cmd   string `json:"cmd"`
	}
	classes := make([]string, 0, len(tiers))
	for c := range tiers {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	provs := make([]provider, 0, len(tiers))
	for _, c := range classes {
		provs = append(provs, provider{Class: c, Cmd: tiers[c]})
	}
	return writeJSON(filepath.Join(root, ".projx", "routing.json"), map[string]any{"providers": provs})
}

func writeCageJSON(root string, netAllow, tools []string) error {
	return writeJSON(filepath.Join(root, ".projx", "cage.json"), map[string]any{
		"netAllow": netAllow,
		"tools":    tools,
	})
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// storeSeed is the `store seed [profile...]` CLI: applies the floor plus any
// named stacks (e.g. `store seed go node`) to a fresh project store + config.
func storeSeed(absRoot string, args []string) {
	st := openStore(absRoot)
	n, err := Seed(st, absRoot, args)
	if err != nil {
		die("seed: %v", err)
	}
	applied := "floor"
	if len(args) > 0 {
		applied = "floor + " + strings.Join(args, ", ")
	}
	fmt.Printf("seeded %d record(s) [%s] into %s\n", n, applied, filepath.Join(absRoot, ".projx"))
}
