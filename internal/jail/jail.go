// Package jail builds a restricted-PATH directory (the "exec jail") and manages
// the environment variables needed for brokered exec interposition.
//
// The jail works by placing shims (symlinks, hard links, or copies of the
// projx-engine binary) for each allowed binary into a dedicated directory.
// The agent's child process inherits PATH set to only that directory, so every
// exec call resolves to a shim instead of the real binary.  The shim detects
// it is not running as "projx-engine" (multi-call dispatch) and routes through
// RestrictiveBroker before forwarding to the real binary found on the
// PROJX_REAL_PATH that the jail injects into the environment.
package jail

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Jail describes a prepared jail directory.
type Jail struct {
	// Dir is the jail directory containing the shim links/copies.
	Dir string
	// RealPath is the original PATH (OS path-list format) from which the real
	// binaries will be resolved by the shim.
	RealPath string
	// Root is the project root that the RestrictiveBroker will enforce.
	Root string
	// AllowBins is the list of allowed binary basenames (no extension).
	AllowBins []string
	// AllowHosts is the list of allowed network hostnames.
	AllowHosts []string
}

// Build creates shims inside dir for each name in allowBins, pointing at
// enginePath (the projx-engine executable).  dir is created with
// os.MkdirAll if it does not already exist.
//
// For each allowed binary name, Build materialises:
//
//	<dir>/<name>          (non-Windows)
//	<dir>/<name>.exe      (Windows)
//
// It tries os.Symlink first; falls back to os.Link (hard link); falls back
// to copying the file.  All three produce a binary that the OS will exec.
func Build(dir, enginePath string, allowBins []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, name := range allowBins {
		shimName := name
		if runtime.GOOS == "windows" {
			// Strip any existing extension then add .exe so callers can pass
			// bare names ("git") or names with extensions ("git.exe") safely.
			ext := strings.ToLower(filepath.Ext(shimName))
			if ext == ".exe" || ext == ".cmd" || ext == ".bat" || ext == ".com" {
				shimName = shimName[:len(shimName)-len(ext)]
			}
			shimName += ".exe"
		}
		dst := filepath.Join(dir, shimName)
		if err := linkOrCopy(enginePath, dst); err != nil {
			return err
		}
	}
	return nil
}

// Env returns a copy of parentEnv with jail-related variables overridden.
// Specifically it sets:
//
//	PATH                    = j.Dir  (only the jail dir — the interposition)
//	PROJX_REAL_PATH         = j.RealPath
//	PROJX_BROKER_ROOT       = j.Root
//	PROJX_BROKER_ALLOW_BINS = comma-joined j.AllowBins
//	PROJX_BROKER_ALLOW_HOSTS= comma-joined j.AllowHosts
//
// Any pre-existing PATH / PROJX_REAL_PATH / PROJX_BROKER_* entries in
// parentEnv are removed before the new values are appended so there are no
// duplicates.
func (j *Jail) Env(parentEnv []string) []string {
	// Keys to strip (case-insensitive on Windows, exact elsewhere).
	stripPrefixes := []string{
		"PATH=",
		"PROJX_REAL_PATH=",
		"PROJX_BROKER_ROOT=",
		"PROJX_BROKER_ALLOW_BINS=",
		"PROJX_BROKER_ALLOW_HOSTS=",
	}

	out := make([]string, 0, len(parentEnv)+5)
	for _, kv := range parentEnv {
		if envKeyMatch(kv, stripPrefixes) {
			continue
		}
		out = append(out, kv)
	}

	out = append(out,
		"PATH="+j.Dir,
		"PROJX_REAL_PATH="+j.RealPath,
		"PROJX_BROKER_ROOT="+j.Root,
		"PROJX_BROKER_ALLOW_BINS="+strings.Join(j.AllowBins, ","),
		"PROJX_BROKER_ALLOW_HOSTS="+strings.Join(j.AllowHosts, ","),
	)
	return out
}

// ── helpers ───────────────────────────────────────────────────────────────────

// envKeyMatch returns true if kv starts with any of the given prefixes
// (case-insensitive comparison so Windows env works correctly).
func envKeyMatch(kv string, prefixes []string) bool {
	upper := strings.ToUpper(kv)
	for _, p := range prefixes {
		if strings.HasPrefix(upper, strings.ToUpper(p)) {
			return true
		}
	}
	return false
}

// linkOrCopy materialises dst as a reference to src.  It tries symlink first,
// then hard link, then a plain file copy (last resort for Windows w/o Developer
// Mode or cross-volume scenarios).  dst must not already exist; if it does
// the function is a no-op (returns nil) so repeated Build calls are safe.
func linkOrCopy(src, dst string) error {
	// Already exists — idempotent.
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}

	// Try symlink.
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}

	// Try hard link.
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	// Fall back to copy.
	return copyFile(src, dst)
}

// copyFile copies src to dst as a regular file with mode 0o755.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
