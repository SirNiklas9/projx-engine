//go:build linux

package caged

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BananaLabs-OSS/Pulp-ext-confine/core"
	egress "github.com/BananaLabs-OSS/Pulp-ext-egress/core"
)

// TestMain handles the egress-child re-exec dispatch. caged's init() already set
// egress.PreExecHook = core.ApplyLandlockFromEnv, so the re-exec'd child applies
// Landlock right before exec — exactly the composed ordering RunCaged relies on.
func TestMain(m *testing.M) {
	egress.Init() // exits if NETGW_MODE=child; no-op otherwise
	os.Exit(m.Run())
}

// fakeAgentSource is a tiny probe that records FS + net access results to a file.
const fakeAgentSource = `package main

import (
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	inside  := os.Getenv("PROBE_INSIDE")
	outside := os.Getenv("PROBE_OUTSIDE")
	result  := os.Getenv("PROBE_RESULT")

	var t []string
	if _, err := os.ReadFile(inside); err == nil {
		t = append(t, "FS_INSIDE:OK")
	} else {
		t = append(t, "FS_INSIDE:DENIED")
	}
	if _, err := os.ReadFile(outside); err == nil {
		t = append(t, "FS_OUTSIDE:OK")
	} else {
		t = append(t, "FS_OUTSIDE:DENIED")
	}
	// Any outbound connection should be blocked by the deny-all netns.
	c, err := net.DialTimeout("tcp", "10.0.0.99:80", 1*time.Second)
	if err == nil {
		c.Close()
		t = append(t, "NET:REACHED")
	} else {
		t = append(t, "NET:DENIED")
	}
	_ = os.WriteFile(result, []byte(strings.Join(t, "\n")+"\n"), 0o644)
}
`

func buildFakeAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeAgentSource), 0o644); err != nil {
		t.Fatalf("write agent src: %v", err)
	}
	bin := filepath.Join(dir, "agent")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build fake agent: %v\n%s", err, out)
	}
	return bin
}

// TestRunCagedComposes proves the relocated composition: a single RunCaged call
// applies BOTH Landlock FS confinement (inside root OK, outside DENIED) AND a
// deny-all egress netns (outbound DENIED) to the launched child.
func TestRunCagedComposes(t *testing.T) {
	if os.Getenv("NETGW_CONFINED") == "1" {
		t.Skip("must not run inside the confined netns")
	}
	if !core.Detect().Available() {
		t.Skip("Landlock not available (cooperative only)")
	}

	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	outside := filepath.Join(home, fmt.Sprintf(".caged-outside-%d", os.Getpid()))
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Skipf("cannot write outside file: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	result := filepath.Join(root, "result.txt")
	agent := buildFakeAgent(t)

	res, err := RunCaged(CagedPolicy{
		Argv: []string{agent},
		Root: root,
		Env: map[string]string{
			"PROBE_INSIDE":  inside,
			"PROBE_OUTSIDE": outside,
			"PROBE_RESULT":  result,
			"NETGW_CONFINED": "1",
		},
		Dir: root,
		// NetAllow empty → deny-all egress netns; FS still confined.
	})
	if err != nil {
		t.Fatalf("RunCaged: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("agent exit %d", res.ExitCode)
	}

	data, rerr := os.ReadFile(result)
	if rerr != nil {
		t.Fatalf("read result: %v", rerr)
	}
	got := strings.TrimSpace(string(data))
	t.Logf("caged agent result:\n%s\naudit: %+v", got, res.AuditEvents)

	for _, want := range []string{"FS_INSIDE:OK", "FS_OUTSIDE:DENIED", "NET:DENIED"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing marker %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FS_OUTSIDE:OK") {
		t.Errorf("Landlock not enforcing: outside read succeeded")
	}
	if strings.Contains(got, "NET:REACHED") {
		t.Errorf("egress netns not enforcing: outbound reached")
	}
}
