package main

// cmd_secret.go — implements `projx-engine secret` subcommands.
//
// secret set <CODENAME>  — read plaintext from stdin, seal, persist. Print only
//                          "secret <CODENAME> set". NEVER echo the value.
// secret ls              — print codenames (sorted, one per line). Never values.
// secret rm <CODENAME>   — delete sealed entry.
//
// There is deliberately NO `secret get` that prints plaintext.

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/secrets"
)

func runSecretCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: secret <set <CODENAME> | ls | rm <CODENAME>>")
		os.Exit(1)
	}

	switch args[0] {
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: secret set <CODENAME>")
			os.Exit(1)
		}
		codename := args[1]

		// Read value from stdin. Works for piped input and terminal input.
		// ReadString stops at the first '\n'; TrimRight strips the trailing
		// newline (or \r\n on Windows). If there is no newline (echo -n),
		// ReadString returns the value with err=io.EOF — still correct.
		reader := bufio.NewReader(os.Stdin)
		value, _ := reader.ReadString('\n')
		value = strings.TrimRight(value, "\r\n")

		st, err := secrets.Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: secrets: %v\n", err)
			os.Exit(1)
		}
		if err := st.Set(codename, value); err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("secret %s set\n", codename)

	case "ls":
		st, err := secrets.Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: secrets: %v\n", err)
			os.Exit(1)
		}
		for _, name := range st.Names() {
			fmt.Println(name)
		}

	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: secret rm <CODENAME>")
			os.Exit(1)
		}
		st, err := secrets.Open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: secrets: %v\n", err)
			os.Exit(1)
		}
		if err := st.Delete(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "projx-engine: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("secret %s removed\n", args[1])

	default:
		fmt.Fprintf(os.Stderr, "projx-engine: unknown secret subcommand %q\n", args[0])
		os.Exit(1)
	}
}
