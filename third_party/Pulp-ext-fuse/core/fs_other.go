//go:build !linux

package core

import "errors"

// VDrive is the policy-enforcing virtual drive configuration.
type VDrive struct {
	Backing string
	Policy  Policy
	Hooks   Hooks
}

// Server represents a mounted VDrive.
type Server struct{}

// Mount is not supported on non-Linux platforms.
func Mount(mountpoint string, vd *VDrive) (*Server, error) {
	return nil, errors.New("virtual drive: FUSE only supported on linux (Windows ProjFS is a separate backend)")
}

// Unmount is a no-op on non-Linux platforms.
func (s *Server) Unmount() error { return nil }
