//go:build linux

package caged

import (
	"fmt"
	"os"
	"strings"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
	egress "github.com/BananaLabs-OSS/Pulp-ext-egress/core"
)

// init wires the load-bearing composition seam: the egress netns child, after
// the network namespace + TUN wall is in place, calls PreExecHook in the SAME
// thread immediately before syscall.Exec. We point it at ApplyLandlockFromEnv
// so Landlock is applied in that exact window (the ordering that was a prior
// bug if violated). Setting it in init() means any binary that imports caged
// gets the seam wired without touching main().
func init() {
	egress.PreExecHook = core.ApplyLandlockFromEnv
}

// runCaged is the Linux composed launch: jail PATH + secret/env injection +
// egress netns (malicious-proof) + Landlock FS (applied in-child before exec).
func runCaged(policy CagedPolicy) (CagedResult, error) {
	if len(policy.Argv) == 0 {
		return CagedResult{}, fmt.Errorf("caged: RunCaged: empty argv")
	}

	var audit []AuditEvent

	jailDir, cleanup, jerr := buildJail(policy, &audit)
	if jerr != nil {
		return CagedResult{}, fmt.Errorf("caged: build jail: %w", jerr)
	}
	defer cleanup()

	fsPolicy := buildCorePolicy(policy, jailDir)

	env := composeChildEnv(policy, jailDir, &audit)

	// Convey the Landlock policy to the egress child via env vars so the
	// PreExecHook (ApplyLandlockFromEnv) can reconstruct + apply it in-child.
	env = injectConfinePolicyEnv(env, fsPolicy)
	// The egress child shells out to `ip` (iproute2) during netns setup, so
	// ensure standard system bin dirs are on PATH even if the jail pinned PATH
	// to jailDir only. Landlock + the jail allowlist still bound what the AGENT
	// can exec; this only affects the netns-setup helper.
	env = ensureSystemPathInEnv(env)

	audit = append(audit, AuditEvent{
		Kind:   "fs",
		Detail: fmt.Sprintf("landlock confine to root=%s (ro=%d rw=%d)", fsPolicy.Root, len(fsPolicy.ReadOnly), len(fsPolicy.ReadWrite)),
	})
	if len(policy.NetAllow) > 0 {
		audit = append(audit, AuditEvent{Kind: "net", Detail: "egress netns allowing: " + strings.Join(policy.NetAllow, ", ")})
	} else {
		audit = append(audit, AuditEvent{Kind: "net", Detail: "egress netns deny-all (no allowed names)", Denied: true})
	}
	if policy.NetOnMiss != nil {
		audit = append(audit, AuditEvent{Kind: "net", Detail: "live egress grants enabled (request-access on miss)"})
	}
	audit = append(audit, AuditEvent{Kind: "launch", Detail: "exec " + policy.Argv[0]})

	ep := egress.Policy{AllowNames: policy.NetAllow}
	// Wire the runtime grant seam: an un-allowed name is routed to the broker
	// hook. No Live cache here on purpose — every miss re-consults, so a revoked
	// grant re-blocks the name on its next resolution.
	ep.OnConnectMiss = policy.NetOnMiss
	exitCode, launchErr := egress.RunConfinedNetns(ep, policy.Argv, env, policy.Dir)
	if launchErr != nil {
		return CagedResult{AuditEvents: audit}, fmt.Errorf("caged: RunConfinedNetns: %w", launchErr)
	}
	return CagedResult{ExitCode: exitCode, AuditEvents: audit}, nil
}

// ensureSystemPathInEnv prepends standard system binary directories to PATH in
// env if not already present (needed so the netns-setup child can find `ip`).
func ensureSystemPathInEnv(env []string) []string {
	const systemDirs = "/usr/sbin:/sbin:/usr/bin:/bin"
	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			current := kv[len("PATH="):]
			if strings.Contains(current, "/usr/sbin") {
				return env
			}
			out := make([]string, len(env))
			copy(out, env)
			out[i] = "PATH=" + systemDirs + ":" + current
			return out
		}
	}
	out := make([]string, len(env)+1)
	copy(out, env)
	out[len(env)] = "PATH=" + systemDirs
	return out
}

// injectConfinePolicyEnv sets PROJX_CONFINE_ROOT/RO/RW in env (stripping prior
// values) so core.ApplyLandlockFromEnv can reconstruct the policy in-child.
func injectConfinePolicyEnv(env []string, p core.Policy) []string {
	strip := []string{"PROJX_CONFINE_ROOT=", "PROJX_CONFINE_RO=", "PROJX_CONFINE_RW="}
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		skip := false
		for _, s := range strip {
			if strings.HasPrefix(kv, s) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	out = append(out, "PROJX_CONFINE_ROOT="+p.Root)
	if len(p.ReadOnly) > 0 {
		out = append(out, "PROJX_CONFINE_RO="+strings.Join(p.ReadOnly, string(os.PathListSeparator)))
	}
	if len(p.ReadWrite) > 0 {
		out = append(out, "PROJX_CONFINE_RW="+strings.Join(p.ReadWrite, string(os.PathListSeparator)))
	}
	return out
}
