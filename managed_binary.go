package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var configuredBinary string
var configuredHeadlessBinary string

type managedRuntime struct {
	CLI      string
	Headless string
}

func managedBinaryRoot(home string) string { return filepath.Join(home, ".codex", "projx", "bin") }

func activateManagedBinary() (string, bool, error) {
	home, err := claudeHomeDir()
	if err != nil {
		return "", false, err
	}
	rt, copied, err := provisionManagedRuntime(home)
	if err == nil {
		configuredBinary = rt.CLI
		configuredHeadlessBinary = rt.Headless
	}
	return rt.CLI, copied, err
}

func provisionManagedBinary(home string) (string, bool, error) {
	rt, copied, err := provisionManagedRuntime(home)
	return rt.CLI, copied, err
}

// provisionManagedRuntime copies the console engine and its separate background
// proxy to one immutable, content-addressed directory. Release/build tooling puts
// the proxy beside the CLI. Test binaries use themselves as a harmless stand-in;
// production bootstrap fails closed when the release is missing its proxy asset.
func provisionManagedRuntime(home string) (managedRuntime, bool, error) {
	cliSource, err := os.Executable()
	if err != nil {
		return managedRuntime{}, false, fmt.Errorf("resolve running executable: %w", err)
	}
	headlessSource, err := managedHeadlessSource(cliSource)
	if err != nil {
		return managedRuntime{}, false, err
	}
	return provisionManagedRuntimeFrom(home, cliSource, headlessSource)
}

func managedHeadlessSource(cliSource string) (string, error) {
	if runtime.GOOS != "windows" {
		return cliSource, nil
	}
	candidate := filepath.Join(filepath.Dir(cliSource), "projx-engine-headless.exe")
	if fileExists(candidate) {
		return candidate, nil
	}
	// `go test` executables are not release artifacts; allowing the test image as
	// a stand-in keeps existing bootstrap tests hermetic without weakening release
	// provisioning.
	if strings.HasSuffix(strings.ToLower(cliSource), ".test.exe") {
		return cliSource, nil
	}
	return "", fmt.Errorf("managed runtime is incomplete: %s is missing", candidate)
}

func provisionManagedRuntimeFrom(home, cliSource, headlessSource string) (managedRuntime, bool, error) {
	digest, err := runtimeDigest(cliSource, headlessSource)
	if err != nil {
		return managedRuntime{}, false, err
	}
	label := resolveVersion()
	if label == "" {
		label = "dev"
	}
	label = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(label)
	cliName := "projx-engine"
	headlessName := cliName
	if runtime.GOOS == "windows" {
		cliName += ".exe"
		headlessName += "-headless.exe"
	}
	dir := filepath.Join(managedBinaryRoot(home), label+"-"+digest)
	rt := managedRuntime{CLI: filepath.Join(dir, cliName), Headless: filepath.Join(dir, headlessName)}
	if fileExists(rt.CLI) && fileExists(rt.Headless) {
		return rt, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return managedRuntime{}, false, err
	}
	if err := copyImmutable(cliSource, rt.CLI); err != nil {
		return managedRuntime{}, false, err
	}
	if rt.Headless != rt.CLI {
		if err := copyImmutable(headlessSource, rt.Headless); err != nil {
			return managedRuntime{}, false, err
		}
	}
	return rt, true, nil
}

func runtimeDigest(paths ...string) (string, error) {
	h := sha256.New()
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open runtime asset %s: %w", path, err)
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], nil
}

func copyImmutable(src, dst string) error {
	if fileExists(dst) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".projx-runtime-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil && !fileExists(dst) {
		return err
	}
	return nil
}
