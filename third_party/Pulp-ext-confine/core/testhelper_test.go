package core_test

import (
	"bytes"
	"os/exec"
)

func runCmd(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	return buf.Bytes(), cmd.Run()
}

func runCmdInDir(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	return buf.Bytes(), cmd.Run()
}
