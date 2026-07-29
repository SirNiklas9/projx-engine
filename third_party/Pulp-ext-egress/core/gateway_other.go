//go:build !linux

package core

import "errors"

// PreExecHook is a no-op placeholder on non-Linux platforms. It is defined here
// so callers can set it unconditionally without a build tag.
var PreExecHook func() error

// Init is a no-op on non-Linux platforms.
func Init() {}

// RunConfinedNetns returns an error on non-Linux platforms.
// Network namespace confinement via TUN/gVisor is Linux-only.
func RunConfinedNetns(policy Policy, argv []string, env []string, dir string) (int, error) {
	return 0, errors.New("egress gateway: netns/TUN only supported on linux")
}
