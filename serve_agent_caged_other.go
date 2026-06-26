//go:build !linux

package main

import (
	"fmt"

	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// launchAgentCaged is unavailable off Linux — the cage is Linux-specific. Per
// feedback-cage-optional-not-required, run uncaged on other platforms.
func launchAgentCaged(_, _ string, _ *PermHub, _ grants.GrantStore) (int, error) {
	return 0, fmt.Errorf("caged execution is Linux-only; run uncaged (caged:false)")
}
