//go:build linux

package cage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	egresscore "github.com/BananaLabs-OSS/Pulp-ext-egress/core"
	fusecore "github.com/BananaLabs-OSS/Pulp-ext-fuse/core"
	grants "github.com/BananaLabs-OSS/Pulp-grants"
)

// TestMain lets egress.Init() claim the process when it is re-exec'd as the
// netns-setup child (it sets up the TUN, hands the fd back, applies Landlock,
// then execs the real worker). On a normal test run Init() returns immediately.
func TestMain(m *testing.M) {
	egresscore.Init()
	os.Exit(m.Run())
}

// autoApprover grants every request permanently — drives the harness with no
// human. (In production this is the CLI/phone approver.)
type autoApprover struct{ acc int }

func (a autoApprover) Decide(grants.Request) grants.Decision {
	return grants.Decision{Access: a.acc, Scope: grants.ScopePermanent}
}

// TestCageProbe runs INSIDE the cage as the re-exec'd worker (gated by env). Its
// working dir is the FUSE mountpoint, so it uses CWD-relative paths for the
// project and an absolute path for the out-of-cage escape attempt.
func TestCageProbe(t *testing.T) {
	if os.Getenv("CAGE_PROBE") != "1" {
		t.Skip("not the caged probe")
	}
	// 1. Statically-allowed file — must read.
	if b, err := os.ReadFile("normal.txt"); err != nil || string(b) != "NORMAL" {
		fmt.Printf("FS_NORMAL:FAIL:%v\n", err)
		os.Exit(11)
	}
	fmt.Println("FS_NORMAL:OK")

	// 2. Denied-by-policy file — only readable if the broker live-granted it.
	if b, err := os.ReadFile(filepath.Join("secret", "key.txt")); err != nil || string(b) != "TOPSECRET" {
		fmt.Printf("FS_SECRET:FAIL:%v\n", err)
		os.Exit(12)
	}
	fmt.Println("FS_SECRET:OK")

	// 3. Write to the real backing dir (outside the mount) — Landlock must EPERM.
	if backing := os.Getenv("CAGE_BACKING"); backing != "" {
		if err := os.WriteFile(filepath.Join(backing, "escape.txt"), []byte("x"), 0644); err == nil {
			fmt.Println("ESCAPE:REACHED")
			os.Exit(13)
		}
		fmt.Println("ESCAPE:BLOCKED")
	}
	os.Exit(0)
}

// TestCageFSFloor is the whole-system proof: compose the real cage around a
// generic probe (agent-agnostic) and assert it exits 0 — meaning the FUSE floor
// served the project, the broker live-granted the denied file, and Landlock
// blocked the out-of-cage write.
func TestCageFSFloor(t *testing.T) {
	if os.Getenv("CAGE_PROBE") == "1" {
		t.Skip("probe child")
	}

	// Backing OUTSIDE /tmp (DefaultPolicy grants /tmp RW), so the escape test is
	// meaningful. Use an ext4 path under $HOME.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".pulp-cage-test", fmt.Sprint(os.Getpid()))
	if err := os.MkdirAll(filepath.Join(root, "secret"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Join(home, ".pulp-cage-test")) })
	mustWrite(t, filepath.Join(root, "normal.txt"), "NORMAL")
	mustWrite(t, filepath.Join(root, "secret", "key.txt"), "TOPSECRET")

	self, _ := os.Readlink("/proc/self/exe")
	if self == "" {
		self = os.Args[0]
	}

	store := grants.NewMemStore()
	spec := AgentSpec{
		Argv:        []string{self, "-test.run=TestCageProbe", "-test.v"},
		ProjectRoot: root,
		Store:       store,
		Approver:    autoApprover{acc: int(fusecore.ReadWrite)},
		FSAllow:     []fusecore.Rule{{Prefix: "normal.txt", Access: fusecore.Read}},
		Env: map[string]string{
			"CAGE_PROBE":   "1",
			"CAGE_BACKING": root,
		},
	}

	res, err := RunCagedAgent(spec)
	if err != nil {
		t.Fatalf("RunCagedAgent: %v (audit: %+v)", err, res.AuditEvents)
	}
	if res.ExitCode != 0 {
		t.Fatalf("caged probe failed: exit %d (11=normal 12=secret-grant 13=escape-not-blocked); audit: %+v", res.ExitCode, res.AuditEvents)
	}

	// The secret grant should have been persisted by the broker.
	if _, ok := store.Lookup(grants.KindFS, "secret/key.txt", int(fusecore.Read)); !ok {
		t.Error("expected a persisted fs grant covering secret/key.txt")
	}
	t.Log("PASS: whole cage held — FUSE floor + live grant + Landlock escape-block, all through the real composed cage")
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
