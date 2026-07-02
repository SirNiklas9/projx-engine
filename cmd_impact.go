package main

// cmd_impact.go — `projx-engine impact <symbol> [--depth N]`: prints the blast radius
// (who calls this, transitively) so a change's fallout is visible before you make it.

import (
	"fmt"
	"os"
	"strconv"
)

func runImpactCmd(absRoot string, args []string) {
	var target string
	depth := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--depth":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					depth = n
				}
				i++
			}
		default:
			if target == "" {
				target = args[i]
			}
		}
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: projx-engine impact <symbol> [--depth N]")
		os.Exit(1)
	}

	st := openStore(absRoot)
	defer st.Close()
	hits, truncated := computeImpact(st, target, depth)
	if len(hits) == 0 {
		fmt.Printf("impact %s: no callers found in the indexed code-map (run `map sync` if this is stale)\n", target)
		return
	}
	fmt.Printf("impact %s: %d symbol(s) reached\n", target, len(hits))
	for _, h := range hits {
		fmt.Printf("  [depth %d] %s\t%s\n", h.Depth, h.Name, h.Anchor)
	}
	if truncated {
		fmt.Fprintf(os.Stderr, "impact: truncated at %d results — this symbol has a very wide blast radius\n", impactMaxResults)
	}
}
