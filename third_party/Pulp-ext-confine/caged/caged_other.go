//go:build !linux

package caged

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
)

// runCaged on non-Linux platforms composes jail PATH + secret/env injection,
// then delegates the actual confined launch to the core confiner:
//
//	Windows — AppContainer FS confinement. When policy.NetAllow is non-empty the
//	          core grants the internetClient capability so the child can reach the
//	          (cooperative) egress proxy; when empty, no network capability is
//	          granted (full denial). Netns (malicious-proof) is Linux-only.
//	Other   — cooperative launch (no OS-level wall; honestly reported in audit).
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
	// The Windows confiner reads PROJX_JAIL_DIR from env to grant the jail dir
	// RX so the child can exec the allowlisted shims.
	if jailDir != "" {
		env = setEnv(env, "PROJX_JAIL_DIR", jailDir)
	}

	// Windows: claude.exe is a Node single-executable app that unpacks to
	// os.tmpdir() and needs writable scratch to start. Instead of granting the
	// real %TEMP% (a slow, invasive recursive icacls of the whole temp tree),
	// REDIRECT the agent's TEMP/TMP into a subdir of the already-granted project
	// root — writable scratch inside the cage, no external grant.
	if runtime.GOOS == "windows" && policy.Root != "" {
		cageTmp := filepath.Join(policy.Root, ".cage-tmp")
		_ = os.MkdirAll(cageTmp, 0o755)
		env = setEnv(env, "TEMP", cageTmp)
		env = setEnv(env, "TMP", cageTmp)
		audit = append(audit, AuditEvent{Kind: "fs", Detail: "redirected TEMP/TMP into cage: " + cageTmp})
	}

	c := core.Detect()
	audit = append(audit, AuditEvent{
		Kind:   "fs",
		Detail: fmt.Sprintf("%s confine to root=%s (ro=%d rw=%d)", c.Level(), fsPolicy.Root, len(fsPolicy.ReadOnly), len(fsPolicy.ReadWrite)),
	})
	if len(policy.NetAllow) > 0 {
		audit = append(audit, AuditEvent{Kind: "net", Detail: "internetClient granted (cooperative egress); allowed: " + fmt.Sprint(policy.NetAllow)})
	} else {
		audit = append(audit, AuditEvent{Kind: "net", Detail: "no network capability granted (denied)", Denied: true})
	}
	audit = append(audit, AuditEvent{Kind: "launch", Detail: "exec " + policy.Argv[0]})

	exitCode, launchErr := c.LaunchConfined(fsPolicy, policy.Argv, env, policy.Dir)
	if launchErr != nil {
		return CagedResult{AuditEvents: audit}, fmt.Errorf("caged: LaunchConfined: %w", launchErr)
	}
	return CagedResult{ExitCode: exitCode, AuditEvents: audit}, nil
}
