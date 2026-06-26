//go:build linux

package main

import (
	cage "github.com/BananaLabs-OSS/Pulp-cage"
	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// launchAgentCaged runs the agent in the full cage with the control plane's
// PermHub as the approver and the shared grants store — so a caged miss becomes
// a live request streamed to clients, and an approval unblocks the agent.
func launchAgentCaged(absRoot, task string, hub *PermHub, gstore grants.GrantStore) (int, error) {
	spec := buildCageSpec(absRoot, task, hub, gstore)
	res, err := cage.RunCagedAgent(spec)
	if err != nil {
		return 0, err
	}
	return res.ExitCode, nil
}
