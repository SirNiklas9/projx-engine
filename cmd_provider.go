package main

import (
	"fmt"
	"sort"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

const providerEnabledSetting = "setting/provider-enabled/"

func runProviderCmd(absRoot string, args []string) {
	if len(args) == 1 && args[0] == "show" {
		st := openStore(absRoot)
		defer st.Close()
		var names []string
		values := map[string]string{}
		for _, r := range st.physicalFor(store.ScopeGlobal).List(store.OfKind(store.KRoute)) {
			if strings.HasPrefix(r.Key, providerEnabledSetting) {
				name := strings.TrimPrefix(r.Key, providerEnabledSetting)
				names = append(names, name)
				values[name] = r.Body
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println("providers: no global gates configured")
			return
		}
		fmt.Println("global provider gates:")
		for _, name := range names {
			state := "enabled"
			if strings.EqualFold(strings.TrimSpace(values[name]), "false") {
				state = "disabled"
			}
			fmt.Printf("  %s: %s\n", name, state)
		}
		return
	}
	if len(args) != 2 || (args[0] != "enable" && args[0] != "disable") {
		die("provider: usage: provider enable|disable <name> | provider show")
	}
	name := strings.ToLower(strings.TrimSpace(args[1]))
	if name == "" {
		die("provider: provider name is required")
	}
	st := openStore(absRoot)
	defer st.Close()
	value := "true"
	if args[0] == "disable" {
		value = "false"
	}
	rec := store.Record{
		ID:    providerEnabledSetting + name,
		Key:   providerEnabledSetting + name,
		Kind:  store.KRoute,
		Scope: store.ScopeGlobal,
		Body:  value,
	}
	if err := st.physicalFor(store.ScopeGlobal).Put(rec); err != nil {
		die("provider %s %s: %v", args[0], name, err)
	}
	fmt.Printf("provider %s globally %sd\n", name, args[0])
}
