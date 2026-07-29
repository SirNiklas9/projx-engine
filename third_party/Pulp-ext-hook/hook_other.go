//go:build !windows

package hook

import "errors"

// errUnsupported is returned by the cross-platform stubs: the AppContainer hook
// is a Windows-only mechanism (Linux uses Landlock + netns, which need no hook).
var errUnsupported = errors.New("Pulp-ext-hook: AppContainer hook injection is Windows-only")

// StageDLL is unsupported off Windows.
func StageDLL(dir string) (string, error) { return "", errUnsupported }

// Inject is unsupported off Windows.
func Inject(proc uintptr, dllPath string) error { return errUnsupported }
