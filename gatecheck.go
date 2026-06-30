package main

import (
	store "github.com/SirNiklas9/projx-store"
)

// gateDeniedPath delegates to the shared store matcher (one definition for every face).
func gateDeniedPath(s store.Store, path string) (pattern string, denied bool) {
	return store.GateDenied(s, path)
}
