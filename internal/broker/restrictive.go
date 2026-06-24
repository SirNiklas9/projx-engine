package broker

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// RestrictiveBroker is the deterministic, default-deny policy engine for agent actions.
// It is the "teeth" of the sandbox: only explicitly allowlisted binaries, paths inside
// ProjectRoot, and allowlisted network hostnames are permitted. Every other action is
// denied with a descriptive reason.
//
// Security contract:
//   - Unknown Kind → deny.
//   - exec: basename-only matching after stripping OS executable extensions. An absolute
//     path does NOT bypass the check — only the basename is tested against AllowBins.
//   - read/write: path must be contained within ProjectRoot after filepath.Clean. Symlinks
//     are evaluated (best-effort; TOCTOU window exists — see limitations below).
//   - net: hostname extracted from URL must be in AllowHosts.
//
// Known limitations (honest disclosure):
//   - Symlink evaluation is TOCTOU: a symlink could be swapped between the Lstat and
//     EvalSymlinks calls. A production implementation should open the file with O_NOFOLLOW
//     and walk the path under a file-descriptor lock.
//   - Foreign-OS paths (e.g. a Windows path evaluated on Linux or vice-versa for the
//     read/write kind) are not specially handled: the exec kind manually splits on both
//     separators, but read/write use the host-OS filepath package.
//   - AllowBins entries are normalized at construction time; callers must not mutate the
//     map after NewRestrictiveBroker returns.
type RestrictiveBroker struct {
	AllowBins   map[string]bool // allowlisted exec basenames, lowercased, extension-stripped
	ProjectRoot string          // absolute, cleaned, symlink-resolved root
	AllowHosts  map[string]bool // allowlisted net hostnames, lowercased
}

// execExtensions lists the well-known executable extensions (all lowercase, with dot)
// that are stripped when normalising an exec target basename.
var execExtensions = map[string]bool{
	".exe": true,
	".cmd": true,
	".bat": true,
	".com": true,
	".ps1": true,
}

// NewRestrictiveBroker constructs a RestrictiveBroker and normalises all inputs.
// bins entries are lowercased and have well-known executable extensions stripped.
// projectRoot is made absolute and symlink-resolved.
// hosts entries are lowercased.
// Returns an error if projectRoot is empty.
func NewRestrictiveBroker(bins []string, projectRoot string, hosts []string) (*RestrictiveBroker, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return nil, errors.New("broker: projectRoot must not be empty")
	}

	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("broker: cannot make projectRoot absolute: %w", err)
	}
	// EvalSymlinks only succeeds if the path exists; if it does not exist yet (test roots
	// that will be created by the caller) keep the Abs-cleaned version.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	// Final clean to normalise trailing separators etc.
	abs = filepath.Clean(abs)

	allowBins := make(map[string]bool, len(bins))
	for _, b := range bins {
		allowBins[normaliseExecName(b)] = true
	}

	allowHosts := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		allowHosts[strings.ToLower(h)] = true
	}

	return &RestrictiveBroker{
		AllowBins:   allowBins,
		ProjectRoot: abs,
		AllowHosts:  allowHosts,
	}, nil
}

// Check evaluates the action against the broker's policy and returns a Decision.
// Default is deny; only explicitly allowlisted actions receive Allow==true.
func (b *RestrictiveBroker) Check(a Action) Decision {
	switch a.Kind {
	case "exec":
		return b.checkExec(a.Target)
	case "read", "write":
		return b.checkPath(a.Kind, a.Target)
	case "net":
		return b.checkNet(a.Target)
	default:
		return Decision{Allow: false, Reason: fmt.Sprintf("broker: unknown action kind %q", a.Kind)}
	}
}

// compile-time assertion: RestrictiveBroker must satisfy Broker.
var _ Broker = (*RestrictiveBroker)(nil)

// ── exec ──────────────────────────────────────────────────────────────────────

// checkExec enforces the exec allowlist.
func (b *RestrictiveBroker) checkExec(target string) Decision {
	if strings.TrimSpace(target) == "" {
		return Decision{Allow: false, Reason: "broker: exec target is empty"}
	}
	norm := normaliseExecName(execBasename(target))
	if b.AllowBins[norm] {
		return Decision{Allow: true, Reason: fmt.Sprintf("broker: exec %q is allowlisted", norm)}
	}
	return Decision{Allow: false, Reason: fmt.Sprintf("broker: exec %q is not in the allowlist", norm)}
}

// execBasename returns the final component of a path, splitting on both '/' and '\'
// so that foreign-OS paths (e.g. a Windows absolute path sent to a Linux host, or
// vice-versa) are handled correctly without relying on the host OS filepath package.
func execBasename(target string) string {
	// Split on both separators.
	idx := strings.LastIndexAny(target, "/\\")
	if idx >= 0 {
		return target[idx+1:]
	}
	return target
}

// normaliseExecName lowercases the name and strips a trailing well-known executable
// extension so that e.g. "powershell.exe" and "powershell" both normalise to "powershell".
func normaliseExecName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	ext := filepath.Ext(name)
	if execExtensions[ext] {
		name = name[:len(name)-len(ext)]
	}
	return name
}

// ── read / write ──────────────────────────────────────────────────────────────

