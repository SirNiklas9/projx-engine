package main

// cmd_route.go — the DECIDER's control surface (`route`).
//
// `run` USES the decider to route a task; `route` lets you INSPECT a decision and
// manage the standing routing settings the decider reads. Settings are ordinary store
// records (KRoute, `setting/` key prefix) so they travel with the project, are
// journaled, and stay out of context injection.
//
//   route <task>            print the tier decision (class + source + cmd) — no execution
//   route pin <tier>        hard-lock every task to <tier> (Luna -> Sol spectrum)
//   route floor <tier>      set a MINIMUM tier (decider may go above, never below)
//   route policy set <class> [--scope project|workspace|global] [--provider P] [--profile P] [--model M] [--fallback CMD] [--cross-provider-fallback]
//   route clear pin|floor   remove a standing setting
//   route clear policy <class> [provider|profile|model|fallback|all] [--scope project|workspace|global]
//   route show              print the current pin / floor / keyword / policy signals

import (
	"flag"
	"fmt"
	"strings"

	"github.com/SirNiklas9/projx-engine/internal/routing"
	store "github.com/SirNiklas9/projx-store"
)

// tierAliases maps friendly names to the canonical capability classes so the user can
// say `route pin opus` or `route pin deep-reasoning`.
var tierAliases = map[string]string{
	"cheap-fast": "cheap-fast", "cheap": "cheap-fast", "haiku": "cheap-fast",
	"default": "default", "standard": "default", "sonnet": "default",
	"deep-reasoning": "deep-reasoning", "deep": "deep-reasoning", "opus": "deep-reasoning",
	"luna": "luna", "luna-plus": "luna-plus",
	"terra": "terra", "terra-plus": "terra-plus",
	"nova": "nova", "nova-plus": "nova-plus",
	"sol": "sol",
}

func runRouteCmd(absRoot string, args []string) {
	args, jsonOut := takeJSONFlag(args)
	if len(args) == 0 {
		die("route: usage: route <task> | route pin <tier> | route floor <tier> | route policy set <class> [--scope project|workspace|global] [--provider P] [--profile P] [--model M] [--fallback CMD] | route clear pin|floor | route clear policy <class> [provider|profile|model|fallback|all] [--scope project|workspace|global] | route show")
	}
	switch args[0] {
	case "pin":
		routeSetTier(absRoot, store.SettingRoutePin, "pin", args[1:], jsonOut)
	case "floor":
		routeSetTier(absRoot, store.SettingRouteFloor, "floor", args[1:], jsonOut)
	case "policy":
		routePolicy(absRoot, args[1:], jsonOut)
	case "clear":
		routeClear(absRoot, args[1:], jsonOut)
	case "show":
		routeShow(absRoot, jsonOut)
	default:
		routeDecide(absRoot, strings.Join(args, " "), jsonOut)
	}
}

var routePolicySettingKeys = map[string]string{
	"provider":                "setting/route-provider",
	"profile":                 "setting/route-profile",
	"model":                   "setting/route-model",
	"effort":                  "setting/route-effort",
	"native-effort":           "setting/route-native-effort",
	"fallback":                "setting/route-fallback",
	"cross-provider-fallback": "setting/route-cross-provider-fallback",
}

var routePolicyClasses = []string{
	"luna", "luna-plus", "terra", "terra-plus", "nova", "nova-plus", "sol",
	"cheap-fast", "default", "deep-reasoning", "elevate", "local",
}

func routePolicy(absRoot string, args []string, jsonFlags ...bool) {
	if len(args) == 0 {
		die("route policy: usage: route policy set <class> [--scope project|workspace|global] [--provider P] [--profile P] [--model M] [--fallback CMD] [--cross-provider-fallback]")
	}
	switch args[0] {
	case "set":
		routePolicySet(absRoot, args[1:], jsonFlags...)
	default:
		die("route policy: unknown subcommand %q (want set)", args[0])
	}
}

