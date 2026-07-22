//go:build windows

package main

import (
	"bytes"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadlessProxyPreservesPipedProtocolOutput(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "projx-engine.exe")
	proxy := filepath.Join(dir, "projx-engine-headless.exe")
	build := func(output string, args ...string) {
		cmdArgs := append([]string{"build", "-o", output}, args...)
		cmd := exec.Command("go", cmdArgs...)
		cmd.Dir = "."
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(cmdArgs, " "), err, out)
		}
	}
	build(engine, ".")
	build(proxy, "-ldflags=-H=windowsgui", "./cmd/projx-headless")

	image, err := pe.Open(proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	var subsystem uint16
	switch header := image.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem = header.Subsystem
	case *pe.OptionalHeader64:
		subsystem = header.Subsystem
	default:
		t.Fatalf("unknown PE optional header %T", image.OptionalHeader)
	}
	if subsystem != 2 {
		t.Fatalf("proxy subsystem = %d; want Windows GUI (2)", subsystem)
	}

	out, err := exec.Command(proxy, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("proxy version: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "projx-engine") {
		t.Fatalf("proxy lost child stdout: %s", out)
	}

	// Replace the sibling with this test executable so one hermetic helper can
	// prove all three protocol streams and exit-code forwarding.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(engine); err != nil {
		t.Fatal(err)
	}
	if err := copyImmutable(self, engine); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(proxy, "-test.run=TestHeadlessProxyProtocolHelper")
	cmd.Env = append(os.Environ(), "PROJX_HEADLESS_TEST_HELPER=1")
	cmd.Stdin = strings.NewReader("request-body")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("proxy exit = %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "stdout:request-body" || stderr.String() != "stderr:request-body" {
		t.Fatalf("proxy streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestHeadlessProxyProtocolHelper(t *testing.T) {
	if os.Getenv("PROJX_HEADLESS_TEST_HELPER") != "1" {
		return
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(24)
	}
	fmt.Fprint(os.Stdout, "stdout:"+string(body))
	fmt.Fprint(os.Stderr, "stderr:"+string(body))
	os.Exit(23)
}
