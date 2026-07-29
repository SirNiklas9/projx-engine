//go:build linux

// Package cage is the agent-agnostic orchestration layer: it composes the whole
// kernel cage — live-grant broker + FUSE filesystem floor + Landlock + egress
// netns — around ONE agent worker process. The worker (CagedPolicy.Argv) is any
// binary: claude, codex, a GPT CLI, anything. The cage gates SYSCALLS, not the
// agent, so a single composition works for every agent; per-agent specifics
// (config files, model flag, steering prompt) are produced by a thin adapter
// that fills Argv/Env before calling RunCagedAgent.
package cage

import (
	"fmt"
	"os"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/caged"
	fusecore "github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// AgentSpec describes one caged agent run. Argv is ANY agent binary.
type AgentSpec struct {
	Argv        []string          // the agent worker to launch (agnostic)
	ProjectRoot string            // backing dir the agent works in
	Store       grants.GrantStore // persistent grants (storage.sqlite)
	Approver    grants.Approver   // human-in-the-loop (CLI / phone)
	NetAllow    []string          // statically-allowed hosts
	FSAllow     []fusecore.Rule   // statically-allowed FS paths (rest needs a live grant)
	Env         map[string]string // extra env for the worker
}

// RunCagedAgent composes the WHOLE cage around one agent worker and blocks until
// it exits:
//   - a grants.Broker over Store + Approver (live grant + revoke),
//   - a FUSE floor over ProjectRoot whose misses route to the broker (live FS),
//   - Landlock confined to the FUSE mountpoint — the backing is NOT granted, so
//     the only path to the project is through the FUSE policy + live broker,
//   - an egress netns whose connect-misses route to the broker (live net).
//
// Agent-agnostic: Argv may be any binary.
func RunCagedAgent(spec AgentSpec) (caged.CagedResult, error) {
	if len(spec.Argv) == 0 {
		return caged.CagedResult{}, fmt.Errorf("cage: empty argv")
	}
	broker := &grants.Broker{
		Store:     spec.Store,
		Approver:  spec.Approver,
		CellID:    "agent",
		GrantedBy: "approver",
	}

	mnt, err := os.MkdirTemp("", "pulp-cage-mnt-")
	if err != nil {
		return caged.CagedResult{}, err
	}
	defer os.RemoveAll(mnt) // runs last (after Unmount)

	vd := &fusecore.VDrive{
		Backing: spec.ProjectRoot,
		Policy:  fusecore.Policy{Rules: spec.FSAllow},
		Hooks: fusecore.Hooks{
			OnMiss: func(rel string, want fusecore.Access) fusecore.Access {
				return fusecore.Access(broker.Decide(grants.KindFS, rel, int(want)))
			},
		},
	}
	srv, err := fusecore.Mount(mnt, vd)
	if err != nil {
		return caged.CagedResult{}, fmt.Errorf("cage: fuse mount: %w", err)
	}
	defer srv.Unmount() // runs first

	policy := caged.CagedPolicy{
		Argv:     spec.Argv,
		Root:     mnt, // Landlock grants the mountpoint, not the backing
		NetAllow: spec.NetAllow,
		NetOnMiss: func(name string) bool {
			return broker.Decide(grants.KindNet, name, 1) >= 1
		},
		Env: spec.Env,
		Dir: mnt,
	}
	return caged.RunCaged(policy)
}
