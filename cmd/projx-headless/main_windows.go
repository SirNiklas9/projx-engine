//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

func main() {
	proxy, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "projx headless adapter: resolve executable:", err)
		os.Exit(127)
	}
	engine := filepath.Join(filepath.Dir(proxy), "projx-engine.exe")
	cmd := exec.Command(engine, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "projx headless adapter:", err)
		os.Exit(127)
	}
}
