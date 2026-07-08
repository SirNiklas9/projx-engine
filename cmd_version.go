package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// version is the human-set release version and the source of truth for
// `projx-engine version`. Bump it in lockstep with the git tag (tag v0.4.0 ->
// version = "0.4.0"). Overridable at build time: -ldflags "-X main.version=<v>".
var version = "0.4.0"

// releaseAPI is the GitHub latest-release endpoint for the public repo. Used by
// `version --check` to report whether a newer release is available. The skill
// performs the actual download/swap — this command only reports.
const releaseAPI = "https://api.github.com/repos/SirNiklas9/projx-engine/releases/latest"

// runVersionCmd implements `projx-engine version [--check]`. It prints the
// release version plus the VCS revision/time that `go build` stamps into the
// binary. With --check it also queries the latest GitHub release and reports
// whether an update is available (read-only; it never modifies anything).
func runVersionCmd(args []string) {
	check := false
	for _, a := range args {
		if a == "--check" || a == "-check" {
			check = true
		}
	}

	fmt.Printf("projx-engine v%s\n", strings.TrimPrefix(version, "v"))

	rev, when, dirty := vcsInfo()
	if rev != "" {
		short := rev
		if len(short) > 12 {
			short = short[:12]
		}
		suffix := ""
		if dirty {
			suffix = " (dirty)"
		}
		fmt.Printf("  commit:  %s%s\n", short, suffix)
	}
	if when != "" {
		fmt.Printf("  built:   %s\n", when)
	}
	fmt.Printf("  go:      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if check {
		reportUpdate()
	}
}

// reportUpdate fetches the latest release tag and prints whether the running
// build is behind it. Failures are non-fatal — a version check should never
// break the command (offline, rate-limited, confined egress, etc.).
func reportUpdate() {
	latest, err := latestReleaseTag()
	if err != nil {
		fmt.Printf("  update:  check failed (%v)\n", err)
		return
	}
	switch cmpVer(parseVer(version), parseVer(latest)) {
	case -1:
		fmt.Printf("  update:  available v%s -> %s (run the projx skill to update)\n",
			strings.TrimPrefix(version, "v"), latest)
	case 1:
		fmt.Printf("  update:  up to date (ahead of latest release %s)\n", latest)
	default:
		fmt.Printf("  update:  up to date (latest %s)\n", latest)
	}
}

// latestReleaseTag returns the tag_name of the latest GitHub release.
func latestReleaseTag() (string, error) {
	req, err := http.NewRequest(http.MethodGet, releaseAPI, nil)
	if err != nil {
		return "", err
	}
	// GitHub requires a User-Agent; the versioned Accept header pins the schema.
	req.Header.Set("User-Agent", "projx-engine")
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned %s", resp.Status)
	}

	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("no tag_name in latest release")
	}
	return rel.TagName, nil
}

// parseVer turns "v0.3.0" / "0.3.0" into comparable numeric fields. Non-numeric
// or missing fields become 0, so a malformed tag sorts low rather than panicking.
func parseVer(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop any pre-release / build suffix (e.g. "0.3.0-rc1", "0.3.0+meta").
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(part)
		out[i] = n
	}
	return out
}

// cmpVer returns -1 if a<b, 1 if a>b, 0 if equal.
func cmpVer(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// vcsInfo pulls the vcs.* build settings that `go build` embeds. Returns empty
// values when build info is unavailable (e.g. built with -buildvcs=false).
func vcsInfo() (rev, when string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, when, dirty
}
