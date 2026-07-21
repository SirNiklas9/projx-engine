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

func managedBinaryRoot(home string) string { return filepath.Join(home, ".codex", "projx", "bin") }

func activateManagedBinary() (string, bool, error) {
	home, err := claudeHomeDir()
	if err != nil {
		return "", false, err
	}
	path, copied, err := provisionManagedBinary(home)
	if err == nil {
		configuredBinary = path
	}
	return path, copied, err
}

// provisionManagedBinary copies the running engine to immutable, user-owned
// Codex storage. New content gets a new path, so a Windows upgrade never needs
// to overwrite a locked executable; existing sessions finish on their old image.
func provisionManagedBinary(home string) (string, bool, error) {
	self, err := os.Executable()
	if err != nil {
		return "", false, fmt.Errorf("resolve running executable: %w", err)
	}
	in, err := os.Open(self)
	if err != nil {
		return "", false, fmt.Errorf("open running executable: %w", err)
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return "", false, fmt.Errorf("hash running executable: %w", err)
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))[:16]
	label := resolveVersion()
	if label == "" {
		label = "dev"
	}
	label = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(label)
	name := "projx-engine"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(managedBinaryRoot(home), label+"-"+digest, name)
	if _, err := os.Stat(dst); err == nil {
		return dst, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	if _, err := in.Seek(0, 0); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".projx-engine-*")
	if err != nil {
		return "", false, err
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		_ = tmp.Close()
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return "", false, err
	}
	if err := tmp.Chmod(0o755); err != nil {
		return "", false, err
	}
	if err := tmp.Sync(); err != nil {
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		if _, statErr := os.Stat(dst); statErr == nil {
			return dst, false, nil
		}
		return "", false, err
	}
	remove = false
	return dst, true, nil
}