// checkPath enforces that the target path is within ProjectRoot.
func (b *RestrictiveBroker) checkPath(kind, target string) Decision {
	if strings.TrimSpace(target) == "" {
		return Decision{Allow: false, Reason: fmt.Sprintf("broker: %s target is empty", kind)}
	}

	// Reject UNC paths outright (\\server\share).
	if strings.HasPrefix(target, `\\`) {
		return Decision{Allow: false, Reason: fmt.Sprintf("broker: %s target is a UNC path and is not allowed: %q", kind, target)}
	}

	// Compute the cleaned absolute path.
	cleaned := toAbsCleaned(target, b.ProjectRoot)

	// Check containment before any symlink resolution (catches most traversals cheaply).
	if deny, reason := b.containmentCheck(kind, cleaned); deny {
		return Decision{Allow: false, Reason: reason}
	}

	// Symlink resolution (best-effort; a TOCTOU window remains — see type doc).
	// Resolve symlinks on the deepest EXISTING prefix of the path and re-append the
	// trailing components that don't exist yet. This catches a symlinked ANCESTOR
	// directory escaping the root even when the leaf (e.g. a file about to be created)
	// does not exist — the case a naive EvalSymlinks-on-the-whole-path misses.
	resolved := resolveDeepestAncestor(cleaned)
	if deny, reason := b.containmentCheck(kind, resolved); deny {
		return Decision{Allow: false, Reason: reason}
	}

	return Decision{Allow: true, Reason: fmt.Sprintf("broker: %s path is within project root", kind)}
}

// containmentCheck tests whether cleaned (an absolute, filepath.Clean'd path) is
// contained within b.ProjectRoot.  Returns (true, reason) on deny.
func (b *RestrictiveBroker) containmentCheck(kind, cleaned string) (bool, string) {
	// filepath.Rel gives us the relative path from root → target.
	// If that path starts with ".." the target is outside the root.
	rel, err := filepath.Rel(b.ProjectRoot, cleaned)
	if err != nil {
		// On Windows, filepath.Rel returns an error when the paths are on different volumes.
		return true, fmt.Sprintf("broker: %s path %q is on a different volume from project root", kind, cleaned)
	}
	// Check for traversal.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true, fmt.Sprintf("broker: %s path %q escapes project root", kind, cleaned)
	}
	// filepath.Rel on Windows will happily compute a relative path between two different
	// drive roots if it can express it without ".."; guard absolute paths that are not
	// under the root by checking the prefix explicitly (with separator guard).
	if !isDescendantOf(cleaned, b.ProjectRoot) {
		return true, fmt.Sprintf("broker: %s path %q is not within project root %q", kind, cleaned, b.ProjectRoot)
	}
	return false, ""
}

// isDescendantOf returns true when target is equal to root or is a descendant of root.
// It guards the prefix-sibling bug: root=/proj must not match /proj-evil.
// Both arguments must already be filepath.Clean'd absolute paths.
func isDescendantOf(target, root string) bool {
	if target == root {
		return true
	}
	// Ensure root ends with a separator for the HasPrefix comparison so that
	// /proj does not match /proj-evil.
	prefix := root + string(filepath.Separator)
	return strings.HasPrefix(target, prefix)
}

// toAbsCleaned returns an absolute, filepath.Clean'd version of target.
// If target is relative, it is joined to projectRoot.
func toAbsCleaned(target, projectRoot string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(projectRoot, target))
}

// resolveDeepestAncestor resolves symlinks on the deepest EXISTING prefix of path and
// re-appends the trailing components that do not exist yet. This catches a symlinked
// ancestor directory escaping the root even when the leaf (e.g. a file being created)
// does not exist — which a plain EvalSymlinks(whole-path) misses because it errors on
// the missing leaf. The trailing components cannot themselves be symlinks (they don't
// exist), so no further resolution is needed once the existing prefix is resolved.
// If nothing resolves (e.g. the volume root), returns the cleaned input unchanged.
func resolveDeepestAncestor(path string) string {
	var trailing []string
	cur := filepath.Clean(path)
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(trailing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, trailing[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(path)
		}
		trailing = append(trailing, filepath.Base(cur))
		cur = parent
	}
}

// ── net ───────────────────────────────────────────────────────────────────────

// checkNet enforces the network hostname allowlist.
func (b *RestrictiveBroker) checkNet(target string) Decision {
	if strings.TrimSpace(target) == "" {
		return Decision{Allow: false, Reason: "broker: net target is empty"}
	}

	u, err := url.Parse(target)
	if err != nil {
		return Decision{Allow: false, Reason: fmt.Sprintf("broker: net target %q is not a valid URL: %v", target, err)}
	}

	// url.Parse is very permissive; a bare hostname with no scheme is parsed as a
	// Path, not a Host.  If Host is empty after parsing, treat the raw target as
	// an unparseable host (deny).
	host := strings.ToLower(u.Hostname()) // strips port if present
	if host == "" {
		return Decision{Allow: false, Reason: fmt.Sprintf("broker: net target %q has no extractable host", target)}
	}

	if b.AllowHosts[host] {
		return Decision{Allow: true, Reason: fmt.Sprintf("broker: host %q is allowlisted", host)}
	}
	return Decision{Allow: false, Reason: fmt.Sprintf("broker: host %q is not in the allowlist", host)}
}