func routePolicySet(absRoot string, args []string, jsonFlags ...bool) {
	if len(args) == 0 {
		die("route policy set: need a class")
	}
	class := canonRouteClass(args[0])
	if class == "" {
		die("route policy set: unknown class %q", args[0])
	}
	fs := flag.NewFlagSet("route policy set", flag.ExitOnError)
	scopeFlag := fs.String("scope", "project", "scope: project|workspace|global")
	provider := fs.String("provider", "", "preferred provider")
	profile := fs.String("profile", "", "preferred profile")
	model := fs.String("model", "", "preferred model")
	effort := fs.String("effort", "", "neutral effort: low|medium|high|ultra")
	nativeEffort := fs.String("native-effort", "", "provider-native effort")
	fallback := fs.String("fallback", "", "explicit fallback command")
	crossProviderFallback := fs.Bool("cross-provider-fallback", false, "allow automatic fallback to another provider when the preferred provider has no usable profile")
	_ = fs.Parse(args[1:])
	scope, err := parseScopeName(*scopeFlag)
	if err != nil {
		die("route policy set: %v", err)
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	delete(seen, "scope")
	if len(seen) == 0 {
		die("route policy set: need at least one policy field")
	}
	st := openStore(absRoot)
	defer st.Close()
	if scope == store.ScopeWorkspace && st.space == nil {
		die("route policy set: not in a workspace (no .projx-workspace ancestor)")
	}
	out := map[string]string{}
	for name, key := range routePolicySettingKeys {
		if !seen[name] {
			continue
		}
		value := strings.TrimSpace(map[string]string{
			"provider":                *provider,
			"profile":                 *profile,
			"model":                   *model,
			"effort":                  *effort,
			"native-effort":           *nativeEffort,
			"fallback":                *fallback,
			"cross-provider-fallback": fmt.Sprintf("%t", *crossProviderFallback),
		}[name])
		recKey := key + "/" + class
		if value == "" {
			if err := routeDeleteScoped(st, recKey, scope); err != nil {
				die("route policy set: clear %s: %v", name, err)
			}
			continue
		}
		if err := st.Put(store.Record{ID: recKey, Kind: store.KRoute, Scope: scope, Key: recKey, Body: value}); err != nil {
			die("route policy set: %s: %v", name, err)
		}
		out[name] = value
	}
	if optionalJSONFlag(jsonFlags) {
		writeCLIJSON(map[string]any{"ok": true, "class": class, "scope": scope.String(), "policy": out})
		return
	}
	fmt.Printf("route policy updated for %s (%s)\n", class, scope)
	for _, name := range []string{"provider", "profile", "model", "effort", "native-effort", "fallback", "cross-provider-fallback"} {
		if value, ok := out[name]; ok {
			fmt.Printf("  %s: %s\n", name, value)
		}
	}
}

func routeDeleteScoped(st *projectStore, id string, scope store.Scope) error {
	return st.physicalFor(scope).Delete(id)
}

func canonRouteClass(s string) string {
	if tier := canonTier(s); tier != "" {
		return tier
	}
	s = strings.ToLower(strings.TrimSpace(s))
	for _, class := range routePolicyClasses {
		if s == class {
			return class
		}
	}
	return ""
}

func routePolicySnapshot(st *projectStore) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, class := range routePolicyClasses {
		for field, key := range routePolicySettingKeys {
			if r, ok := st.Get(key + "/" + class); ok && strings.TrimSpace(r.Body) != "" {
				if out[class] == nil {
					out[class] = map[string]string{}
				}
				out[class][field] = strings.TrimSpace(r.Body)
			}
		}
	}
	return out
}

// canonTier resolves a user-supplied tier name to a canonical class, or "" if unknown.
func canonTier(s string) string { return tierAliases[strings.ToLower(strings.TrimSpace(s))] }

// routeSetTier writes a pin/floor setting record.
func routeSetTier(absRoot, key, label string, args []string, jsonFlags ...bool) {
	if len(args) == 0 {
		die("route %s: need a tier (luna|luna-plus|terra|terra-plus|nova|nova-plus|sol, or legacy aliases)", label)
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
	if optionalJSONFlag(jsonFlags) {
		writeCLIJSON(map[string]any{"ok": true, "setting": label, "tier": tier})
		return
	}
	fmt.Printf("route %s set to %s\n", label, tier)
}

// routeClear removes a pin or floor setting.
func routeClear(absRoot string, args []string, jsonFlags ...bool) {
	if len(args) == 0 {
		die("route clear: clear what? (pin | floor | policy)")
	}
	if args[0] == "policy" {
		routePolicyClear(absRoot, args[1:], jsonFlags...)
		return
	}
	var key, label string
	switch args[0] {
	case "pin":
		key, label = store.SettingRoutePin, "pin"
	case "floor":
		key, label = store.SettingRouteFloor, "floor"
	default:
		die("route clear: unknown setting %q (want pin | floor | policy)", args[0])
	}
	st := openStore(absRoot)
	defer st.Close()
	if err := st.physicalFor(store.ScopeProject).Delete(key); err != nil {
		die("route clear %s: %v", label, err)
	}
	if optionalJSONFlag(jsonFlags) {
		writeCLIJSON(map[string]any{"ok": true, "setting": label, "cleared": true})
		return
	}
	fmt.Printf("route %s cleared\n", label)
}

func routePolicyClear(absRoot string, args []string, jsonFlags ...bool) {
	if len(args) == 0 {
		die("route clear policy: need a class")
	}
	class := canonRouteClass(args[0])
	if class == "" {
		die("route clear policy: unknown class %q", args[0])
	}
	scopeName := "project"
	field := "all"
	for i := 1; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
			continue
		case arg == "--scope":
			if i+1 >= len(args) {
				die("route clear policy: --scope requires a value")
			}
			scopeName = args[i+1]
			i++
		case strings.HasPrefix(arg, "--scope="):
			scopeName = strings.TrimSpace(strings.TrimPrefix(arg, "--scope="))
		case strings.HasPrefix(arg, "-"):
			die("route clear policy: unknown flag %q", arg)
		case field == "all":
			field = strings.ToLower(arg)
		default:
			die("route clear policy: too many arguments")
		}
	}
	scope, err := parseScopeName(scopeName)
	if err != nil {
		die("route clear policy: %v", err)
	}
	fields := []string{}
	if field == "all" || field == "" {
		fields = []string{"provider", "profile", "model", "fallback"}
	} else if _, ok := routePolicySettingKeys[field]; ok {
		fields = []string{field}
	} else {
		die("route clear policy: unknown field %q (want provider|profile|model|fallback|all)", field)
	}
	st := openStore(absRoot)
	defer st.Close()
	if scope == store.ScopeWorkspace && st.space == nil {
		die("route clear policy: not in a workspace (no .projx-workspace ancestor)")
	}
	for _, name := range fields {
		if err := routeDeleteScoped(st, routePolicySettingKeys[name]+"/"+class, scope); err != nil {
			die("route clear policy: %s: %v", name, err)
		}
	}
	if optionalJSONFlag(jsonFlags) {
		writeCLIJSON(map[string]any{"ok": true, "class": class, "scope": scope.String(), "cleared": fields})
		return
	}
	fmt.Printf("route policy cleared for %s (%s): %s\n", class, scope, strings.Join(fields, ", "))
}

