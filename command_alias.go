package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// installPublicCommandAlias keeps projx-engine as the stable integration
// binary while exposing the shorter projx command for interactive use.
func installPublicCommandAlias(home, target string) (string, bool, error) {
	name := "projx"
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	path := filepath.Join(home, ".local", "bin", name)
	data := publicCommandAlias(target)

	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return path, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	if err := atomicWriteFile(path, data); err != nil {
		return "", false, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			return "", false, err
		}
	}
	return path, true, nil
}

func publicCommandAlias(target string) []byte {
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		target = strings.ReplaceAll(target, "%", "%%")
		return []byte(fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", target))
	}
	quoted := "'" + strings.ReplaceAll(target, "'", "'\"'\"'") + "'"
	return []byte("#!/bin/sh\nexec " + quoted + " \"$@\"\n")
}
