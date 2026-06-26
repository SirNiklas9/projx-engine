//go:build !linux

package main

import "fmt"

// runAgentCaged is unavailable off Linux — the cage's hard enforcement
// (Landlock/netns/FUSE) is Linux-specific. Per feedback-cage-optional-not-required
// the cage is opt-in and never required: on other platforms, run uncaged.
func runAgentCaged(_, _ string) error {
	return fmt.Errorf("caged execution is Linux-only; run without --caged (uncaged is the cross-platform default)")
}
