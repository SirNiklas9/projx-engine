package main

// cmd_route.go — the DECIDER's control surface (`route`).
//
// `run` USES the decider to route a task; `route` lets you INSPECT a decision and
// manage the standing routing settings the decider reads. Settings are ordinary store
// records (KRoute, `setting/` key prefix) so they travel with the project, are
// journaled, and stay out of context injection.
//
//   route <task>            print the tier decision (class + source + cmd) — no execution
//   route pin <tier>        hard-lock every task to <tier> (cheap-fast|default|deep-reasoning)
//   route floor <tier>      set a MINIMUM tier (decider may go above, never below)
//   route clear pin|floor   remove a standing setting
//   route show              print the current pin / floor / keyword signals

import (
	"fmt"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// tierAliases maps friendly names to the canonical capability classes so the user can
// say `route pin opus` or `route pin deep-reasoning`.
var tierAliases = map[string]string{
	"cheap-fast": "cheap-fast", "cheap": "cheap-fast", "haiku": "cheap-fast",
	"default": "default", "standard": "default", "sonnet": "default",
	"deep-reasoning": "deep-reasoning", "deep": "deep-reasoning", "opus": "deep-reasoning",
}

func runRouteCmd(absRoot string, args []string) {
	if len(args) == 0 {
		die("route: usage: route <task> | route pin <tier> | route floor <tier> | route clear pin|floor | route show")
	}
	switch args[0] {
	case "pin":
		routeSetTier(absRoot, store.SettingRoutePin, "pin", args[1:])
	case "floor":
		routeSetTier(absRoot, store.SettingRouteFloor, "floor", args[1:])
	case "clear":
		routeClear(absRoot, args[1:])
	case "show":
		routeShow(absRoot)
	default:
		routeDecide(absRoot, strings.Join(args, " "))
	}
}

// canonTier resolves a user-supplied tier name to a canonical class, or "" if unknown.
func canonTier(s string) string { return tierAliases[strings.ToLower(strings.TrimSpace(s))] }

// routeSetTier writes a pin/floor setting record.
func routeSetTier(absRoot, key, label string, args []string) {
	if len(args) == 0 {
		die("route %s: need a tier (cheap-fast|default|deep-reasoning, or haiku|sonnet|opus)", label)
	}
	tier := canonTier(args[0])
	if tier == "" {
		die("route %s: unknown tier %q", label, args[0])
	}
	st := openStore(absRoot)
	defer st.Close()
	if err := st.Put(store.Record{ID: key, Kind: store.KRoute, Scope: store.ScopeProject, Key: key, Body: tier}); err != nil {
		die("route %s: %v", label, err)
	}
	fmt.Printf("route %s set to %s\n", label, tier)
}

// routeClear removes a pin or floor setting.
func routeClear(absRoot string, args []string) {
	if len(args) == 0 {
		die("route clear: clear what? (pin | floor)")
	}
	var key, label string
	switch args[0] {
	case "pin":
		key, label = store.SettingRoutePin, "pin"
	case "floor":
		key, label = store.SettingRouteFloor, "floor"
	default:
		die("route clear: unknown setting %q (want pin | floor)", args[0])
	}
	st := openStore(absRoot)
	defer st.Close()
	if err := st.Delete(key); err != nil {
		die("route clear %s: %v", label, err)
	}
	fmt.Printf("route %s cleared\n", label)
}

// routeShow prints the standing routing settings the decider reads.
func routeShow(absRoot string) {
	st := openStore(absRoot)
	defer st.Close()
	get := func(key string) string {
		if r, ok := st.Get(key); ok {
			return strings.TrimSpace(r.Body)
		}
		return "(none)"
	}
	fmt.Printf("route settings:\n")
	fmt.Printf("  pin:   %s\n", get(store.SettingRoutePin))
	fmt.Printf("  floor: %s\n", get(store.SettingRouteFloor))
	for _, class := range []string{"cheap-fast", "default", "deep-reasoning"} {
		if r, ok := st.Get("setting/route-keywords/" + class); ok && strings.TrimSpace(r.Body) != "" {
			fmt.Printf("  +keywords[%s]: %s\n", class, strings.TrimSpace(r.Body))
		}
	}
}

// routeDecide prints the decider's tier choice for a task without executing anything.
func routeDecide(absRoot, task string) {
	st := openStore(absRoot)
	defer st.Close()
	d := store.RouteDecide(st, task, nil) // triage seam nil — deterministic decision
	fmt.Printf("route decision:\n")
	fmt.Printf("  class:  %s\n", d.Class)
	fmt.Printf("  source: %s\n", d.Source)
	cmd := d.Cmd
	if cmd == "" {
		cmd = "(use PROJX_AGENT_CMD or claude)"
	}
	fmt.Printf("  cmd:    %s\n", cmd)
	fmt.Printf("  reason: %s\n", d.Reason)
}
