package main

import (
	"fmt"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

// runModeCmd implements `projx-engine [--root <dir>] mode <name> [on|off]`.
//
// It is the convenience toggle over the store-declared enforcement modes, so a
// slash-command (/projx dispatcher on|off, /projx cage on|off) or a human can flip
// a mode without hand-writing a `store commit --kind gate-rule --key setting/…`:
//
//	mode dispatcher [on|off]   trunk-dispatch discipline  (setting/dispatcher-mode, SOFT)
//	mode cage       [on|off]   OS-level confinement default (setting/cage-mode)
//
// With no on|off argument it PRINTS the current state (read-only). With on|off it
// upserts the setting gate-rule record under the SAME id the seed uses, so it
// replaces in place rather than piling up duplicates.
//
// It deliberately does NOT expose override-authority. That flag is HUMAN-controlled:
// the AI may request an override but only a human delegates the authority, and the
// hook blocks the AI from flipping it. Exposing it here would be a self-service
// bypass — exactly what the delegation model forbids. Toggle it out-of-band as a
// human via `store commit --key setting/override-authority`, never through `mode`.
func runModeCmd(absRoot string, args []string) {
	if len(args) == 0 {
		die("usage: mode <dispatcher|cage> [on|off]")
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))

	var key string
	switch name {
	case "dispatcher", "dispatcher-mode", "disp":
		key = store.SettingDispatcherMode
	case "cage", "cage-mode":
		key = store.SettingCageMode
	case "override-authority", "override", "authority":
		die("mode: %q is human-controlled and cannot be toggled here — the AI may request it, but only a human delegates it (commit setting/override-authority out-of-band)", name)
	default:
		die("mode: unknown mode %q (want dispatcher|cage)", name)
	}

	st := openStore(absRoot)
	defer st.Close()

	// No value → report current state, no write.
	if len(args) == 1 {
		fmt.Printf("%s: %s\n", key, modeState(st, key))
		return
	}

	body, err := normalizeOnOff(args[1])
	if err != nil {
		die("mode: %v", err)
	}

	id := "gate-rule/" + slug(key)
	var bp *store.Record
	if before, had := st.Get(id); had {
		bp = &before
	}
	rec := store.Record{ID: id, Kind: store.KGateRule, Scope: store.ScopeProject, Key: key, Body: body, Origin: "ui:mode"}
	if err := st.Put(rec); err != nil {
		die("put: %v", err)
	}
	recordStoreOp(absRoot, "put", "ui", bp, &rec)
	fmt.Printf("%s: %s\n", key, body)
}

// modeState returns "on"/"off" for a setting key, reading through the same store
// getters the status view uses so the reported state can never drift from what the
// gate actually enforces.
func modeState(st store.Store, key string) string {
	var on bool
	switch key {
	case store.SettingDispatcherMode:
		on = store.DispatcherModeOn(st)
	case store.SettingCageMode:
		on = store.CageModeOn(st)
	}
	if on {
		return "on"
	}
	return "off"
}

// normalizeOnOff maps the accepted affirmative/negative spellings to "on"/"off",
// matching the bodies the store getters recognize.
func normalizeOnOff(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "yes":
		return "on", nil
	case "off", "false", "0", "no":
		return "off", nil
	}
	return "", fmt.Errorf("value must be on|off (got %q)", v)
}
