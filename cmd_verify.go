package main

import (
	"fmt"
	"os"

	core "github.com/SirNiklas9/projx-core"
	verify "github.com/SirNiklas9/projx-verify"
)

func runVerifyCmd(absRoot string, _ []string) {
	st := openStore(absRoot)
	defer st.Close()

	proj, warns, err := core.ParseDir(absRoot)
	if err != nil {
		die("parse: %v", err)
	}
	for _, w := range warns {
		fmt.Printf("warning: %s\n", w)
	}

	rules := verify.RulesFromStore(st)
	violations := verify.Check(rules, proj)
	if len(violations) == 0 {
		fmt.Println("no violations")
		return
	}
	for _, v := range violations {
		fmt.Printf("violation: %s -> %s  [rule: %s->%s note: %s]\n",
			v.Edge.From, v.Edge.To, v.Rule.From, v.Rule.To, v.Rule.Note)
	}
	os.Exit(1)
}
