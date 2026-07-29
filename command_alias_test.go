package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallPublicCommandAliasIsIdempotentAndTracksManagedRuntime(t *testing.T) {
	home := t.TempDir()
	firstTarget := filepath.Join(home, "runtime one", "projx-engine")
	if runtime.GOOS == "windows" {
		firstTarget += ".exe"
	}

	path, changed, err := installPublicCommandAlias(home, firstTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first install should write the public command alias")
	}
	if runtime.GOOS == "windows" && filepath.Ext(path) != ".cmd" {
		t.Fatalf("Windows alias = %q, want .cmd", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), firstTarget) {
		t.Fatalf("alias does not target managed runtime: %q", body)
	}

	if _, changed, err = installPublicCommandAlias(home, firstTarget); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Fatal("same managed runtime should not rewrite the alias")
	}

	secondTarget := filepath.Join(home, "runtime two", "projx-engine")
	if runtime.GOOS == "windows" {
		secondTarget += ".exe"
	}
	if _, changed, err = installPublicCommandAlias(home, secondTarget); err != nil {
		t.Fatal(err)
	} else if !changed {
		t.Fatal("new managed runtime should refresh the alias")
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), secondTarget) || strings.Contains(string(body), firstTarget) {
		t.Fatalf("alias was not refreshed safely: %q", body)
	}
}
