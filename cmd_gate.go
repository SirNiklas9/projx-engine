package main

import (
	"fmt"
	"os"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

func runGateCmd(absRoot string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gate <add|list|rm>")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		gateAdd(absRoot, rest)
	case "list":
		gateList(absRoot, rest)
	case "rm":
		gateRm(absRoot, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown gate subcommand %q (add|list|rm)\n", sub)
		os.Exit(1)
	}
}

func gateAdd(absRoot string, args []string) {
	if len(args) == 0 {
		die("usage: gate add <pattern>")
	}
	pattern := strings.Join(args, " ")
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		die("pattern must not be empty")
	}

	id := "gate-rule/" + slug(pattern)
	st := openStore(absRoot)
	defer st.Close()

	var bp *store.Record
	if before, had := st.Get(id); had {
		bp = &before
	}

	rec := store.Record{ID: id, Kind: store.KGateRule, Scope: store.ScopeProject, Key: pattern, Body: pattern}
	if err := st.Put(rec); err != nil {
		die("put: %v", err)
	}
	recordStoreOp(absRoot, "put", "ui", bp, &rec)
	fmt.Println("added gate rule", id)
}

func gateList(absRoot string, _ []string) {
	st := openStore(absRoot)
	defer st.Close()
	rules := st.List(store.OfKind(store.KGateRule))
	if len(rules) == 0 {
		fmt.Println("(no gate rules)")
		return
	}
	for _, r := range rules {
		fmt.Printf("%s\t%s\n", r.ID, r.Body)
	}
}

func gateRm(absRoot string, args []string) {
	if len(args) == 0 {
		die("usage: gate rm <id-or-pattern>")
	}
	idOrPattern := args[0]

	st := openStore(absRoot)
	defer st.Close()

	// Try as direct id first.
	before, had := st.Get(idOrPattern)
	if !had {
		// Try as pattern: derive the id via slug.
		derivedID := "gate-rule/" + slug(idOrPattern)
		before, had = st.Get(derivedID)
		if !had {
			fmt.Fprintf(os.Stderr, "gate rule not found: %s\n", idOrPattern)
			os.Exit(1)
		}
		idOrPattern = derivedID
	}

	if err := st.Delete(idOrPattern); err != nil {
		die("delete: %v", err)
	}
	recordStoreOp(absRoot, "delete", "ui", &before, nil)
	fmt.Println("removed gate rule", idOrPattern)
}