// routeShow prints the standing routing settings the decider reads.
func routeShow(absRoot string, jsonFlags ...bool) {
	st := openStore(absRoot)
	defer st.Close()
	get := func(key string) string {
		if r, ok := st.Get(key); ok {
			return strings.TrimSpace(r.Body)
		}
		return "(none)"
	}
	pin := get(store.SettingRoutePin)
	floor := get(store.SettingRouteFloor)
	keywords := map[string]string{}
	policy := routePolicySnapshot(st)
	for _, class := range []string{"cheap-fast", "default", "deep-reasoning"} {
		if r, ok := st.Get("setting/route-keywords/" + class); ok && strings.TrimSpace(r.Body) != "" {
			keywords[class] = strings.TrimSpace(r.Body)
		}
	}
	if optionalJSONFlag(jsonFlags) {
		writeCLIJSON(map[string]any{"pin": pin, "floor": floor, "keywords": keywords, "policy": policy})
		return
	}
	fmt.Printf("route settings:\n")
	fmt.Printf("  pin:   %s\n", pin)
	fmt.Printf("  floor: %s\n", floor)
	for _, class := range []string{"cheap-fast", "default", "deep-reasoning"} {
		if value := keywords[class]; value != "" {
			fmt.Printf("  +keywords[%s]: %s\n", class, value)
		}
	}
	for _, class := range routePolicyClasses {
		if fields := policy[class]; len(fields) > 0 {
			fmt.Printf("  policy[%s]:\n", class)
			for _, name := range []string{"provider", "profile", "model", "fallback"} {
				if value := fields[name]; value != "" {
					fmt.Printf("    %s: %s\n", name, value)
				}
			}
		}
	}
}

// routeDecide prints the decider's tier choice for a task without executing anything.
func routeDecide(absRoot, task string, jsonFlags ...bool) {
	st := openStore(absRoot)
	defer st.Close()
	cfg := routing.LoadConfig(absRoot)
	d := routing.DecideWithStore(st, task, cfg, nil) // preview only; dispatch_run executes
	cmd := d.ProviderCmd
	if cmd == "" {
		cmd = d.Provider
	}
	if optionalJSONFlag(jsonFlags) {
		writeCLIJSON(map[string]any{
			"task": task, "class": d.Class, "source": d.Source, "cmd": cmd,
			"provider": d.Provider, "profile": d.Profile, "model": d.Model,
			"effort":           firstNonEmpty(d.NativeEffort, d.Effort),
			"selection_reason": d.Selection, "reason": d.Reason,
		})
		return
	}
	fmt.Printf("route decision:\n")
	fmt.Printf("  class:  %s\n", d.Class)
	fmt.Printf("  source: %s\n", d.Source)
	fmt.Printf("  provider: %s\n", firstNonEmpty(d.Provider, cmd, "(none available)"))
	fmt.Printf("  model:    %s\n", firstNonEmpty(d.Model, "(not selected)"))
	fmt.Printf("  effort:   %s\n", firstNonEmpty(d.NativeEffort, d.Effort, "(not selected)"))
	if d.Selection != "" {
		fmt.Printf("  selection: %s\n", d.Selection)
	}
	if d.ProviderCmd != "" {
		fmt.Printf("  fallback: %s\n", d.ProviderCmd)
	}
	fmt.Printf("  reason: %s\n", d.Reason)
}
